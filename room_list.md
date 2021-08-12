### Room List API

This stream is paginatable.

```
POST /sync?since=
{
    room_list: {
        sort: ["by_tag", "by_name", "by_member_count", "by_recency"]
        limit: 5,
        fields: ["tag","name","member_count","timestamp"],

        add_page: true,
        streaming_add: true,
        add_rooms: [!foo:bar],
        del_rooms: [!foo:bar]
    }
}
```
- `sort`: The sort operations to perform on the rooms. The first element is applied first, then tiebreaks
  are done with the 2nd element, then 3rd, and so on. For example: `["by_tag", "by_name", "by_member_count", "by_recency"]`:
  ```
  [FAV] BBB Some Room Name (100 members) - last msg: 1s ago
  [FAV] BBB Some Room Name (100 members) - last msg: 5m ago
  [FAV] BBB Some Room Name (98 members)
  [FAV] AAA Some Room Name
  Some Room Name
  ```
- `limit`: The number of rooms to return per page.
- `fields`: The fields to return in the response:
  ```
  name: The room name (from m.room.name or server-side calculated from members)
  timestamp: origin_server_ts of last event in timeline
  member_count: number of joined users
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
                member_count: 53,
                timestamp: 16823744325,
                tag: { m.favourite: { order: 0.1111 }}
            },
            { ... }
        ],
        p: {
            next: "next_page_token"
        }
    },
    next_batch: "s1"
}
```
Clients don't need to paginate through the entire list of rooms. When operating in streaming mode,
rooms will be sent to the client based on their sort preferences. E.g if `sort: ["by_recency"]` then
every event in ANY room (even ones not returned to the client before) will be sent to the client. The fields
present in the response depends on the `fields` in the request and whether the room ID in question has been
added to the tracked list. Any room that wasn't previously tracked but now is will return all the `fields` in
the request. That means `streaming_add: true` is required for all fields to be present. Already tracked rooms will
only return the respective fields that have changed e.g `timestamp` on new events.

The tracked room IDs can be fed into a few different APIs.

### Room Summary API

This stream is NOT paginatable. This API is evaluated after the room list API if both streams are requested in a single `/sync` request.

```
POST /sync?since=
{
    room_summary: {
        room_id: "room_list",
        last_event: true,
        lazy_room_member: true,
        heroes: true,
        state_events: [
            ["m.room.name", ""],
            ["m.room.topic", ""],
        ]
    }
}
```
- `room_id`: The room ID to get a summary for, or the constant `"room_list"` to pull from tracked room IDs.
- `last_event`: If true, returns the last timeline event for the room.
- `state_events`: Array of 2-element arrays. The subarray `[0]` is the event type, `[1]` is the state key.
   The state events to return in the response.
- `lazy_room_member`: True and the `m.room.member` event for `last_event` will be sent to the client based on an LRU cache.
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
            last_event: { event JSON },
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
When streaming, any change in the events will be sent as deltas to the client. This means all events will be
sent to the client if `last_event: true`, otherwise only changes in the state events will be communicated via
this stream.


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
        last_event: true,
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
   * `by_member_count`: If the joined member count changed between `SP` and `since`, load the room ID.
   * `by_recency`: If there are any events in this room between `SP` and `since`, load the room ID.
- Remember the room IDs loaded as `Radd` and the reasons why they were added (name, tag, etc).
- Load all tracked room IDs `T[room_id]` for this Session.
- Remember all room IDs which exist in `Radd` but not `T[room_id]` as `Rnew`.
- If the filter has `streaming_add` set:
   * Remove rooms from `T[room_id]` if they exist in `del_rooms`.
   * Add rooms to `T[room_id]` present in `add_rooms` or `Radd`, de-duplicating existing entries.
   * Save `T[room_id]`.
- If `fields: []` then return no response.
- Else return `len(Radd)` objects. Return only the modified field if the room ID is not present in `Rnew` e.g
  tag, name, timestamp. Return all `fields` if the room ID is present in `Rnew`.

