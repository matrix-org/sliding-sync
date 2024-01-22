package internal

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EventMetadata holds timing information about an event, to be used when sorting room
// lists by recency.
type EventMetadata struct {
	NID       int64
	Timestamp uint64
}

// RoomMetadata holds room-scoped data.
// TODO: This is a lie: we sometimes remove a user U from the list of heroes
// when calculating the sync response for that user U. Grep for `RemoveHero`.
//
// It is primarily used in two places:
//
//   - in the caches.GlobalCache, to hold the latest version of data that is consistent
//     between all users in the room; and
//   - in the sync3.RoomConnMetadata struct, to hold the version of data last seen by
//     a given user sync connection.
//
// Roughly speaking, the sync3.RoomConnMetadata is constantly catching up with changes
// in the caches.GlobalCache.
type RoomMetadata struct {
	RoomID         string
	Heroes         []Hero
	NameEvent      string // the content of m.room.name, NOT the calculated name
	AvatarEvent    string // the content of m.room.avatar, NOT the resolved avatar
	CanonicalAlias string
	JoinCount      int
	InviteCount    int
	// LastMessageTimestamp is the origin_server_ts of the event most recently seen in
	// this room. Because events arrive at the upstream homeserver out-of-order (and
	// because origin_server_ts is an untrusted event field), this timestamp can
	// _decrease_ as new events come in.
	LastMessageTimestamp uint64
	// LatestEventsByType tracks timing information for the latest event in the room,
	// grouped by event type.
	LatestEventsByType map[string]EventMetadata
	Encrypted          bool
	PredecessorRoomID  *string
	UpgradedRoomID     *string
	RoomType           *string
	// if this room is a space, which rooms are m.space.child state events. This is the same for all users hence is global.
	ChildSpaceRooms map[string]struct{}
	// The latest m.typing ephemeral event for this room.
	TypingEvent json.RawMessage
}

func NewRoomMetadata(roomID string) *RoomMetadata {
	return &RoomMetadata{
		RoomID:             roomID,
		LatestEventsByType: make(map[string]EventMetadata),
		ChildSpaceRooms:    make(map[string]struct{}),
	}
}

// DeepCopy returns a version of the current RoomMetadata whose Heroes+LatestEventsByType fields are
// brand-new copies. The return value's Heroes field can be
// safely modified by the caller, but it is NOT safe for the caller to modify any other
// fields.
func (m *RoomMetadata) DeepCopy() *RoomMetadata {
	newMetadata := *m

	// XXX: We're doing this because we end up calling RemoveHero() to omit the
	// currently-sycning user in various places. But this seems smelly. The set of
	// heroes in the room is a global, room-scoped fact: it is a property of the room
	// state and nothing else, and all users see the same set of heroes.
	//
	// I think the data model would be cleaner if we made the hero-reading functions
	// aware of the currently syncing user, in order to ignore them without having to
	// change the underlying data.
	//
	// copy the heroes or else we may modify the same slice which would be bad :(
	newMetadata.Heroes = make([]Hero, len(m.Heroes))
	copy(newMetadata.Heroes, m.Heroes)

	// copy LatestEventsByType else we risk concurrent map r/w when the connection
	// reads this map and updates write to it.
	newMetadata.LatestEventsByType = make(map[string]EventMetadata)
	for k, v := range m.LatestEventsByType {
		newMetadata.LatestEventsByType[k] = v
	}

	newMetadata.ChildSpaceRooms = make(map[string]struct{})
	for k, v := range m.ChildSpaceRooms {
		newMetadata.ChildSpaceRooms[k] = v
	}

	// ⚠️ NB: there are other pointer fields (e.g. PredecessorRoomID *string)
	// and pointer-backed fields which are not deepcopied here, because they do not
	// change.
	return &newMetadata
}

// SameRoomName checks if the fields relevant for room names have changed between the two metadatas.
// Returns true if there are no changes.
func (m *RoomMetadata) SameRoomName(other *RoomMetadata) bool {
	return (m.RoomID == other.RoomID &&
		m.NameEvent == other.NameEvent &&
		m.CanonicalAlias == other.CanonicalAlias &&
		m.JoinCount == other.JoinCount &&
		m.InviteCount == other.InviteCount &&
		sameHeroNames(m.Heroes, other.Heroes))
}

func (m *RoomMetadata) SameJoinCount(other *RoomMetadata) bool {
	return m.JoinCount == other.JoinCount
}

func (m *RoomMetadata) SameInviteCount(other *RoomMetadata) bool {
	return m.InviteCount == other.InviteCount
}

func sameHeroNames(a, b []Hero) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

func (m *RoomMetadata) RemoveHero(userID string) {
	for i, h := range m.Heroes {
		if h.ID == userID {
			m.Heroes = append(m.Heroes[0:i], m.Heroes[i+1:]...)
			return
		}
	}
}

