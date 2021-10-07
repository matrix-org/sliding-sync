## Paginated Sync v3

*Please file an issue on this repository if you wish to make comments on this.*

This is a proposal to replace Sync v2 (the current sync mechanism in the r0 spec) with a new paginated sync mechanism.

### Why?

- Sync v2 slows down with more rooms due to lack of room pagination. Some accounts now have 1000s of rooms making them completely impractical to sync on.
- Sync v2 sends far too much data which you cannot opt-out of e.g receipts for ALL rooms.
- Sync v2 supports very very old sync tokens, forcing the server to calculate extremely large and costly deltas.

A new sync mechanism should have the following properties:

- Sync time should be independent of the number of rooms you are in.
- Time from launch to confident usability should be as low as possible.
- Time from login on existing accounts to usability should be as low as possible.
- Bandwidth should be minimised.
- Support lazy-loading of things like read receipts (and avoid sending unnecessary data to the client)
- Support informing the client when state changes from under it, due to state res
- Clients should be able to work correctly without ever syncing in the full set of rooms they’re in.
- Don’t incremental sync rooms you don’t care about.
- Combining uploaded filters with ad-hoc filter parameters (which isn’t possible with sync v2 today)
- Servers should not need to store all past since tokens. If a since token has been discarded we should gracefully degrade to initial sync.

A critical component to all of these properties is to support *paginated rooms*, which sync v2 does not do.

### What Matrix does currently

For every event received by a homeserver, an immutable position is assigned to it. Sync tokens are thus the position in this single linear stream (ignoring vector clocks that Synapse workers do). This has problems. If you sync with an ancient position, you get a bazillion events. This was the failure mode of sync v1 (`/initialSync` and `/events`): using an old position would cause massive amounts of data to be sent to clients via `/events`. Sync v2 remedied this by introducing room state deltas and timeline limits. This helps but it is still very costly on the server to calculate the state delta. Sync v3 is required because the number of rooms people are in now is getting large enough to cause unreasonably long delays. We want to paginate rooms, and cut down on the amount of room state that needs to be sent to the client to get them operational.

### What this could look like

The overarching model here is to imagine `/sync` as a pubsub system, where you are "subscribing" to *ranges* of a sorted room list array. In addition, you can also "subscribe" to explicit room IDs whenever you want e.g. when you are viewing the room or receiving a permalink for a room, and data is de-duplicated between these two subscriptions if the room is both explicitly subscribed to and in the subslice.

