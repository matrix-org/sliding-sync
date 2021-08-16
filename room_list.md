### Room List API

The purpose of this API is to:
 - provide the client with a list of room IDs sorted based on some useful sort criteria. Critically,
   the data used to sort these rooms  _is told to the client_ so they can continue to sort rooms as live streaming data comes in.
 - Track a list of room IDs the client is interested in, for feeding into other APIs automagically.

This stream is paginatable. The first "initial" sync always returns a paginated response to seed the client
with data initially. This is **pagination mode**.

```
POST /sync?since=
{
    room_list: {
        sort: ["by_tag", "by_name", "by_recency"]
        limit: 5,
        fields: ["tag","name","timestamp"],

        add_page: true,
        streaming_add: true,
        add_rooms: [!foo:bar],
        del_rooms: [!foo:bar]
    }
}
```
- `sort`: The sort operations to perform on the rooms. The first element is applied first, then tiebreaks
  are done with the 2nd element, then 3rd, and so on. For example: `["by_tag", "by_name", "by_recency"]`:
  ```
  [FAV] BBB Some Room Name - last msg: 1s ago
  [FAV] BBB Some Room Name - last msg: 5m ago
  [FAV] AAA Some Room Name
  Some Room Name
  ```
    * `by_tag`: Inspect the [room tags](https://spec.matrix.org/unstable/client-server-api/#room-tagging) on this account and sort based on `order`, lower comes first.
    * `by_name`: [Calculate the room name](https://spec.matrix.org/unstable/client-server-api/#calculating-the-display-name-for-a-room) and sort lexiographically A-Z, A comes first.
    * `by_recency`: Sort based on the last event's `origin_server_ts`, higher comes first.
- `limit`: The number of rooms to return per page.
- `fields`: The fields to return in the response:
  ```
  name: The room name (from m.room.name or server-side calculated from members)
  timestamp: origin_server_ts of last event in timeline
  tag: the tag object for the room in account data  e.g { m.favourite: { order: 0.1111 }}
  ```
The final 4 parameters control what the server is tracking. The room list stream remembers a set of room IDs across
requests for use with other APIs. If `add_page: true` then the room IDs returned in the response will be added to
this list. If `streaming_add: true` then any new room that appears as a result of streaming will be added to the set of room IDs.
If `add_rooms` has room IDs in it then they will be added to the list explicitly, and `del_rooms` will delete the rooms from the list.
It is allowed to have an explicit `fields: []` which will not return any data via this stream and instead just update the tracked
room IDs. This is useful if you are getting the room name / latest events via another stream.


Returns the response:
```
{
    room_list: {
        rooms: [
            {
                room_id: "!foo:bar",
                name: "My room name",
                timestamp: 16823744325,
                tag: { m.favourite: { order: 0.1111 }}
            },
            { ... }
        ],
        "next_page": "next_page_token"
    },
    next_batch: "s1"
}
```
Clients don't need to paginate through the entire list of rooms so they can ignore `next_page` if they wish.
If they want to paginate, they provide the value of `next_page` in the next request along with the `next_batch` value,
to pin the results to a particular snapshot in time.

If a request comes in without a pagination token but with a `?since=` value, this swaps this API into **streaming mode**.

When operating in **streaming mode**, rooms will be sent to the client based on their `sort` preferences:
 - `by_recency`: any event in ANY room the client is joined to (even ones not returned to the client before) will cause the room to be sent to the client.
 - `by_tag`: any time ANY room tag is changed in this user's account data, send the room to the client.
 - `by_name`: any time ANY room data involved in [calculating the room name](https://spec.matrix.org/unstable/client-server-api/#calculating-the-display-name-for-a-room) changes, send the room to the client.
 
The fields present in the response depends on the `fields` in the request and whether the room ID in question has been
added to the tracked list. Any room that wasn't previously tracked but now is will return all the `fields` in
the request. That means `streaming_add: true` is required for all fields to be present. Already tracked rooms will
only return the respective fields that have changed e.g `timestamp` on new events, to avoid sending redundant data.

The tracked room IDs can now be fed into a few different APIs.

### Room Summary API

The purpose of this API is to provide **exactly** the information about a room ID to populate room summary views such as:
 - the left-hand-side (LHS) menu on Element-Web
 - the main room list on Element-Android

...without having to track the entire room state. This stream is NOT paginatable. This API is evaluated after the room list API
if both streams are requested in a single `/sync` request.

```
POST /sync?since=
{
    room_summary: {
        room_id: "room_list",
        track_timeline: true,
        lazy_room_member: true,
        heroes: true,
        state_events: [
            ["m.room.name", ""],
            ["m.room.topic", ""],
        ]
    }
}
```
- `room_id`: The room ID to get a summary for, or the magic constant `"room_list"` to pull from tracked room IDs.
- `track_timeline`: If true, returns a live stream of events for this room.
- `state_events`: Array of 2-element arrays. The subarray `[0]` is the event type, `[1]` is the state key.
   The state events to return in the response. This format compresses better in low bandwidth mode.
- `lazy_room_member`: If true then the `m.room.member` events for timeline events will be sent to the client based on an LRU cache.
   This guarantees you'll be told the room member who spoke last, and subsequent times you will not be told (mostly).
- `heroes`: If set, returns the `summary` object from sync v2:
   ```
    "summary": {
        "m.heroes": [
            "@alice:example.com",
            "@bob:example.com"
        ],
        "m.invited_member_count": 0,
        "m.joined_member_count": 2
    }
    ```

Returns the response:
```
{
    room_summary: {
        $room_id: {
            timeline: {
                events: [ event JSON ]
            },
            lazy_room_member: { m.room.member event JSON }
            summary: { "m.heroes": [ ... ]}
            state_events: [
                { event JSON },
                { event JSON },
            ]
        }
    },
    next_batch: "s3"
}
```
When streaming, the room can be invalidated and a delta sent to the client under the following circumstances:
 - `state_events` is non-empty and a state event matched in `state_events` has changed.
 - `heroes` is true and the list of heroes has changed or the number of invited/joined members has changed.
 - `track_timeline` is true and a new event (or more, depending on the poll rate and activity in the room) was sent into the room.

When a delta is sent, only the changed information is sent to the client e.g `timeline.events` field only. This
means all new events for a room will be sent to the client if `track_timeline: true`,
otherwise only changes in the state events / member counts / heroes will be communicated via this stream. 

With these two APIs, you can emulate Element-Web's LHS room list with the following request:
```
POST /sync?since=
{
    room_list: {
        sort: ["by_tag", "by_recency"]
        limit: 20,  // based on viewport
        fields: ["tag"],
        add_page: true,
        streaming_add: true,
    },
    room_summary: {
        room_id: "room_list",
        track_timeline: true,
        lazy_room_member: true,
        heroes: true,
        state_events: [
            ["m.room.name", ""],
            ["m.room.canonical_alias", ""],
            ["m.room.avatar", ""],
        ],
    }
}
```
The room list API provides the number of favourites, etc (you get back a sorted list of room IDs with `tag` fields).
The room summary API provides the info for the room name/avatar and the most recent message along with the lazy-loaded member.

Low-bandwidth clients which show only the room name and the tag (e.g favourites) are also supported, and may just simply use the room list stream:
```
POST /sync?since=
{
    room_list: {
        sort: ["by_name"],
        limit: 20,
        fields: ["name","tag"],
        add_page: true,
        streaming_add: true
    }
}
Returns a paginated list:
{
    room_list: {
        rooms: [
            {
                room_id: "!foo:bar",
                name: "My room name",
                tag: { m.favourite: { order: 0.1111 }}
            },
            { ... }
        ],
        "next_page": "p1"
    }
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
        room_member_sort: "by_pl"
    }
}
```
- `room_id`: The room ID to get a data for, or the magic constant `"room_list"` to pull from tracked room IDs.
- `earliest_timeline_event_ids`: Optional. If set, the room data API will not return any events between the event ID given
  and the `since` value provided in this request (inclusive of both), as the server will assume it has been fetching the timeline by other means
  such as the room summary API.
- `room_member_limit`: The maximum number of room members to fetch for each room. If the limit is exceeded, a pagination token for
  the room member stream will be provided. If 0 or missing, does not paginate room members.
- `room_member_sort`: Enum representing the sort order. See the room member stream for full values.

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
       from room summary     timeline.events
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

- `room_data`: Only the member events are paginated, not the entire room state. This is probably okay as the vast
  majority of current state in rooms are actually just member events. Member events can be sorted coherently, but
  arbitrary state events cannot (what do you sort by?).
- `room_summary`: It's unclear how to unregister a room once you're tracking the summaries for it. In reality, if you start reading the `room_data`
  for a room then you probably want to unregister the associated `room_summary`. This can be done in a few ways, all with
  annoying trade-offs:
    * `room_list.del_rooms: ["!foo:bar"]` : This will remove the room from the room list and hence drop it from the summary.
      However, this would also drop it from the `room_data` API.
    * `room_summary.del_rooms: ["!foo:bar"]` : This removes the room from the room summary API but not any others. This however
      creates 2 sets of room lists which is awkward and clunky.
    * `room_data.remove_from_summary: true` : This removes it lazily, but its unclear what "removal" is: which list? Same drawbacks as above.
- `room_list`: How do you convey invited or left rooms? Particularly for left rooms, server implementations need to be careful
  to update the room list AFTER all the other streams (so you get the leave event) which is the opposite for joins/normal operations.
- 