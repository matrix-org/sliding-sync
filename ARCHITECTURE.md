## Architecture

_Current as of August 2022_

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
