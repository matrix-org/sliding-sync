package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/getsentry/sentry-go"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"github.com/tidwall/gjson"
)

// Accumulator tracks room state and timelines.
//
// In order for it to remain simple(ish), the accumulator DOES NOT SUPPORT arbitrary timeline gaps.
// There is an Initialise function for new rooms (with some pre-determined state) and then a constant
// Accumulate function for timeline events. v2 sync must be called with a large enough timeline.limit
// for this to work!
type Accumulator struct {
	db                     *sqlx.DB
	roomsTable             *RoomsTable
	eventsTable            *EventTable
	snapshotTable          *SnapshotTable
	spacesTable            *SpacesTable
	entityName             string
	snapshotMemberCountVec *prometheus.HistogramVec // TODO: Remove, this is temporary to get a feeling how often a new snapshot is created
}

func NewAccumulator(db *sqlx.DB) *Accumulator {
	return &Accumulator{
		db:            db,
		roomsTable:    NewRoomsTable(db),
		eventsTable:   NewEventTable(db),
		snapshotTable: NewSnapshotsTable(db),
		spacesTable:   NewSpacesTable(db),
		entityName:    "server",
	}
}

func (a *Accumulator) strippedEventsForSnapshot(txn *sqlx.Tx, snapID int64) (StrippedEvents, error) {
	snapshot, err := a.snapshotTable.Select(txn, snapID)
	if err != nil {
		return nil, err
	}
	// pull stripped events as this may be huge (think Matrix HQ)
	return a.eventsTable.SelectStrippedEventsByNIDs(txn, true, append(snapshot.MembershipEvents, snapshot.OtherEvents...))
}

// calculateNewSnapshot works out the new snapshot by combining an old snapshot and a new state event. Events get replaced
// if the tuple of event type/state_key match. A new slice is returned (the inputs are not modified) along with the NID
// that got replaced.
func (a *Accumulator) calculateNewSnapshot(old StrippedEvents, new Event) (StrippedEvents, int64, error) {
	// TODO: implement dendrite's binary tree diff algorithm?
	tupleKey := func(e Event) string {
		// 0x1f = unit separator
		return e.Type + "\x1f" + e.StateKey
	}
	newTuple := tupleKey(new)
	added := false
	var replacedNID int64
	seenTuples := make(map[string]struct{}, len(old))
	result := make(StrippedEvents, 0, len(old)+1)
	for _, e := range old {
		existingTuple := tupleKey(e)
		if _, seen := seenTuples[existingTuple]; seen {
			return nil, 0, fmt.Errorf("seen this tuple before: %v", existingTuple)
		}
		seenTuples[existingTuple] = struct{}{}
		if e.NID == new.NID && existingTuple != newTuple {
			// ruh roh. This should be impossible, but it can happen if the v2 response sends the same
			// event in both state and timeline. We need to alert the operator and whine badly as it means
			// we have lost an event by now.
			logger.Warn().Str("new_event_id", new.ID).Str("old_event_id", e.ID).Str("room_id", new.RoomID).Str("type", new.Type).Str("state_key", new.StateKey).Msg(
				"Detected different event IDs with the same NID when rolling forward state. This has resulted in data loss in this room (1 event). " +
					"This can happen when the v2 /sync response sends the same event in both state and timeline sections. " +
					"The event in this log line has been dropped!",
			)
			return nil, 0, fmt.Errorf("new event has same nid value '%d' as event in snapshot but has a different tuple: %v != %v", new.NID, existingTuple, newTuple)
		}
		if existingTuple == newTuple {
			// use the new event
			result = append(result, new)
			added = true
			replacedNID = e.NID
			continue
		}
		// use the old event
		result = append(result, e)
	}
	if !added {
		result = append(result, new)
	}
	return result, replacedNID, nil
}