`POST /v3/sync`:
```json=
{
  // Identifies the session for the purposes of remembering request
  // parameters. This allows a single device to have multiple sync
  // sessions active and not have them step on each other.
  // "to-device" messages will only be deleted from the server once
  // ALL sessions have received said message. Sessions can be deleted
  // by the server after a period of inactivity. Deleted sessions do
  // not result in to-device messages being purged if they have never
  // been delivered to any session yet: they must be delivered to at
  // least one active session on the device.
  // If this id is missing, it is set to 'default'.
  "session_id": "arbitrary-client-chosen-string",
  
  // first 100 rooms
  "rooms": [ [0,99] ],
  
  // how `rooms` gets sorted. Note "by_name" means servers need to
  // implement the room name calculation algorithm. We may be able to
  // add a "locale" key for sorting rooms which are composed of user
  // names more sensibly according to i18n.
  "sort": [ "by_notification_count", "by_recency", "by_name" ],
  
  "required_state": [
    ["m.room.join_rules", ""],
    ["m.room.history_visibility", ""],
    ["m.space.child", "*"] // wildcard
  ],
  
  // the initial timeline limit to send for a new room, live stream
  // data can exceed this limit
  "timeline_limit": 10,
  
  "room_subscriptions": {
      "!sub1:bar": { // the client may be actively viewing this room
          "required_state": [ ["*","*"] ], // all state events
          "timeline_limit": 50
      },
      // empty object will use the same request params as the list subscription
      "!sub2:bar": {}
  },
  // if the client was already subscribed to this room, this is how you unsub
  // unsubbing twice is a no-op
  "unsubscribe_rooms": [ "!sub3:bar" ]
  
  "filters": {
    // only returns rooms in these spaces (ignores subspaces)
    "spaces": ["!space1:example.com", "!space2:example.com"],
    // options to control which events should be live-streamed e.g not_types, types from sync v2
  }
}
```
Returns:
```json=
{
  "ops": [
    {
      "range": [0,99],
      "op": "SYNC",
      "rooms": [
        {
          "room_id": "!foo:bar",
          "name": "The calculated room name",
          // this is the CURRENT STATE, unlike v2 sync
          "required_state": [
            {"sender":"@alice:example.com","type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"invite"}},
            {"sender":"@alice:example.com","type":"m.room.history_visibility", "state_key":"", "content":{"history_visibility":"joined"}},
            {"sender":"@alice:example.com","type":"m.space.child", "state_key":"!foo:example.com", "content":{"via":["example.com"]}},
            {"sender":"@alice:example.com","type":"m.space.child", "state_key":"!bar:example.com", "content":{"via":["example.com"]}},
            {"sender":"@alice:example.com","type":"m.space.child", "state_key":"!baz:example.com", "content":{"via":["example.com"]}}
          ],
          "timeline": [
            // We can de-dupe events in `required_state` via a top-level event map so only the event IDs are referenced here.
            {"sender":"@alice:example.com","type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"invite"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"A"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"B"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"C"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"D"}},
          ],
          "notification_count": 54, // from sync v2
          "highlight_count": 3      // from sync v2
        },
        {
          "room_id": "!sub1:bar"
          // because this is an explicit room subscription, the
          // room data goes into room_subscriptions and
          // only the bare minimum data is here to provide the sort ordering
        }
        // ... 98 more items
      ],
    }
  ],
  "room_subscriptions": {
        "!sub1:bar": {
          "name": "#canonical-alias:localhost",
          "required_state": [
            {"sender":"@alice:example.com","type":"m.room.create", "state_key":"", "content":{"creator":"@alice:example.com"}},
            {"sender":"@alice:example.com","type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"invite"}},
            {"sender":"@alice:example.com","type":"m.room.history_visibility", "state_key":"", "content":{"history_visibility":"joined"}},
            {"sender":"@alice:example.com","type":"m.room.member", "state_key":"@alice:example.com", "content":{"membership":"join"}}
          ],
          "timeline": [
            {"sender":"@alice:example.com","type":"m.room.create", "state_key":"", "content":{"creator":"@alice:example.com"}},
            {"sender":"@alice:example.com","type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"invite"}},
            {"sender":"@alice:example.com","type":"m.room.history_visibility", "state_key":"", "content":{"history_visibility":"joined"}},
            {"sender":"@alice:example.com","type":"m.room.member", "state_key":"@alice:example.com", "content":{"membership":"join"}}
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"A"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"B"}},
          ],
          // 0 notif count fields are required initially as if they are
          // omitted it may indicate "no update/change" instead of "0".
          "notification_count": 0, // from sync v2
          "highlight_count": 0      // from sync v2
        },
        "!sub2:bar": {
            // this room isn't even in the first 100 rooms but it is here
            // because we had an explicit room_subscription for it
        }
  }
  // the total number of rooms the user is joined to, used to pre-allocate
  // placeholder rooms for smooth scrolling
  "count": 1337, 
  "notifications": { .... } // see later section
}
```
Subsequent updates are just live-streamed to the client as and when they happen. For a topic change in the 4th room:
```json=
{
  "ops": [
    {
      "index": 3,
      "op": "UPDATE",
      "room": {
        "timeline": [
          {"sender":"@alice:example.com","type":"m.room.topic", "state_key":"", "content":{"topic":"This is a nice topic"}},
        ],
        "notification_count": 55, // increments by 1
      }
    }
  ],
  "count": 1337 // the total number of rooms the user is joined to
}
```
UPDATEs do exactly that, update fields without removing existing fields. The above response means "append to the timeline". Clients need to know that state events in the timeline ALSO mean to update the current state of the room. Updates which affect [calculating the room name](https://matrix.org/docs/spec/client_server/latest#calculating-the-display-name-for-a-room) will also update the `name` field for that room, in addition to returning the event which modifies the room name. This means clients don't need to implement the room name calculation algorithm at all. If an update occurs in a room which is both in the sorted list and an explicit room subscription, only the room subscription will receive the information: there will be no explicit UPDATE operation:
```json=
{
  "room_subscriptions": {
    "!sub1:bar": {
        "timeline": [
          {"sender":"@alice:example.com","type":"m.room.topic", "state_key":"", "content":{"topic":"This is a nice topic"}},
        ],
        "notification_count": 55, // increments by 1
    }
  }
}
```

If the user leaves the 9th room, we need to bump everything up and add an entry at the 100th position:
```json=
{
  "ops": [
    {
      "index": 8,
      "op": "UPDATE",
      "room": {
        "timeline": [
          {"sender":"@alice:example.com","type":"m.room.member", "state_key":"@alice:example.com", "content":{"membership":"leave"}},
        ]
      }
    }
    {
      "op": "DELETE",
      "index": 8
    },
    {
      "op": "INSERT",
      "index": 99,
      "room": {
          "room_id": "!foo:bar",
          "required_state": [
            {"sender":"@alice:example.com","type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"invite"}},
            {"sender":"@alice:example.com","type":"m.room.history_visibility", "state_key":"", "content":{"history_visibility":"joined"}},
            {"sender":"@alice:example.com","type":"m.space.child", "state_key":"!foo:example.com", "content":{"via":["example.com"]}},
            {"sender":"@alice:example.com","type":"m.space.child", "state_key":"!bar:example.com", "content":{"via":["example.com"]}},
            {"sender":"@alice:example.com","type":"m.space.child", "state_key":"!baz:example.com", "content":{"via":["example.com"]}}
          ],
          "timeline": [
            // We can de-dupe events in `required_state` via a top-level
            // event map so only the event IDs are referenced here.
            {"sender":"@alice:example.com","type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"invite"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"A"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"B"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"C"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"D"}},
          ]
      },
    }
  ],
  // the count is AFTER the ops have been applied so decremented by 1
  "count": 1336
}
```
It is up to the client to decide what to do here. We could have configurable options for:
 - Leaving a room removes from the list.
 - Getting banned in a room does NOT remove from the list (so the user can see they were banned).
 - Forgetting a room (e.g a banned room) then removes it from the list.

If a user joins a room in the 35th position we need to get rid of the 100th entry:
```json=
{
  "ops": [
    {
      "op": "DELETE",
      "index": 99
    },
    {
      "op": "INSERT",
      "index": 34,
      "room": {
          "room_id": "!foo:bar",
          "required_state": [
            {"sender":"@alice:example.com","type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"invite"}},
            {"sender":"@alice:example.com","type":"m.room.history_visibility", "state_key":"", "content":{"history_visibility":"joined"}},
            {"sender":"@alice:example.com","type":"m.space.child", "state_key":"!foo:example.com", "content":{"via":["example.com"]}},
            {"sender":"@alice:example.com","type":"m.space.child", "state_key":"!bar:example.com", "content":{"via":["example.com"]}},
            {"sender":"@alice:example.com","type":"m.space.child", "state_key":"!baz:example.com", "content":{"via":["example.com"]}}
          ],
          "timeline": [
            // We can de-dupe events in `required_state` via a top-level
            // event map so only the event IDs are referenced here.
            {"sender":"@alice:example.com","type":"m.room.join_rules", "state_key":"", "content":{"join_rule":"invite"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"A"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"B"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"C"}},
            {"sender":"@alice:example.com","type":"m.room.message", "content":{"body":"D"}},
          ]
      },
    }
  ],
  "count": 1337 // the count is AFTER the ops so incremented by 1
}
```
Invites would be handled outside the core `rooms` array as they often appear in their own prominent section. If a room is tracked via an explicit subscription and it enters or leaves the sorted list, only the INSERT/DELETE operations will be present, and the INSERT operation will only have the `room_id` field.

If the user scrolls down, we need to request and subscribe to the next 100 rooms:

`POST /v3/sync`:
```json=
{
  "rooms": [ [0,99], [100,199] ],  // first 200 rooms
  // request parameters are sticky and don't need to be specified again
  // a notable exception to this is 'unsubscribe_rooms' which merely alters
  // the 'room_subscriptions' map when it is received and then gets cleared.
}
```
The server sees the client wanting to subscribe to 0-99 but there is already an active subscription so it's a no-op. It is required though because the _absence_ of the range would unsubscribe the client from 0-99. The server sees a new range 100-199 so returns:
```json=
{
  "ops": [
    {
      "range": [100,199],
      "op": "SYNC",
      "rooms": [
        // ... 100 rooms ...
      ]
    }
  ]
}
```
Updates happen in the first 200 rooms now. When the client scrolls even more, the client just requests 0-99 and 200-299 (effectively the 1st and 3rd pages):
`POST /v3/sync`:
```json=
{
  "rooms": [ [0,99], [200,299] ]
}
```
The server sees 100-199 is missing and issues an invalidation to tell the client that they will be working on stale data for this range. When the user scrolls back up they will need to re-subscribe to this range:
```json=
{
  "ops": [
    {
      "op": "INVALIDATE",
      "range": [100,199]
    }
  ]
}
```

It's up to the client to decide what to do when rooms are INVALIDATEd. For offline support, these rooms should still be visible and clickable, and ultimately interactable. The client needs to speedily request that range again in case the rooms have shifted from under them. Alternatively, they can just delete the rooms and display placeholders until the range is requested again.

#### Limitations of this approach
 - Scrolling the room list becomes expensive. If a page is invalidated, they need to be fully synced from scratch again. This consumes needless bandwidth if the rooms haven't changed much.
 - Resyncing after the connection has been closed becomes expensive. The client may have many timeline events and state for a room, but will be told all of this again. If there have been no events in the room, this becomes needlessly bandwidth consuming.
 - A lot rides on the ability to detect when a connection has been closed. This is tricky (but possible) to do with long-poll connections by relying on timeouts. If the client doesn't send another `/sync` request after N seconds then the "connection" is treated as closed and a sync request with that sync token returns `M_UNKNOWN_SYNC_TOKEN` which causes the client to start over from scratch.
 - You lose the ability to "replay" sync requests. Events are live-streamed then dropped.

Care needs to be taken on the server to synchronise incoming requests for additional pages with returning deltas to the client i.e protect these operations with a shared mutex. Failure to do so could result in duplicates or missing data e.g client knows `[0,99]` and then requests `[100,199]`. At same time, room `115` gets an event and gets bumped to position `0`. If the range request is processed first, the bump needs to take into account the newly tracked range. If the event is processed first, the range request must not return the room again in `[100,199]`.

### Hybrid approach

We want sync v3 to work in low bandwidth scenarios. This means we want to make use of as much data we know the client knows about. On re-establishing a sync connection, or re-requesting a page that was previously INVALIDATEd, the server will perform the following operations:
 - For this device/session: check the last sent event ID for the room ID in question. Count the number of timeline events from that point to the latest event. Call it `N`.
 - For this specific sync request: calculate a reasonable upper-bound for how many events will be returned in a reasonable worst-case scenario. This is simply `timeline_limit + len(required_state)` (ignoring `*` wildcards on state). Call it `M`.
 - If N > M then we would probably send more events if we did a delta than just telling the client everything from scratch, so issue a `SYNC` for this room.
 - If N < M then we don't have many events since the connection was last established, so just send the delta as an `UPDATE`.

This approach has numerous benefits:
 - In the common case when you scroll a room, you won't get any `SYNC`s for rooms that were invalidated because it's highly unlikely to receive 10+ events during the room scroll (assuming you scroll back up in reasonable time).
 - When you reconnect after sleeping your laptop overnight, most rooms will be `UPDATE`s, and busy rooms like Matrix HQ will be `SYNC`ed from fresh rather than sending 100s of events.

This imposes more restrictions on the server implementation:
 - Servers still need the absolute stream ordering for events to work out how many events from `$event_id` to `$latest_event_id`.
 - Servers need to remember the last sent event ID for each session for each room. If rooms share a single monotonically increasing stream, then this is a single integer per session (akin to today's sync tokens for PDU events). Servers need to remember _which rooms_ have been sent to the client, along with the stream position when that was sent. So it's basically a `map[string]int64`.

An example of what this looks like in the response:
```json=
{
  "ops": [
    {
      "range": [100,117],
      "op": "SYNC",
      "rooms": [
        // ... 18 rooms with complete state ...
      ]
    },
    {
        "range": [118,124],
        "op": "UPDATE",
        "rooms": [
            // ... 7 rooms with a few timeline events ...
            // It is assumed that clients will keep a map of room_id -> Room object
            // and when a room gets DELETEd or INVALIDATEd in this API that the Rooms
            // are persisted as stale such that an UPDATE like this can bring it
            // up-to-date again.
        ]
    },
    {
        "range": [125,177],
        "op": "SYNC",
        "rooms": [
            // ... 53 rooms with complete state ...
        ]
    }
  ]
}
```

Some clients don't want to store state and are happy with using more bandwidth. For these clients, sync v2 has `?full_state=`. We can add a similar flag in this API to say "never incrementally catch me up from an earlier connection / invalidated page".

If a client gets a `SYNC` for a room where they previously had timeline events and state for, they MUST drop the state but can keep the timeline events as a disjointed timeline section. They may be able to tie the sections together again via `/messages` requests (backfilling).

For cases where the state resolution algorithm has deleted state, we can force a `SYNC` on that room to re-issue the correct state, with an empty timeline section to inform the client that no new events have been sent, but the current state has changed.

#### Notifications

If you are tracking the top 5 rooms and an event arrives in the 6th room, you will be notified about the event ONLY IF the sort order means the room bumps into the top 5. If for example you sorted `by_name` then you won't be notified about the event in the 6th room, unless it's an `m.room.name` event which moves the room into the top 5. In most "recent" sort orders a new event *will result* in the 6th room bumping to the top of the list. A notable exception is when the rooms are sorted in *alphabetical order* (`by_name`), which is what some other chat clients do for example. In this case, you don't care about the event unless the event is a "highlightable" event (e.g direct @mention). If you are explicitly "highlighted" in a room (according to push rules), a new section appears **at the top-level**:

```json=
{
    "notifications": [
        {
            "room_id": "!foo:bar",
            "event_id": "$aaaaaabbbbbccccc",
            "highlight_count": 1,
            "name": "The room name",
            "last_message_timestamp": 1633105777488
        }
    ]
}
```
If a client gets a notification when they are not connected to this API, the first `SYNC` response will contain a `notifications` section like this. A client will want to display this on the UI e.g "NEW UNREADS" in the below image:

![](https://i.imgur.com/Wc0A9c7.png)

In order for the "NEW UNREADS" message to be positioned at the top or bottom of the list, we need to include sorting information. This is why the notification contains enough information to sort the notification into the room list client-side. We may want to replace `last_message_timestamp` with the actual `event` which caused the notification in order to immediately display tray notifications (e.g on Desktop, which may need lazy-loaded members as well).

Clients need to "subscribe" to this room to track this room and pull in any other timeline events and state for this room. Why? Because the client has not explicitly subscribed (in a pubsub sense) to this room, so we aren't going to flood them with data whenever an unsolicited @mention arrives. This means we can send redundant data (e.g if the same user @mentions the client it's possible we will send 2x `m.room.member` events for each lazy-loaded member).

##### End-to-end encryptions (E2EE) rooms

The server cannot calculate the `highlight_count` in E2EE rooms as it cannot read the message content. This is a problem when clients want to sort by `highlight_count`. In comparison, the server can calculate the name, `unread_count`, and work out the most recent timestamp when sorting by those fields. What should the server do when the client wants to sort by `highlight_count` (which is pretty typical!)? It can:
 - Assume `highlight_count == 1` whenever `unread_count > 0`. This ensures that E2EE rooms are always bumped above unreads in the list, but doesn't allow sorting within the list of highlighted rooms.
 - Assume `highlight_count == 0` always. This will always sort E2EE rooms below the highlight list, even if the E2EE room has a @mention.
 - Sort E2EE rooms in their own dedicated list.

In all cases, the client needs to do additional work to calculate the `highlight_count`. When the client is streaming this work is very small as it just concerns a single event. However, when the client has been offline for a while there could be hundreds or thousands of missed events. There are 3 options here:
 - Do no work and immediately red-highlight the room. Risk of false positives.
 - Grab the last N messages and see if any of them are highlights. **Current implementations using sync v2 do this.**
 - Grab all the missed messages and see if any of them are highlights. Denial of service risk if there are thousands of messages.

Once the highlight count has been adequately *estimated* (it's only truly calculated if you grab all messages), this may affect the sort order for this room - it may diverge from that of the server. More specifically, it may bump the room up or down the list, depending on what the sort implementation is for E2EE rooms (top of list or below rooms with highlights). How this interacts with this API has not yet been fully determined.


### Missing bits

- Room invites. This can be in a separate section of the response, outside the sorted `rooms` array.
- Typing notifs, read receipts, room tag data, and any other room-scoped data. This can be added as request params to state whether you want these or not.
- Account data. Again, this can be added as request params and we can do similar pubsub for updates to types the client is interested in.
- To-device messages. It would be nice to have a queue per event type / sender / room so clients can rapidly get at room keys without having to wade through lots of key share requests. Need to check with the crypto team whether the ordering on to-device messages cross-event-type is important or not.
- Presence and member lists in general.
- Device lists and OTK counts.