![GitHub branch checks state](https://img.shields.io/github/checks-status/matrix-org/sliding-sync/main)

# sync-v3

Run an experimental sync v3 server using an existing Matrix account. This is possible because, for the most part,
v3 sync is a strict subset of v2 sync.

An implementation of [MSC3575](https://github.com/matrix-org/matrix-doc/blob/kegan/sync-v3/proposals/3575-sync.md).

## Usage

Requires Postgres 13+.

```bash
$ createdb syncv3
$ go build ./cmd/syncv3
$ ./syncv3 -server "https://matrix-client.matrix.org" -db "user=$(whoami) dbname=syncv3 sslmode=disable"
```

Then visit http://localhost:8008/client/ (with trailing slash) and paste in the `access_token` for any account on `-server`.

When you hit the Sync button nothing will happen initially, but you should see:

```
INF Poller: v2 poll loop started ip=::1 since= user_id=@kegan:matrix.org
```

Wait for the first initial v2 sync to be processed (this can take minutes!) and then v3 APIs will be responsive.

### Docker

When running using the official docker container, these environment variables can be set:

```
SYNCV3_SERVER   equivalent of -server
SYNCV3_DB       equivalent of -db
SYNCV3_BINDADDR equivalent of -port, default: 0.0.0.0:8008
```

### How can I help?

At present, the best way to help would be to run a local v3 server pointed at a busy account and just leave it and a client running in the background. Look at it occasionally and submit any issues you notice. You can save console logs by right-clicking -> Save As.

Please run the server with `SYNCV3_DEBUG=1` set. This will force the server to panic when assertions fail rather than just log them.