// roomInfoDelta calculates what the RoomInfo should be given a list of new events.
func (a *Accumulator) roomInfoDelta(roomID string, events []Event) RoomInfo {
	isEncrypted := false
	var upgradedRoomID *string
	var roomType *string
	var pred *string
	for _, ev := range events {
		if ev.Type == "m.room.encryption" && ev.StateKey == "" {
			isEncrypted = true
		}
		if ev.Type == "m.room.tombstone" && ev.StateKey == "" {
			contentType := gjson.GetBytes(ev.JSON, "content.replacement_room")
			if contentType.Exists() && contentType.Type == gjson.String {
				upgradedRoomID = &contentType.Str
			}
		}
		if ev.Type == "m.room.create" && ev.StateKey == "" {
			contentType := gjson.GetBytes(ev.JSON, "content.type")
			if contentType.Exists() && contentType.Type == gjson.String {
				roomType = &contentType.Str
			}
			predecessorRoomID := gjson.GetBytes(ev.JSON, "content.predecessor.room_id").Str
			if predecessorRoomID != "" {
				pred = &predecessorRoomID
			}

		}
	}
	return RoomInfo{
		ID:                roomID,
		IsEncrypted:       isEncrypted,
		UpgradedRoomID:    upgradedRoomID,
		Type:              roomType,
		PredecessorRoomID: pred,
	}
}

type InitialiseResult struct {
	// AddedEvents is true iff this call to Initialise added new state events to the DB.
	AddedEvents bool
	// SnapshotID is the ID of the snapshot which incorporates all added events.
	// It has no meaning if AddedEvents is False.
	SnapshotID int64
	// PrependTimelineEvents is empty if the room was not initialised prior to this call.
	// Otherwise, it is an order-preserving subset of the `state` argument to Initialise
	// containing all events that were not persisted prior to the Initialise call. These
	// should be prepended to the room timeline by the caller.
	PrependTimelineEvents []json.RawMessage
}

