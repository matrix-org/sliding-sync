# sync-v3

Run an experimental sync v3 server using an existing Matrix account. This is possible because, for the most part,
v3 sync is a strict subset of v2 sync.

**UNDER ACTIVE DEVELOPMENT, BREAKING CHANGES ARE FREQUENT.**

## Usage

```bash
$ createdb syncv3
$ go build ./cmd/syncv3
$ ./syncv3 -server "https://matrix-client.matrix.org" -db "user=$(whoami) dbname=syncv3 sslmode=disable"
```

Then issue Sync v3 calls.

## API overview

Sync v3 is implemented used POST requests. The URI is mostly constant:
 - `Authorization: Bearer MDA...` : The access token for your account. `?access_token=` is unsupported.
 - `/_matrix/client/v3/sync` : The path. Constant.
 - `?since=` : The sync v3 token. To begin a new sync session, don't supply a `since` value.

The body of the request consists of different streams:
```json
{
    "typing": {
        ...
    },
    "room_members": {
        ...
    },
    "to_device": {
        ...
    },
    ...
}
```
Specify as many streams as you are interested in. For example, if you are on a room member page for
Matrix HQ, you probably only need the `room_members` stream.

Each stream has request parameters which control what data gets returned, for example `typing` has
a `room_id` which specifies which room to track typing notifications in. These request parameters are
given inside the JSON object for the respective stream. For example:
```json
"typing": {
    "room_id": "!foo:bar"
}
```
These parameters are "sticky" and are remembered across requests so you don't need to constantly add
them and use up needless bandwidth. The server remembers this by associating a unique *session ID* to the `since`
token returned, so make sure you keep using the same `since` token.

A session is created when a client calls sync v3 without a `since` token. Just like how a user can
have multiple devices, a device can have multiple sessions. `to_device` messages are only purged from
the server when ALL sessions for that device have received the message. This resolves a longstanding
iOS E2EE bug whereby a device has 2 sync streams which are implicitly racing with each other and
clearing `to_device` messages, when both streams actually want the message. Sessions can be deleted
at the whim of the server (e.g to save resources). When this happens, the response will be:
```json
{
    "errcode": "M_UNKNOWN_SYNC_SESSION",
    "error": "Session unknown or expired"
}
```
Clients MUST call `/sync` again without a `?since=` value when this happens in order to make a new session.

Responses are split stream-by-stream:
```json
{
    "typing": {
        "events": [ Typing EDUs ]
    },
    "room_members": {
        "events": [ Room Member Events]
    },
    "next": "sync_v3_token"
}
```

Streams can accept more complicated filters / request parameters like `limit` and `sort`. These parameters
can be modified by the server in the response to enforce server limits. For example:
```json
{
    "room_members": {
        "limit": 99999
    }
}
```
can have the response:
```json
{
    "room_members": {
        "limit": 100
    }
}
```
This is called _negotiation_ and most streams will have some form of negotiated request parameters.

Some streams are paginatable. Paginatable streams accept and return a `p` JSON object which contains pagination
parameters. The `p` object looks like:
```json
{
    "room_members": {
        "p": {
            "next": "1" // opaque token
        }
    }
}
```

When paginated streams are returned, the next page of results are contained within `p`:
```json
{
    "room_members": {
        "p": {
            "next": "$token_to_use_for_next_page"
        },
        "events": [ Room Member Events ]
    },
    "next": "$token_to_use_to_get_new_events"
}
```
To visualise this, the value of `?since=` is a snapshot of the room, and `p.next` paginates through
this snapshot. This means that when paginating you MUST NOT advance your `?since=` value. Clients can
request the next page by specifying `since` inside `p`:
```json
{
    "room_members": {
        "p": {
            "since": "$token_from_p_next"
        }
    }
}
```
Clients are not required to paginate through all entries in a paginated stream. If you are only interested
in the first 5 room members for example, then you can set `limit: 5` and ignore `p.next`. In order to differentiate
clients which are keeping up-to-date with room members and those who are just interested in the first page
of results, clients MUST specify `p.since = "1"` to indicate the first page of results. This makes `"1"` a
sentinel constant value in pagination to indicate "the first page of results". If you don't do this, the server
will just send back deltas from the last `since` value, which won't necessarily contain the first 5 members
of your chosen sort order (e.g alphabetically).

## Streams

### Typing Stream

Request Parameters:
 - `room_id` (string): The room ID to track typing notifications in.

Response fields:
 - `events` ([]EDU): A list of typing EDUs.

### To Device Stream

TODO

### Room Member Stream

TODO

### Room Stream

TODO

### Room List Stream

TODO