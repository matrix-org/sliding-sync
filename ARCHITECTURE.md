## Architecture

_Current as of December 2022_

At a high-level, clients (like Element) talk directly to their own homeserver (like Synapse) for every
single CS API endpoint as usual. Clients which opt-in to sliding sync will no longer call `/sync`, and
will instead speak to the unstable endpoint on the proxy for sync data. The sliding sync API requires
a valid Bearer token for the client so it can perform `/sync` requests on the user's behalf.

```
+---------+                      +-------+
| Clients | <------------------> | Proxy | -.
+---------+   Sliding Sync API   +-------+  | /sync
     |                                      V
     |                           +------------+
     `-------------------------> | Homeserver |
    All other CS API endpoints   +------------+
```

### Proxy design

Data flows in one direction: from `/sync` to the client connection. Data runs through a dispatcher (event emitter)
which hits various in-memory caches which are designed for certain scopes of data:

-   Global: Data which has an absolute truth for all users. E.g the room topic, who is joined to each room.
-   User: Data which has different truths depending on the user requesting the data. E.g. unread counts in a room.
-   Connection: Data which is scoped to a specific sliding sync connection. E.g. room lists and subscriptions.

```
            9
        .----------------------------------------------------------------------.
        |                                                                      V
+--------+ ---> +-----------+      +-----------+      +------------+      +---------+      +-----------+      +------------+
| Client | <--- | ConnState | <--- | UserCache | <--- | Dispatcher | <--- | Handler | <--- | V2 Poller | <--- | Homeserver |
+--------+   8  +-----------+   7  +-----------+   6  +------------+   4  +---------+   2  +-----------+   1  +------------+
                                                          |                       |
                                   +-------------+        | 5        .____.       | 3
                                   | GlobalCache | <------`          | DB | <-----`
                                   +-------------+                   `----`

```

-   1: `/sync` loop provides data to the poller goroutines (1 per connection).
-   2: The poller consolidates data into a single poller goroutine and invokes the HTTP handler.
-   3: The database is updated with the data.
-   4: The handler invokes the dispatcher. The dispatcher works out which users need to be notified of this data and invokes callbacks for all registered listeners, always starting with the global cache.
-   5: The global cache remembers global room state which does not change between users.
-   6: The user cache remembers user-scoped data which does not change between connections for the same user.
-   7: The user cache informs any active sliding sync connections of the new data.
-   8: The connection state notifies the client or buffers the data for when they next make a request.
-   9: The client makes a connection, which reads buffered data or returns results using the User/Global cache data.

The following sections detail each stage of the pipeline.

#### V2 Poller

It's just a goroutine which hits `/sync` repeatedly and invokes callbacks when it gets data. Parses sync v2 JSON. All callbacks are scheduled onto a main 'executor' goroutine.

#### Handler

It's a `http.Handler` that serves as a way to glue together the entire pipeline. It creates a lot of these components.

#### DB

One file per table. Contains an "accumulator" which keeps track of the state of the room and state snapshots.

#### Dispatcher

An `EventEmitter` which notifies about generic `Updates` for a target user. If the user is a special value `DispatcherAllUsers` then the dispatcher notifies for all events, which is what the `GlobalCache` does.

#### GlobalCache

Persists in-memory structs which contain global room metadata, which remains the same for all users.

#### UserCache

Persists in-memory structs which contain user-scoped room metadata, which remains the same for all connections for a single user.

#### ConnState

This is the most complicated part of the proxy, as it needs to juggle incoming data from two sources: the homeserver and the client. By this point, data is heavily processed into a useful form: The `Handler` has pre-processed the client request and the pipeline has processed rooms into an easy-to-extract format or parsed updates into `Update` structs.

### Stand-alone components

There are many structs which are not directly involved with the core data flows in the server. These are summarised as follows:

-   `JoinedRoomsTracker`: A map which controls who is joined to each room. Critical for security.
-   `Conn`: Handles idempotency and retries for a connection. Maintains the `pos` values.
-   `ConnMap`: Maintains a thread-safe map of connection ID to `Conn`. Handles expiry based on inactivity.
-   `Request`: The sliding sync request. Contains all sorts of logic to work out deltas based on sticky parameters, write operations based on index positions, and union room subscription information over multiple lists.
-   `SliceRanges`: Contains algorithms for room list ranges e.g finding deltas, creating subslices.
-   `Response`: The sliding sync response.
-   `SortableRooms`: A slice of room information which can be sorted. A filtered variant is also used via `FilteredSortableRooms`.
-   `InternalRequestLists`: Contains all the rooms for a users account, as well as which `FilteredSortableRooms` are registered.