// Initialise starts a new sync accumulator for the given room using the given state as a baseline.
//
// This will only take effect if this is the first time the v3 server has seen this room, and it wasn't
// possible to get all events up to the create event (e.g Matrix HQ).
// This function:
// - Stores these events
// - Sets up the current snapshot based on the state list given.
//
// If the v3 server has seen this room before, this function
//   - queries the DB to determine which state events are known to th server,
//   - returns (via InitialiseResult.PrependTimelineEvents) a slice of unknown state events,
//
// and otherwise does nothing.
func (a *Accumulator) Initialise(roomID string, state []json.RawMessage) (InitialiseResult, error) {
	var res InitialiseResult
	if len(state) == 0 {
		return res, nil
	}
	err := sqlutil.WithTransaction(a.db, func(txn *sqlx.Tx) error {
		// Attempt to short-circuit. This has to be done inside a transaction to make sure
		// we don't race with multiple calls to Initialise with the same room ID.
		snapshotID, err := a.roomsTable.CurrentAfterSnapshotID(txn, roomID)
		if err != nil {
			return fmt.Errorf("error fetching snapshot id for room %s: %s", roomID, err)
		}
		if snapshotID > 0 {
			// Poller A has received a gappy sync v2 response with a state block, and
			// we have seen this room before. If we knew for certain that there is some
			// other active poller B in this room then we could safely skip this logic.

			// Log at debug for now. If we find an unknown event, we'll return it so
			// that the poller can log a warning.
			logger.Debug().Str("room_id", roomID).Int64("snapshot_id", snapshotID).Msg("Accumulator.Initialise called with incremental state but current snapshot already exists.")
			eventIDs := make([]string, len(state))
			eventIDToRawEvent := make(map[string]json.RawMessage, len(state))
			for i := range state {
				eventID := gjson.ParseBytes(state[i]).Get("event_id")
				if !eventID.Exists() || eventID.Type != gjson.String {
					return fmt.Errorf("Event %d lacks an event ID", i)
				}
				eventIDToRawEvent[eventID.Str] = state[i]
				eventIDs[i] = eventID.Str
			}
			unknownEventIDs, err := a.eventsTable.SelectUnknownEventIDs(txn, eventIDs)
			if err != nil {
				return fmt.Errorf("error determing which event IDs are unknown: %s", err)
			}
			for unknownEventID := range unknownEventIDs {
				res.PrependTimelineEvents = append(res.PrependTimelineEvents, eventIDToRawEvent[unknownEventID])
			}
			return nil
		}

		// We don't have a snapshot for this room. Parse the events first.
		events := make([]Event, len(state))
		for i := range events {
			events[i] = Event{
				JSON:    state[i],
				RoomID:  roomID,
				IsState: true,
			}
		}
		events = filterAndEnsureFieldsSet(events)
		if len(events) == 0 {
			return fmt.Errorf("failed to insert events, all events were filtered out: %w", err)
		}

		// Before proceeding further, ensure that we have "proper" state and not just a
		// single stray event by looking for the create event.
		hasCreate := false
		for _, e := range events {
			if e.Type == "m.room.create" && e.StateKey == "" {
				hasCreate = true
				break
			}
		}
		if !hasCreate {
			const errMsg = "cannot create first snapshot without a create event"
			sentry.WithScope(func(scope *sentry.Scope) {
				scope.SetContext(internal.SentryCtxKey, map[string]interface{}{
					"room_id":   roomID,
					"len_state": len(events),
				})
				sentry.CaptureMessage(errMsg)
			})
			logger.Warn().
				Str("room_id", roomID).
				Int("len_state", len(events)).
				Msg(errMsg)
			return fmt.Errorf(errMsg)
		}

		// Insert the events.
		eventIDToNID, err := a.eventsTable.Insert(txn, events, false)
		if err != nil {
			return fmt.Errorf("failed to insert events: %w", err)
		}
		if len(eventIDToNID) == 0 {
			// we don't have a current snapshot for this room but yet no events are new,
			// no idea how this should be handled.
			const errMsg = "Accumulator.Initialise: room has no current snapshot but also no new inserted events, doing nothing. This is probably a bug."
			logger.Error().Str("room_id", roomID).Msg(errMsg)
			sentry.CaptureException(fmt.Errorf(errMsg))
			return nil
		}

		// pull out the event NIDs we just inserted
		membershipEventIDs := make(map[string]struct{}, len(events))
		for _, event := range events {
			if event.Type == "m.room.member" {
				membershipEventIDs[event.ID] = struct{}{}
			}
		}
		memberNIDs := make([]int64, 0, len(eventIDToNID))
		otherNIDs := make([]int64, 0, len(eventIDToNID))
		for evID, nid := range eventIDToNID {
			if _, exists := membershipEventIDs[evID]; exists {
				memberNIDs = append(memberNIDs, int64(nid))
			} else {
				otherNIDs = append(otherNIDs, int64(nid))
			}
		}

		// Make a current snapshot
		snapshot := &SnapshotRow{
			RoomID:           roomID,
			MembershipEvents: pq.Int64Array(memberNIDs),
			OtherEvents:      pq.Int64Array(otherNIDs),
		}
		err = a.snapshotTable.Insert(txn, snapshot)
		if err != nil {
			return fmt.Errorf("failed to insert snapshot: %w", err)
		}
		if a.snapshotMemberCountVec != nil {
			logger.Trace().Str("room_id", roomID).Int("members", len(memberNIDs)).Msg("Inserted new snapshot")
			a.snapshotMemberCountVec.WithLabelValues(roomID).Observe(float64(len(memberNIDs)))
		}
		res.AddedEvents = true
		latestNID := int64(0)
		for _, nid := range otherNIDs {
			if nid > latestNID {
				latestNID = nid
			}
		}
		for _, nid := range memberNIDs {
			if nid > latestNID {
				latestNID = nid
			}
		}

		if err = a.spacesTable.HandleSpaceUpdates(txn, events); err != nil {
			return fmt.Errorf("HandleSpaceUpdates: %s", err)
		}

		// check for metadata events
		info := a.roomInfoDelta(roomID, events)

		// these events do not have a state snapshot ID associated with them as we don't know what
		// order the state events came down in, it's only a snapshot. This means only timeline events
		// will have an associated state snapshot ID on the event.

		// Set the snapshot ID as the current state
		res.SnapshotID = snapshot.SnapshotID
		return a.roomsTable.Upsert(txn, info, snapshot.SnapshotID, latestNID)
	})
	return res, err
}

