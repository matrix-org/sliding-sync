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

Then visit http://localhost:8008/client/ (with trailing slash) and paste in the `access_token` for any account on `-server`.

When you hit the Sync button nothing will happen initially, but you should see:
```
INF Poller: v2 poll loop started ip=::1 since= user_id=@kegan:matrix.org
```
Wait for the first initial v2 sync to be processed (this can take minutes!) and then v3 APIs will be responsive.

## API

API is under active development and is not stable.
