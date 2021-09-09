### Room List Stream

This is the first stream a client will access on a freshly logged-in client.

The purpose of this API is to:
 - provide the client with a list of room IDs sorted based on some useful sort criteria. Critically,
   the data used to sort these rooms  _is told to the client_ so they can continue to sort rooms as live streaming data comes in.
 - Track the list of room IDs the client is interested in, for feeding into other APIs automagically.
   *This list is called the "**active set**".* This saves bandwidth as the full list of room IDs don't need to be constantly sent back and forth. 
 - provide **exactly** the information about a room ID to populate room summary without having to track the entire room state.

At present, the client needs to know the room IDs of the top-level spaces the client is joined to first if they wish to make use of the Spaces
functionality in this API. This can get these space room IDs via [MSC2946](https://github.com/matrix-org/matrix-doc/pull/2946).

This stream is paginatable. The first "initial" sync always returns a paginated response to seed the client
with data initially. This is **pagination mode**.

```
POST /sync?since=
{
    room_list: {
        sort: ["by_name", "by_recency", "by_space_order"]
        limit: 5,
        state_events: [
          ["m.room.topic", ""],
          ["m.room.avatar", ""],
          ["m.space.parent", "*"]
        ],
        spaces: ["!foo:bar", "!baz:quuz"],
        lazy_load_members: true,
        track_notifications: true
    }
}
```
- `sort`: (default: `by_recency`) The sort operations to perform on the rooms. The first element is applied first, then tiebreaks
  are done with the 2nd element, then 3rd, and so on. For example: `["by_name", "by_recency"]`:
  ```
  Another Room Name
  Random Room Name (last msg: 5m ago)
  Random Room Name (last msg: 15h ago)
  Some Room Name
  ```
    * `by_name`: [Calculate the room name](https://spec.matrix.org/unstable/client-server-api/#calculating-the-display-name-for-a-room) and sort lexiographically A-Z, A comes first. This means servers need to handle heroes and internationalisation.
    * `by_recency`: Sort based on the last event's `origin_server_ts`, higher comes first.
    * `by_space_order`: Sort by the space ordering according to the `order` in `m.space.child` events. If this is set, `spaces` cannot be empty.
    * `by_highlight_count`: Sort based on the number of highlight-able (typically red) unread notifications. Highest first.
    * `by_notification_count`: Sort based on the number of unread notifications (typically grey). Highest first.
- `limit`: (default: 20) The number of rooms to return per request.
- `state_events`: (default: `[]`) Array of 2-element arrays. The subarray `[0]` is the event type, `[1]` is the state key.
   The state events to return in the response. This format compresses better in low bandwidth mode. If the state key is `*` then return all matching
   events with that event type.
- `spaces`:  (default: `[]`) Restrict the rooms returned in this API to the following spaces rather than all the rooms the client is joined to.
  This can be used to reduce the total amount of data sent to the client. The client must be joined to these spaces. Subspaces are not enumerated.
- `lazy_load_members`: (default: `true`) If true, returns the `m.room.member` events for senders in the timeline, along with `m.room.member` events
  for all members which are used to form the room name. This allows clients to render a room avatar based on the members in the room if needed.
- `track_notifications`: (default: `true`)  If true, events which would [cause a notification to appear](https://spec.matrix.org/unstable/client-server-api/#receiving-notifications) will cause the room to appear in this list. For E2E rooms, all events will be notified as it's impossible to know if the encrypted event should cause a notification.

Returns the response:
```
{
    room_list: {
        rooms: [
          {
            room_id: "!foo:bar",
            name: "My room name",
            state_events: [
             { event JSON }, { lazy m.room.member }
            ],
            prev_batch: "backpagination_token",
            timeline: [
              { last event JSON }
            ]
          },
          { ... }
        ],
        next_page: "get_more_rooms_token"
        notifications: [
          {
            room_id: "!bbbb:bar",
            name: "a random room not used in a long time",
            state_events: [
             { event JSON }, { lazy m.room.member }
            ],
            events: [ { ... } ],
            highlight_count: 52,
            notification_count: 102,
          },
          {
            room_id: "!encrypted:bar",
            name: "My Encrypted Room",
            state_events: [
             { event JSON }, { lazy m.room.member }
            ],
            prev_batch: "backpagination_token",
            timeline: [
              { last event JSON }
            ]
          },
        ]
    },
    next_batch: "s1"
}
```
- `rooms`: The ordered list of rooms.
- `next_page`: A pagination token to retrieve more rooms.
- `rooms[].room_id`: The room ID of this room.
- `rooms[].name`: The calculated name of this room.
- `rooms[].highlight_count`: The unread notification count (see `unread_notifications` in /sync v2).
- `rooms[].notification_count`: The unread notification count (see `unread_notifications` in /sync v2).
- `rooms[].state_events`: A list of state events requested via `state_events`.
- `rooms[].prev_batch`: The backpagination token for `/messages`.
- `rooms[].timeline`: An ordered list of events in the timeline. The last event is the most recent.
- `notifications`: An unordered list of `rooms` based on notification criteria. Returned only if `track_notifications: true`.
  Notifiable events do not return a `timeline` as this section only produces the odd notified event and not a coherent stream of events.
  E2EE rooms however DO return a `timeline` as every single event is "notifiable" effectively as the server doesn't know. By sending this
  as a timeline, it allows the room data API to not send events after a given event ID, thus saves bandwidth.
- `notifications[].events`: The notifiable events. Typically this will just contain a single entry but if there is a large gap it's possible
  to receive two or more notifiable events in the same room. Critically, these events do not form part of a coherent timeline, hence why it's
  called `events` and not `timeline`.

Clients don't need to paginate through the entire list of rooms so they can ignore `next_page` if they wish.
If they want to paginate, they provide the value of `next_page` in the next request along with the `next_batch` value,
to pin the results to a particular snapshot in time:
```
POST /sync?since=s1
{
    room_list: {
        next_page: "get_more_rooms_token"
    }
}
```

The server will remember the "active set" of room IDs for this stream according to the following rules:
 - If `spaces` is non-empty then remember the entire set of top-level children rooms for each space. De-duplicate as needed.
   Clients can track subspaces by including the subspace room ID in `spaces`.
 - If `spaces` is empty then remember the room IDs sent to the client in the response, increasing this list as the client paginates.
 - When the client leaves a room in the active set, remove it.

NB: Clients can specify `limit: 0` and `spaces: []` to indicate an empty set as the active set. This is useful when combined
with `track_notifications: true` which means this API will _only_ return a `notifications` section.

NBB: There is no special handling for left rooms. Clients will receive their leave
event in the `timeline` and then no further events. It is up to the client to then remove this room from the sorted list.

If a request comes in without a `next_page` pagination token but with a `?since=` value, this swaps this API into **streaming mode**.

When operating in **streaming mode**, rooms will be sent to the client based on the "active set" and `track_notifications`:
 - For an event E that arrives in a room R, if R is in the active set then send E to the client.
 - Else if `track_notifications` is `true` and E is a notifiable event or E is in an E2EE room based on the `m.room.encryption` state event,
   then send E to the client.
 - Else drop the event. 

With this API, you can _mostly_ (favourites need to be part of a space) emulate Element-Web's LHS room list with the following request:
```
POST /sync?since=
{
    room_list: {
        sort: ["by_highlight_count", "by_notification_count", "by_recency"]
        limit: 10,
        state_events: [
          ["m.room.avatar", ""]
        ],
        lazy_load_members: true,
        track_notifications: true
    },
}
```

Caveats:
 - The room list stream cannot be used to track invitations. That needs a new stream which is fine as they aren't sorted in the same way as joined rooms.
 - Tracking notifications can be heavy if the user is joined to a lot of E2EE rooms. It might be nice to have some way of filtering this list down,
   particularly as we expect the number of E2EE rooms to increase over time.

### Room Data API

The purpose of this API is to provide the entire room state/timeline for a single room (often the room the user is viewing). This stream
is NOT paginatable. This API is evaluated after the room list API if both streams are requested in a single `/sync` request.

```
POST /sync?since=
{
    room_data: {
        room_id: "room_list",
        earliest_timeline_event_ids: ["$aaaa","$bbbb"],
        room_member_limit: 5,
        room_member_sort: "by_pl",
        timeline_limit: 20,
        format: "client|federation",
        types: [ "m.room.message" ],
        not_types: [ "m.room.topic" ]
    }
}
```
- `room_id`: The room ID to get a data for, or the magic constant `"room_list"` to pull from tracked room IDs.
- `earliest_timeline_event_ids`: Optional. If set, the room data API will not return any events between the event ID given
  and the `since` value provided in this request (inclusive of both), as the server will assume it has been fetching the timeline by other means
  such as the room list API. This saves bandwidth by not sending duplicate events if the event IDs are the earliest events the client has
  for each room being tracked.
- `room_member_limit`: The maximum number of room members to fetch for each room. If the limit is exceeded, a pagination token for
  the room member stream will be provided. If 0 or missing, does not paginate room members.
- `room_member_sort`: Enum representing the sort order. See the room member stream for full values.
- `timeline_limit`: The max number of timeline events to return. Mostly this is useful if you are calling the room data stream without having a room list stream running.
- `format`: (default: `client`) The event format to send back. See [Filtering](https://spec.matrix.org/unstable/client-server-api/#filtering)
- `types` and `not_types`: [Event filters](https://spec.matrix.org/unstable/client-server-api/#filtering) to use.

Returns the response:
```
{
    room_data: {
        rooms: {
            $room_id: {
                state: {
                    state_before: "$aaaa",
                    events: [ ... ]
                },
                members: {
                    events: [ ... ],
                    next_batch: "s1",
                    next_page: "p1"
                },
                timeline: {
                    prev_batch: "p1",
                    events: [ ... ]
                }
            }
        }
    }
}
```
- `state.state_before`: Optional. Set to an event ID specified in `earliest_timeline_event_ids`. If set, the `state.events` refer to the state
  of the room _before_ the event ID specified in `state_before`. Clients should set the room state to these events, then roll forward their already stored timeline
  events. Only after that point should the events in `timeline.events` be applied. In this case, the `timeline.prev_batch` refers to the batch of
  events prior to the event in `state_before`, NOT `timeline.events[0].event_id`. To be clear:
  ```
       from room list        timeline.events
     [$aaa,$bbb,$ccc,$ddd] [$eee, $fff, $ggg]
     ^
     |
   state.events

   state.state_before = "$aaa"
   timeline.prev_batch = "token_for_events_before_$aaa"
  ```
  This only applies if `earliest_timeline_event_ids` is not empty AND contains an event in a one of the room IDs given.
- `state.events`: The state events at the start of the timeline, excluding room member events. The timeline may either be `timeline.events` or the earliest event given in `earliest_timeline_event_ids`.
- `members.events`: The `m.room.member` state events at the start of the timeline, same as `state.events`. May be partial, depending on the `room_member_limit`.
  Sorted according to the `room_member_sort` value.

### Room Member API

The purpose of this API is to provide a paginated list of room members for a given room.

```
POST /sync?since=
{
    room_member: {
        room_id: "room_list",
        limit: 5,
        sort: "by_pl"
    }
}
```
- `room_id`: The room to fetch members in.
- `limit`: The max number of members to fetch per page.
- `sort`: How to sort the list of room members. One of:
    * `by_name`: Lexicographical order from A->Z (case-insensitive, unicode case-folding)
    * `by_pl`: Sort highest power level first, then `by_name`.

Returns the response:
```
{
    room_member: {
        limit: 5,
        events: [ m.room.member events ]
        next_page: "p1"
    }
}
```
- `limit`: The negotiated limit, may be lower than the `limit` requested.

### Server implementation guide

Server-side, the pagination operations performed for `room_list` are:
- Multiplex together the `room_list` filter params as per normal v3 semantics.
- Load latest stream position or use `?since=` if provided, call it `SP`. Results are anchored at this position.
- Load all joined rooms for this user at `SP`.
- If `spaces` is non-empty, reduce the set of joined rooms to ones belonging to these spaces. Add all these rooms to the "active set".
- Sort the joined rooms according to `sort`. If it is `by_name` then the server needs to calculate the room name and handle internationalisation.
- Subslice the room IDs based on `limit` (and `next_page` if it exists) to produce a list of room IDs `R`.
- For each room in `R`:
   * Load the state events based on `state_events`, honouring wildcard state keys.
   * Calculate the room name and set it on `name`. If `m.room.member` events were required to do this, include them in the state events if `lazy_load_members: true`.
   * Load the most recent event in `R`. Include it in the `timeline`, and include the `m.room.member` event of the sender in the state events if `lazy_load_members: true`.
   * Set the `prev_batch` value based on the timeline event.
   * Sort the state events lexiographically by event type then state key then add them to the `state_events` response for this room.
   * Add `R` to the "active set" if `spaces` is empty.

Server-side, the streaming operations performed for `room_list` are:
- Load the latest stream position `SP` and the `since` value.
- If the delta between the two positions is too large (heuristic), reset the session.
- Multiplex together the `room_list` filter params as per normal v3 semantics.
- Load the active set, updating it if needed (e.g if `spaces` is non-empty and a new room has been added to the space between the 2 stream positions).
  Events _should_ start flowing from the point the room was added to the space, but ultimately it isn't a hard requirement so long as the client is
  able to view these events.
- Load the set of room IDs `Rsent` who have had a complete response already sent to the client.
- For all events between `since` and `SP`:
   * apply visibility checks to filter out events the user cannot see due to not being joined anymore.
   * if the event is in a room in the "active set", add it to `rooms[].timeline`.
   * else if the event is a notifiable event based on push rules or E2EE presence then add it to `notifications`. If the event
     is part of an E2EE room then add it to `notifications[].timeline`, else add it to `notifications[].events`. If there are multiple
     notifiable events in the same non-E2EE room, append it to `events`. 
   * if the room ID in the event is not in `Rsent` then add it to `Rsent` and include all the necessary `state_events` according to the filter,
     in addition to the `name` and `prev_batch` (see the pagination section).


### Notes, Rationale and Queries


#### Room data

- Only the member events are paginated, not the entire room state. This is probably okay as the vast
  majority of current state in rooms are actually just member events. Member events can be sorted coherently, but
  arbitrary state events cannot (what do you sort by? Timestamp?).