type AccumulateResult struct {
	// NumNew is the number of events in timeline NIDs that were not previously known
	// to the proyx.
	NumNew int
	// TimelineNIDs is the list of event nids seen in a sync v2 timeline. Some of these
	// may already be known to the proxy.
	TimelineNIDs []int64
	// RequiresReload is set to true when we have accumulated a non-incremental state
	// change (typically a redaction) that requires consumers to reload the room state
	// from the latest snapshot.
	RequiresReload bool
}

// Accumulate internal state from a user's sync response. The timeline order MUST be in the order
// received from the server. Returns the number of new events in the timeline, the new timeline event NIDs
// or an error.
//
// This function does several things:
//   - It ensures all events are persisted in the database. This is shared amongst users.
//   - If all events have been stored before, then it short circuits and returns.
//     This is because we must have already processed this part of the timeline in order for the event
//     to exist in the database, and the sync stream is already linearised for us.
//   - Else it creates a new room state snapshot if the timeline contains state events (as this now represents the current state)
//   - It adds entries to the membership log for membership events.
func (a *Accumulator) Accumulate(txn *sqlx.Tx, userID, roomID string, timeline internal.TimelineResponse) (AccumulateResult, error) {
	// The first stage of accumulating events is mostly around validation around what the upstream HS sends us. For accumulation to work correctly
	// we expect:
	// - there to be no duplicate events
	// - if there are new events, they are always new.
	// Both of these assumptions can be false for different reasons
	dedupedEvents := parseAndDeduplicateTimelineEvents(roomID, timeline)
	filteredEvents, err := a.filterToNewTimelineEvents(txn, dedupedEvents)
	if err != nil {
		err = fmt.Errorf("filterTimelineEvents: %w", err)
		return AccumulateResult{}, err
	}
	if len(filteredEvents) == 0 {
		return AccumulateResult{}, nil // nothing to do
	}

	// If this timeline was limited and we don't recognise its first event E3, mark it
	// as not knowing its previous timeline event.
	//
	// We _may_ actually know the previous event. E.g. if the previous poll ends in E1
	// and we see a limited sync starting with E3, the "gap" may have already been
	// filled in by other pollers.
	//
	//  A:  E1 persisted, E2 omitted and unknown, E3 unknown       (missing previous)
	//  B:  E1 persisted, E2 omitted and   known, E3 unknown   (not missing previous)
	//
	// However we can't distinguish between cases (A) and (B) and must assume the worst.
	if timeline.Limited {
		firstTimelineEventUnknown := filteredEvents[0].ID == dedupedEvents[0].ID
		dedupedEvents[0].MissingPrevious = firstTimelineEventUnknown
	}

	// Given a timeline of [E1, E2, S3, E4, S5, S6, E7] (E=message event, S=state event)
	// And a prior state snapshot of SNAP0 then the BEFORE snapshot IDs are grouped as:
	// E1,E2,S3 => SNAP0
	// E4, S5 => (SNAP0 + S3)
	// S6 => (SNAP0 + S3 + S5)
	// E7 => (SNAP0 + S3 + S5 + S6)
	// We can track this by loading the current snapshot ID (after snapshot) then rolling forward
	// the timeline until we hit a state event, at which point we make a new snapshot but critically
	// do NOT assign the new state event in the snapshot so as to represent the state before the event.
	snapID, err := a.roomsTable.CurrentAfterSnapshotID(txn, roomID)
	if err != nil {
		return AccumulateResult{}, err
	}

	// The only situation where no prior snapshot should exist is if this timeline is
	// the very start of the room. If we get here in some other way, we should ignore
	// this timeline completely.
	//
	// For example: we can sometimes get stray events from the homeserver down the
	// timeline, often leaves after an invite rejection. Ignoring these ensures we
	// don't create a false (e.g. lacking m.room.create) record of the room state.
	if snapID == 0 {
		if len(filteredEvents) > 0 && filteredEvents[0].Type == "m.room.create" && filteredEvents[0].StateKey == "" {
			// All okay, continue on.
		} else {
			// Bail out and complain loudly.
			const msg = "Accumulator: skipping processing of timeline, as no snapshot exists"
			logger.Warn().
				Str("event_id", filteredEvents[0].ID).
				Str("event_type", filteredEvents[0].Type).
				Str("event_state_key", filteredEvents[0].StateKey).
				Str("room_id", roomID).
				Str("user_id", userID).
				Int("len_timeline", len(filteredEvents)).
				Msg(msg)
			sentry.WithScope(func(scope *sentry.Scope) {
				scope.SetUser(sentry.User{ID: userID})
				scope.SetContext(internal.SentryCtxKey, map[string]interface{}{
					"event_id":        filteredEvents[0].ID,
					"event_type":      filteredEvents[0].Type,
					"event_state_key": filteredEvents[0].StateKey,
					"room_id":         roomID,
					"len_timeline":    len(filteredEvents),
				})
				sentry.CaptureMessage(msg)
			})
			return AccumulateResult{}, nil
		}
	}

	eventIDToNID, err := a.eventsTable.Insert(txn, filteredEvents, false)
	if err != nil {
		return AccumulateResult{}, err
	}
	if len(eventIDToNID) == 0 {
		// nothing to do, we already know about these events
		return AccumulateResult{}, nil
	}

	result := AccumulateResult{
		NumNew: len(eventIDToNID),
	}

	var latestNID int64
	newEvents := make([]Event, 0, len(eventIDToNID))
	redactTheseEventIDs := make(map[string]*Event)
	for i, ev := range filteredEvents {
		nid, ok := eventIDToNID[ev.ID]
		if ok {
			ev.NID = int64(nid)
			parsedEv := gjson.ParseBytes(ev.JSON)
			if parsedEv.Get("state_key").Exists() {
				// XXX: reusing this to mean "it's a state event" as well as "it's part of the state v2 response"
				// its important that we don't insert 'ev' at this point as this should be False in the DB.
				ev.IsState = true
			}
			// assign the highest nid value to the latest nid.
			// we'll return this to the caller so they can stay in-sync
			if ev.NID > latestNID {
				latestNID = ev.NID
			}
			// if this is a redaction, try to redact the referenced event (best effort)
			if !ev.IsState && ev.Type == "m.room.redaction" {
				// look for top-level redacts then content.redacts (room version 11+)
				redactsEventID := parsedEv.Get("redacts").Str
				if redactsEventID == "" {
					redactsEventID = parsedEv.Get("content.redacts").Str
				}
				if redactsEventID != "" {
					redactTheseEventIDs[redactsEventID] = &filteredEvents[i]
				}
			}
			newEvents = append(newEvents, ev)
			result.TimelineNIDs = append(result.TimelineNIDs, ev.NID)
		}
	}

	// if we are going to redact things, we need the room version to know the redaction algorithm
	// so pull it out once now.
	var roomVersion string
	if len(redactTheseEventIDs) > 0 {
		createEventJSON, err := a.eventsTable.SelectCreateEvent(txn, roomID)
		if err != nil {
			return AccumulateResult{}, fmt.Errorf("SelectCreateEvent: %w", err)
		}
		roomVersion = gjson.GetBytes(createEventJSON, "content.room_version").Str
		if roomVersion == "" {
			// Defaults to "1" if the key does not exist.
			roomVersion = "1"
			logger.Warn().Str("room", roomID).Err(err).Msg(
				"Redact: no content.room_version in create event, defaulting to v1",
			)
		}
		if err = a.eventsTable.Redact(txn, roomVersion, redactTheseEventIDs); err != nil {
			return AccumulateResult{}, err
		}
	}

	for _, ev := range newEvents {
		var replacesNID int64
		// the snapshot ID we assign to this event is unaffected by whether /this/ event is state or not,
		// as this is the before snapshot ID.
		beforeSnapID := snapID

		if ev.IsState {
			// make a new snapshot and update the snapshot ID
			var oldStripped StrippedEvents
			if snapID != 0 {
				oldStripped, err = a.strippedEventsForSnapshot(txn, snapID)
				if err != nil {
					return AccumulateResult{}, fmt.Errorf("failed to load stripped state events for snapshot %d: %s", snapID, err)
				}
			}
			newStripped, replacedNID, err := a.calculateNewSnapshot(oldStripped, ev)
			if err != nil {
				return AccumulateResult{}, fmt.Errorf("failed to calculateNewSnapshot: %s", err)
			}
			replacesNID = replacedNID
			memNIDs, otherNIDs := newStripped.NIDs()
			newSnapshot := &SnapshotRow{
				RoomID:           roomID,
				MembershipEvents: memNIDs,
				OtherEvents:      otherNIDs,
			}
			if err = a.snapshotTable.Insert(txn, newSnapshot); err != nil {
				return AccumulateResult{}, fmt.Errorf("failed to insert new snapshot: %w", err)
			}
			if a.snapshotMemberCountVec != nil {
				logger.Trace().Str("room_id", roomID).Int("members", len(memNIDs)).Msg("Inserted new snapshot")
				a.snapshotMemberCountVec.WithLabelValues(roomID).Observe(float64(len(memNIDs)))
			}
			snapID = newSnapshot.SnapshotID
		}
		if err := a.eventsTable.UpdateBeforeSnapshotID(txn, ev.NID, beforeSnapID, replacesNID); err != nil {
			return AccumulateResult{}, err
		}
	}

	if len(redactTheseEventIDs) > 0 {
		// We need to emit a cache invalidation if we have redacted some state in the
		// current snapshot ID. Note that we run this _after_ persisting any new snapshots.
		redactedEventIDs := make([]string, 0, len(redactTheseEventIDs))
		for eventID := range redactTheseEventIDs {
			redactedEventIDs = append(redactedEventIDs, eventID)
		}
		var currentStateRedactions int
		err = txn.Get(&currentStateRedactions, `
			SELECT COUNT(*)
			FROM syncv3_events
			    JOIN syncv3_snapshots ON event_nid = ANY (ARRAY_CAT(events, membership_events))
			WHERE snapshot_id = $1 AND event_id = ANY($2)
		`, snapID, pq.StringArray(redactedEventIDs))
		if err != nil {
			return AccumulateResult{}, err
		}
		result.RequiresReload = currentStateRedactions > 0
	}

	if err = a.spacesTable.HandleSpaceUpdates(txn, newEvents); err != nil {
		return AccumulateResult{}, fmt.Errorf("HandleSpaceUpdates: %s", err)
	}

	// the last fetched snapshot ID is the current one
	info := a.roomInfoDelta(roomID, newEvents)
	if err = a.roomsTable.Upsert(txn, info, snapID, latestNID); err != nil {
		return AccumulateResult{}, fmt.Errorf("failed to UpdateCurrentSnapshotID to %d: %w", snapID, err)
	}
	return result, nil
}