func (m *RoomMetadata) IsSpace() bool {
	return m.RoomType != nil && *m.RoomType == "m.space"
}

type Hero struct {
	ID     string `json:"user_id"`
	Name   string `json:"displayname,omitempty"`
	Avatar string `json:"avatar_url,omitempty"`
}

// CalculateRoomName calculates the room name. Returns the name and if the name was actually calculated
// based on room heroes.
func CalculateRoomName(heroInfo *RoomMetadata, maxNumNamesPerRoom int) (name string, calculated bool) {
	// If the room has an m.room.name state event with a non-empty name field, use the name given by that field.
	if heroInfo.NameEvent != "" {
		return heroInfo.NameEvent, false
	}
	// If the room has an m.room.canonical_alias state event with a valid alias field, use the alias given by that field as the name.
	if heroInfo.CanonicalAlias != "" {
		return heroInfo.CanonicalAlias, false
	}
	// If none of the above conditions are met, a name should be composed based on the members of the room.
	disambiguatedNames := disambiguate(heroInfo.Heroes)
	totalNumOtherUsers := int(heroInfo.JoinCount + heroInfo.InviteCount - 1)
	isAlone := totalNumOtherUsers <= 0

	// If m.joined_member_count + m.invited_member_count is less than or equal to 1 (indicating the member is alone),
	// the client should use the rules BELOW to indicate that the room was empty. For example, "Empty Room (was Alice)",
	// "Empty Room (was Alice and 1234 others)", or "Empty Room" if there are no heroes.
	if len(heroInfo.Heroes) == 0 && isAlone {
		return "Empty Room", false
	}

	// If the number of m.heroes for the room are greater or equal to m.joined_member_count + m.invited_member_count - 1,
	// then use the membership events for the heroes to calculate display names for the users (disambiguating them if required)
	// and concatenating them.
	if len(heroInfo.Heroes) >= totalNumOtherUsers {
		if len(disambiguatedNames) == 1 {
			return disambiguatedNames[0], true
		}
		calculatedRoomName := strings.Join(disambiguatedNames[:len(disambiguatedNames)-1], ", ") + " and " + disambiguatedNames[len(disambiguatedNames)-1]
		if isAlone {
			return fmt.Sprintf("Empty Room (was %s)", calculatedRoomName), true
		}
		return calculatedRoomName, true
	}

	// if we're here then len(heroes) < (joinedCount + invitedCount - 1)
	numEntries := len(disambiguatedNames)
	if numEntries > maxNumNamesPerRoom {
		numEntries = maxNumNamesPerRoom
	}
	calculatedRoomName := fmt.Sprintf(
		"%s and %d others", strings.Join(disambiguatedNames[:numEntries], ", "), totalNumOtherUsers-numEntries,
	)

	// If there are fewer heroes than m.joined_member_count + m.invited_member_count - 1,
	// and m.joined_member_count + m.invited_member_count is greater than 1, the client should use the heroes to calculate
	// display names for the users (disambiguating them if required) and concatenating them alongside a count of the remaining users.
	if (heroInfo.JoinCount + heroInfo.InviteCount) > 1 {
		return calculatedRoomName, true
	}

	// If m.joined_member_count + m.invited_member_count is less than or equal to 1 (indicating the member is alone),
	// the client should use the rules above to indicate that the room was empty. For example, "Empty Room (was Alice)",
	// "Empty Room (was Alice and 1234 others)", or "Empty Room" if there are no heroes.
	return fmt.Sprintf("Empty Room (was %s)", calculatedRoomName), true
}

func disambiguate(heroes []Hero) []string {
	displayNames := make(map[string][]int)
	for i, h := range heroes {
		name := h.Name
		if name == "" {
			name = h.ID
		}
		displayNames[name] = append(displayNames[name], i)
	}
	disambiguatedNames := make([]string, len(heroes))
	for name, indexes := range displayNames {
		if len(indexes) == 1 {
			disambiguatedNames[indexes[0]] = name
			continue
		}
		// disambiguate all these heroes
		for _, i := range indexes {
			h := heroes[i]
			name := h.Name
			if name == "" {
				name = h.ID
			}
			disambiguatedNames[i] = fmt.Sprintf("%s (%s)", name, h.ID)
		}
	}
	return disambiguatedNames
}

const noAvatar = ""

// CalculateAvatar computes the avatar for the room, based on the global room metadata.
// Assumption: metadata.RemoveHero has been called to remove the user who is syncing
// from the list of heroes.
func CalculateAvatar(metadata *RoomMetadata, isDM bool) string {
	if metadata.AvatarEvent != "" {
		return metadata.AvatarEvent
	}
	if len(metadata.Heroes) == 1 && isDM {
		return metadata.Heroes[0].Avatar
	}
	return noAvatar
}
