### Room List Stream

This is the first stream a client will access on a freshly logged-in client.

The purpose of this API is to:
 - provide the client with a list of room IDs sorted based on some useful sort criteria. Critically,
   the data used to sort these rooms  _is told to the client_ so they can continue to sort rooms as live streaming data comes in.
 - Track a list of room IDs the client is interested in, for feeding into other APIs automagically. This saves bandwidth as the full
   list of room IDs don't need to be constantly sent back and forth.
 - provide **exactly** the information about a room ID to populate room summary without having to track the entire room state.

This stream is paginatable. The first "initial" sync always returns a paginated response to seed the client
with data initially. This is **pagination mode**.

```
POST /sync?since=
{
    room_list: {
        sort: ["by_name", "by_recency"]
        room_limit: 5,
        state_events: [
          ["m.room.topic", ""],
          ["m.room.avatar", ""]
        ],
        space_limit: 5,
        spaces: ["!foo:bar", "!baz:quuz"],
        lazy_load_members: true,
       
        track_spaces: ["!foo:bar"],
        untrack_spaces: ["!foo:bar"],
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
- `room_limit`: (default: 20) The number of rooms to return per space. The maximum number of rooms returned per response is `room_limit * space_limit`.
- `state_events`: (default: `[]`) Array of 2-element arrays. The subarray `[0]` is the event type, `[1]` is the state key.
   The state events to return in the response. This format compresses better in low bandwidth mode.
- `space_limit`: (default: 5) The number of spaces to return per page. Rooms without a space get bucketed into an "orphan" space. Subspaces are not enumerated.
- `spaces`:  (default: `[]`) Limit the room list enumeration to the following spaces. The client must be joined to these spaces.
- `lazy_load_members`: (default: `true`) If true, returns the `m.room.member` events for senders in the timeline.
- `track_spaces`: (default: `[]`) "Explicitly" add these space rooms to the tracked list. The set of tracked spaces is remembered across requests.
- `untrack_spaces`: (default: `[]`) Remove these space rooms from the tracked list. The set of tracked spaces is remembered across requests.
- `track_notifications`: (default: `true`)  If true, events which would [cause a notification to appear](https://spec.matrix.org/unstable/client-server-api/#receiving-notifications) will cause the room to appear in this list. For E2E rooms, all events will be notified as it's impossible to know if the encrypted event should cause a notification.

Returns the response:
```
{
    room_list: {
        spaces: [
          {
            room_id: "!foo:bar",
            name: "My space name",
            state_events: [
             { event JSON }
            ],
            rooms: [
                {
                    room_id: "!aaaa:bar",
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
            "next_page": "get_more_rooms_token"
          },
          {
            rooms: [
                {
                    room_id: "!aaaa:bar",
                    name: "Room not in any space",
                    state_events: [
                      { event JSON }
                    ],
                    prev_batch: "backpagination_token",
                    timeline: [
                      { last event JSON }
                    ]
                },
                { ... }
            ],
            "next_page": "get_more_rooms_token"
          }
        ],
        notifications: [
          {
            room_id: "!bbbb:bar",
            name: "Room that might be in a space but is here because you got pinged",
            state_events: [
              { event JSON }
            ],
            prev_batch: "backpagination_token",
            timeline: [
              { notification event JSON }
            ]
          }
        ]
        "next_page": "get_more_spaces_token"
    },
    next_batch: "s1"
}
```
- `spaces`: The ordered list of spaces.
- `spaces[].room_id`: The room ID of this space. If the room ID is missing, this is the "orphaned" space.
- `spaces[].name`: The calculated name of this space. If the name is missing, this is the "orphaned" space.
- `spaces[].state_events`: A list of state events requested via `state_events`.
- `spaces[].rooms`: A sorted list of rooms that belong to this space.
- `spaces[].next_page`: A pagination token to retrieve more rooms in this space.
- `spaces[].rooms[].room_id`: The room ID of this room.
- `spaces[].rooms[].name`: The calculated name of this room.
- `spaces[].rooms[].state_events`: A list of state events requested via `state_events`.
- `spaces[].rooms[].prev_batch`: The backpagination token for `/messages`.
- `spaces[].rooms[].timeline`: An ordered list of events in the timeline. The last event is the most recent.
- `notifications`: An ordered list of `spaces[].rooms` based on notification criteria. Returned only if `track_notifications: true`.
- `next_page`: A pagination token to retrieve more spaces.

Clients don't need to paginate through the entire list of spaces/rooms so they can ignore `next_page` if they wish.
If they want to paginate, they provide the value of `next_page` in the next request along with the `next_batch` value,
to pin the results to a particular snapshot in time:
```
POST /sync?since=s1
{
    room_list: {
        next_page: "get_more_spaces_token|get_more_rooms_token"
    }
}
```
The server will track which rooms have been sent to the client. If a room has already been sent before, only the `room_id` field will be present
in order to save on bandwidth. For example, if you receive 2 notifications in the same room, the 2nd notification will look like:
```
{
    room_list: {
        notifications: [
          {
            room_id: "!bbbb:bar",
            timeline: [
              { 2nd notification event JSON }
            ]
          }
        ]
    }
}
```

If a request comes in without a `next_page` pagination token but with a `?since=` value, this swaps this API into **streaming mode**.

When operating in **streaming mode**, rooms will be sent to the client based on their membership of a space:
 - For an event E that arrives in a room R, get the set of spaces S that R belongs to. The empty set is valid and is the "orphaned" space.
 - If ANY matched space is "explicitly" tracked (via `track_spaces`) then send E to the client.
 - If ANY matched space is "implicitly" tracked (returned to the client in `spaces`) then send E to the client.
 - Else, do not send E to the client. This means the space must be either not returned to the client OR explicitly untracked via `untrack_spaces`
   in order for this to happen.


With this API, you can _mostly_ (favourites need to be part of a space) emulate Element-Web's LHS room list with the following request:
```
POST /sync?since=
{
    room_list: {
        sort: ["by_recency", "by_name"]
        room_limit: 5,
        state_events: [
          ["m.room.avatar", ""]
        ],
        space_limit: 5,
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
  such as the room list API.
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
     [$aaa,$bbb,$ccc, $ddd] [$eee, $fff, $ggg]
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

#### Room lists

- How do you convey invited or left rooms? Particularly for left rooms, server implementations need to be careful
  to update the room list AFTER all the other streams (so you get the leave event) which is the opposite for joins/normal operations.

#### Room data

- Only the member events are paginated, not the entire room state. This is probably okay as the vast
  majority of current state in rooms are actually just member events. Member events can be sorted coherently, but
  arbitrary state events cannot (what do you sort by? Timestamp?).