// - parses it and returns Event structs.
// - removes duplicate events: this is just a bug which has been seen on Synapse on matrix.org
func parseAndDeduplicateTimelineEvents(roomID string, timeline internal.TimelineResponse) []Event {
	dedupedEvents := make([]Event, 0, len(timeline.Events))
	seenEvents := make(map[string]struct{})
	for i, rawEvent := range timeline.Events {
		e := Event{
			JSON:   rawEvent,
			RoomID: roomID,
		}
		if err := e.ensureFieldsSetOnEvent(); err != nil {
			logger.Warn().Str("event_id", e.ID).Str("room_id", roomID).Err(err).Msg(
				"Accumulator.filterToNewTimelineEvents: failed to parse event, ignoring",
			)
			continue
		}
		if _, ok := seenEvents[e.ID]; ok {
			logger.Warn().Str("event_id", e.ID).Str("room_id", roomID).Msg(
				"Accumulator.filterToNewTimelineEvents: seen the same event ID twice, ignoring",
			)
			continue
		}
		if i == 0 && timeline.PrevBatch != "" {
			// tag the first timeline event with the prev batch token
			e.PrevBatch = sql.NullString{
				String: timeline.PrevBatch,
				Valid:  true,
			}
		}
		dedupedEvents = append(dedupedEvents, e)
		seenEvents[e.ID] = struct{}{}
	}
	return dedupedEvents
}

