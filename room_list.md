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
            event: { ... },
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
- `rooms[].state_events`: A list of state events requested via `state_events`.
- `rooms[].prev_batch`: The backpagination token for `/messages`.
- `rooms[].timeline`: An ordered list of events in the timeline. The last event is the most recent.
- `notifications`: An unordered list of `rooms` based on notification criteria. Returned only if `track_notifications: true`.
  Notifiable events do not return a `timeline` as this section only produces the odd notified event and not a coherent stream of events.
  E2EE rooms however DO return a `timeline` as every single event is "notifiable" effectively as the server doesn't know. By sending this
  as a timeline, it allows the room data API to not send events after a given event ID, thus saves bandwidth.
- `notifications[].highlight_count`: The unread notification count (see `unread_notifications` in /sync v2).
- `notifications[].notification_count`: The unread notification count (see `unread_notifications` in /sync v2).
- `notifications[].event`: The notifiable event.

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

NBB: There is no special handling for invited rooms in this API. This requires an additional stream implementation. There is also no
special handling for left rooms, however this does not require an additional stream implementation. Clients will receive their leave
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
        sort: ["by_recency", "by_name"]
        limit: 10,
        state_events: [
          ["m.room.avatar", ""]
        ],
        lazy_load_members: true,
        track_notifications: true
    },
}
```

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

TODO: Update this section

Server-side, the pagination operations performed for `room_list` are:
- Load latest stream position or use `?since=` if provided, call it `SP`.
- There is a `room_list` stream, so load all joined/invited rooms for this user at `SP`.
- Multiplex together the `room_list` filter params.
- Sort according to `sort` and subslice the room IDs based on `limit` (and `p` if it exists) to produce a list of room IDs `R`.
- If the filter has `add_page` set:
   * Load all tracked room IDs `T[room_id]` for this Session.
   * Remove rooms from `T[room_id]` if they exist in `del_rooms`.
   * append `R` and any in `add_rooms` to `T[room_id]`, de-duplicating existing entries.
   * Save `T[room_id]`.
- If `fields: []` then return no response.
- Else Return `len(R)` objects with the appropriate `fields`.

Server-side, the streaming operations performed for `room_list` are:
- Load the latest stream position `SP` and the `since` value.
- If the delta between the two positions is too large (heuristic), reset the session.
- Multiplex together the `room_list` filter params.
- Inspect the sort order as that will tell you what to notify on, based on the following rules:
   * `by_tag`: If the tag account data has changed for a room between `SP` and `since`, load the room ID.
   * `by_name`: If the `m.room.name` or `m.room.canonical_alias` or hereos have changed between `SP` and `since`, load the room ID.
   * `by_recency`: If there are any events in this room between `SP` and `since`, load the room ID.
- In addition, any newly joined rooms between `SP` and `since`, load the room ID.
- Remember the room IDs loaded as `Radd` and the reasons why they were added (name, tag, etc).
- Load all tracked room IDs `T[room_id]` for this Session.
- Remember all room IDs which exist in `Radd` but not `T[room_id]` as `Rnew`.
- If the filter has `streaming_add` set:
   * Remove rooms from `T[room_id]` if they exist in `del_rooms` or the user left the room between `SP` and `since`.
   * Add rooms to `T[room_id]` present in `add_rooms` or `Radd`, de-duplicating existing entries.
   * Save `T[room_id]`.
- If `fields: []` then return no response.
- Else return `len(Radd)` objects. Return only the modified field if the room ID is not present in `Rnew` e.g
  tag, name, timestamp. Return all `fields` if the room ID is present in `Rnew`.


### Notes, Rationale and Queries


#### Room data

- Only the member events are paginated, not the entire room state. This is probably okay as the vast
  majority of current state in rooms are actually just member events. Member events can be sorted coherently, but
  arbitrary state events cannot (what do you sort by? Timestamp?).
