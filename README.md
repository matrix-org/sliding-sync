![GitHub branch checks state](https://img.shields.io/github/checks-status/matrix-org/sliding-sync/main)

# sync-v3

Run an experimental sync v3 server using an existing Matrix account. This is possible because, for the most part,
v3 sync is a strict subset of v2 sync.

An implementation of [MSC3575](https://github.com/matrix-org/matrix-doc/blob/kegan/sync-v3/proposals/3575-sync.md).

Proxy version to MSC API specification:

-   Version 0.1.x: [2022/04/01](https://github.com/matrix-org/matrix-spec-proposals/blob/615e8f5a7bfe4da813bc2db661ed0bd00bccac20/proposals/3575-sync.md)
    -   First release
-   Version 0.2.x: [2022/06/09](https://github.com/matrix-org/matrix-spec-proposals/blob/3b2b3d547b41e4aeebbde2ad6e89606dd684a86c/proposals/3575-sync.md)
    -   Reworked where lists and ops are situated in the response JSON. Added new filters like `room_name_like`. Added `slow_get_all_rooms`. Standardised on env vars for configuring the proxy. Persist access tokens, encrypted with `SYNCV3_SECRET`.
-   Version 0.3.x: [2022/08/05](https://github.com/matrix-org/matrix-spec-proposals/blob/61decae837b5448b073fc5c718172f9b4d1e5e18/proposals/3575-sync.md)
    -   Spaces support, `txn_id` support.

## Usage

Requires Postgres 13+.

```bash
$ createdb syncv3
$ echo -n "$(openssl rand -hex 32)" > .secret # this MUST remain the same throughout the lifetime of the database created above.
$ go build ./cmd/syncv3
$ SYNCV3_SECRET=$(cat .secret) SYNCV3_SERVER="https://matrix-client.matrix.org" SYNCV3_DB="user=$(whoami) dbname=syncv3 sslmode=disable" SYNCV3_BINDADDR=0.0.0.0:8008 ./syncv3
```

Then visit http://localhost:8008/client/ (with trailing slash) and paste in the `access_token` for any account on `-server`.

When you hit the Sync button nothing will happen initially, but you should see:

```
INF Poller: v2 poll loop started ip=::1 since= user_id=@kegan:matrix.org
```

Wait for the first initial v2 sync to be processed (this can take minutes!) and then v3 APIs will be responsive.

### How can I help?

At present, the best way to help would be to run a local v3 server pointed at a busy account and just leave it and a client running in the background. Look at it occasionally and submit any issues you notice. You can save console logs by right-clicking -> Save As.

Please run the server with `SYNCV3_DEBUG=1` set. This will force the server to panic when assertions fail rather than just log them.