// filterToNewTimelineEvents takes a raw timeline array from sync v2 and applies sanity to it:
// - removes old events: this is an edge case when joining rooms over federation, see https://github.com/matrix-org/sliding-sync/issues/192
// - check which events are unknown. If all events are known, filter them all out.
func (a *Accumulator) filterToNewTimelineEvents(txn *sqlx.Tx, dedupedEvents []Event) ([]Event, error) {
	// if we only have a single timeline event we cannot determine if it is old or not, as we rely on already seen events
	// being after (higher index) than it.
	if len(dedupedEvents) <= 1 {
		return dedupedEvents, nil
	}

	// Figure out which of these events are unseen and hence brand new live events.
	// In some cases, we may have unseen OLD events - see https://github.com/matrix-org/sliding-sync/issues/192
	// in which case we need to drop those events.
	dedupedEventIDs := make([]string, 0, len(dedupedEvents))
	for _, ev := range dedupedEvents {
		dedupedEventIDs = append(dedupedEventIDs, ev.ID)
	}
	unknownEventIDs, err := a.eventsTable.SelectUnknownEventIDs(txn, dedupedEventIDs)
	if err != nil {
		return nil, fmt.Errorf("filterToNewTimelineEvents: failed to SelectUnknownEventIDs: %w", err)
	}

	if len(unknownEventIDs) == 0 {
		// every event has been seen already, no work to do
		return nil, nil
	}

	// In the happy case, we expect to see timeline arrays like this: (SEEN=S, UNSEEN=U)
	// [S,S,U,U] -> want last 2
	// [U,U,U] -> want all
	// In the backfill edge case, we might see:
	// [U,S,S,S] -> want none
	// [U,S,S,U] -> want last 1
	// We should never see scenarios like:
	// [U,S,S,U,S,S] <- we should only see 1 contiguous block of seen events.
	// If we do, we'll just ignore all unseen events less than the highest seen event.

	// The algorithm starts at the end and just looks for the first S event, returning the subslice after that S event (which may be [])
	seenIndex := -1
	for i := len(dedupedEvents) - 1; i >= 0; i-- {
		_, unseen := unknownEventIDs[dedupedEvents[i].ID]
		if !unseen {
			seenIndex = i
			break
		}
	}
	// seenIndex can be -1 if all are unseen, or len-1 if all are seen, either way if we +1 this slices correctly:
	// no seen events  s[A,B,C] =>  s[-1+1:] => [A,B,C]
	// C is seen event s[A,B,C] => s[2+1:] => []
	// B is seen event s[A,B,C] => s[1+1:] => [C]
	// A is seen event s[A,B,C] => s[0+1:] => [B,C]
	return dedupedEvents[seenIndex+1:], nil
}
