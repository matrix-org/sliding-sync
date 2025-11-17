# Sliding Sync

## This repository is archived

The sliding sync proxy implements [MSC3575](https://github.com/matrix-org/matrix-spec-proposals/issues/3575), which has been superseded by [MSC4186](https://github.com/matrix-org/matrix-spec-proposals/issues/4186): Simplified Sliding Sync. The proxy is no longer being worked on and is now archived.

Homeservers are now implementing Simplified Sliding Sync natively. Modern Matrix clients are no longer expected to work with this proxy.

---

Run a sliding sync proxy. An implementation of [MSC3575](https://github.com/matrix-org/matrix-doc/blob/kegan/sync-v3/proposals/3575-sync.md).

## Proxy version to MSC API specification

This describes which proxy versions implement which version of the API drafted
in MSC3575. See https://github.com/matrix-org/sliding-sync/releases for the
changes in the proxy itself.

As of v0.99.12, the proxy implements [this version of the MSC](https://github.com/matrix-org/matrix-spec-proposals/blob/9450ced7fb9cf5ea9077d029b3adf36aebfa8709/proposals/3575-sync.md) with the following exceptions:
 - the `limited` flag is not set in responses.
 - Delta tokens are unsupported.


## Usage

*NOTE: The proxy works fine with Dendrite and Synapse, but it doesn't work well with Conduit due to spec violations in the `state` of a room in `/sync`. Running the proxy with Conduit will cause more expired connections (HTTP 400s) when room state changes, and log lines like `WRN Accumulator.filterToNewTimelineEvents: seen the same event ID twice, ignoring`.*

### Setup
Requires Postgres 13+.

First, you must create a Postgres database and secret:
```bash
$ createdb syncv3
$ echo -n "$(openssl rand -hex 32)" > .secret # this MUST remain the same throughout the lifetime of the database created above.
```

The Sliding Sync proxy requires some environment variables set to function. They are described when the proxy is run with missing variables.

Here is a short description of each, as of writing:
```
SYNCV3_SERVER        Required. The destination homeserver to talk to (CS API HTTPS URL) e.g 'https://matrix-client.matrix.org' (Supports unix socket: /path/to/socket)
SYNCV3_DB            Required. The postgres connection string: https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING
SYNCV3_SECRET        Required. A secret to use to encrypt access tokens. Must remain the same for the lifetime of the database.
SYNCV3_BINDADDR      Default: 0.0.0.0:8008. The interface and port to listen on. (Supports unix socket: /path/to/socket)
SYNCV3_TLS_CERT      Default: unset. Path to a certificate file to serve to HTTPS clients. Specifying this enables TLS on the bound address.
SYNCV3_TLS_KEY       Default: unset. Path to a key file for the certificate. Must be provided along with the certificate file.
SYNCV3_PPROF         Default: unset. The bind addr for pprof debugging e.g ':6060'. If not set, does not listen.
SYNCV3_PROM          Default: unset. The bind addr for Prometheus metrics, which will be accessible at /metrics at this address.
SYNCV3_OTLP_URL      Default: unset. The OTLP HTTP URL to send spans to e.g https://localhost:4318 - if unset does not send OTLP traces.
SYNCV3_OTLP_USERNAME Default: unset. The OTLP username for Basic auth. If unset, does not send an Authorization header.
SYNCV3_OTLP_PASSWORD Default: unset. The OTLP password for Basic auth. If unset, does not send an Authorization header.
SYNCV3_SENTRY_DSN    Default: unset. The Sentry DSN to report events to e.g https://sliding-sync@sentry.example.com/123 - if unset does not send sentry events.
SYNCV3_LOG_LEVEL     Default: info. The level of verbosity for messages logged. Available values are trace, debug, info, warn, error and fatal
SYNCV3_MAX_DB_CONN   Default: unset. Max database connections to use when communicating with postgres. Unset or 0 means no limit.
```

It is easiest to host the proxy on a separate hostname than the Matrix server, though it is possible to use the same hostname by forwarding the used endpoints.

In both cases, the path `https://example.com/.well-known/matrix/client` must return a JSON with at least the following contents:
```json
{
    "m.homeserver": {
        "base_url": "https://example.com"
    },
    "org.matrix.msc3575.proxy": {
        "url": "https://syncv3.example.com"
    }
}
```

#### Same hostname
The following nginx configuration can be used to pass the required endpoints to the sync proxy, running on local port 8009 (so as to not conflict with Synapse):
```nginx
location ~ ^/(client/|_matrix/client/unstable/org.matrix.msc3575/sync) {
    proxy_pass http://localhost:8009;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Host $host;
}

location ~ ^(\/_matrix|\/_synapse\/client) {
    proxy_pass http://localhost:8008;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Host $host;
}

location /.well-known/matrix/client {
    add_header Access-Control-Allow-Origin *;
}
```

### Running
There are three ways to run the proxy:
- Compiling from source:
```
$ CGO_ENABLED=0 go build ./cmd/syncv3
$ SYNCV3_SECRET=$(cat .secret) SYNCV3_SERVER="https://matrix-client.matrix.org" SYNCV3_DB="user=$(whoami) dbname=syncv3 sslmode=disable password='DATABASE_PASSWORD_HERE'" SYNCV3_BINDADDR=0.0.0.0:8008 ./syncv3
```

- Using a Docker image:
```
docker run --rm -e "SYNCV3_SERVER=https://matrix-client.matrix.org" -e "SYNCV3_SECRET=$(cat .secret)" -e "SYNCV3_BINDADDR=:8008" -e "SYNCV3_DB=user=$(whoami) dbname=syncv3 sslmode=disable host=host.docker.internal password='DATABASE_PASSWORD_HERE'" -p 8008:8008 ghcr.io/matrix-org/sliding-sync:latest
```

- Precompiled binaries:

Download the binary for your architcture (eg. `syncv3_linux_amd64` for 64-bit AMD/Intel) from https://github.com/matrix-org/sliding-sync/releases/latest
```
$ SYNCV3_SECRET=$(cat .secret) SYNCV3_SERVER="https://matrix-client.matrix.org" SYNCV3_DB="user=$(whoami) dbname=syncv3 sslmode=disable password='DATABASE_PASSWORD_HERE'" SYNCV3_BINDADDR=0.0.0.0:8008 ./syncv3_linux_amd64
```

Optionally also set `SYNCV3_TLS_CERT=path/to/cert.pem` and `SYNCV3_TLS_KEY=path/to/key.pem` to listen on HTTPS instead of HTTP.
Make sure to tweak the `SYNCV3_DB` environment variable if the Postgres database isn't running on the host.

Regular users may now log in with their sliding-sync compatible Matrix client. If developing sliding-sync, a simple client is provided (although it is not included in the Docker image).

To use the stub client, visit http://localhost:8008/client/ (with trailing slash) and paste in the `access_token` for any account on `SYNCV3_SERVER`. Note that this will consume to-device messages for the device associated with that access token.

When you hit the Sync button nothing will happen initially, but you should see:

```
INF Poller: v2 poll loop started ip=::1 since= user_id=@kegan:matrix.org
```

Wait for the first initial v2 sync to be processed (this can take minutes!) and then v3 APIs will be responsive.

Note that some clients might require that your home server advertises support for sliding-sync in the `.well-known/matrix/client` endpoint; details are in [the work-in-progress specification document](https://github.com/matrix-org/matrix-spec-proposals/blob/kegan/sync-v3/proposals/3575-sync.md#unstable-prefix).

There are some notes on the log format in [docs/log_format.md](./docs/log_format.md).

### Prometheus

To enable metrics, pass `SYNCV3_PROM=:2112` to listen on that port and expose a scraping endpoint `GET /metrics`.
If you want to hook this up to a prometheus, you can just define `prometheus.yml`:
```yaml
global:
    scrape_interval: 30s
    scrape_timeout: 10s
scrape_configs:
    - job_name: ss
      static_configs:
       - targets: ["host.docker.internal:2112"]
```
then run Prometheus in a docker container:
```bash
docker run -p 9090:9090 -v /path/to/prometheus.yml:/etc/prometheus/prometheus.yml prom/prometheus
```
to play with the data, use PromLens and point it at http://localhost:9090:
```bash
docker run -p 8080:8080 prom/promlens
```
Useful queries include:
 - `rate(sliding_sync_poller_num_payloads{job="ss"}[5m])` : This shows the payload rate from pollers to API processes,
   broken down by type. A stacked graph display is especially useful as the height then represents the total payload
   rate. This can be used to highlight abnormal incoming data, such as excessive payload rates. It can also be used
   to gauge how costly certain payload types are. In general, receipts and device data tend to be the most frequent
   background noise. A full list of payload types are defined in the [pubsub directory](https://github.com/matrix-org/sliding-sync/blob/main/pubsub/v2.go).
 - `sliding_sync_poller_num_pollers` : Absolute count of the number of /sync v2 pollers over time. Useful either as a single value,
   or display over time. The higher this value, the more pressure is put on the upstream Homeserver.
 - `sliding_sync_api_num_active_conns` : Absolute count of the number of active sliding sync connections. Useful either as a single value,
   or display over time. The higher this value, the more pressure is put on the proxy API processes.
 - `sum(increase(sliding_sync_poller_process_duration_secs_bucket[1m])) by (le)` : Useful heatmap to show how long /sync v2 responses take to process.
   This can highlight database pressure as processing responses involves database writes and notifications over pubsub.
 - `sum(increase(sliding_sync_api_process_duration_secs_bucket[1m])) by (le)` : Useful heatmap to show how long sliding sync responses take to calculate,
   which excludes all long-polling requests. This can highlight slow sorting/database performance, as these requests should always be fast.

### Can I delete the proxy database and start over?

*It is not recommended to do this.*

Yes, with caveats. Most of the data is just a copy of data from the upstream homeserver. The exceptions to this are:
 - device list changes (table: `syncv3_device_list_updates`)
 - to-device messages (table: `syncv3_to_device_messages` and related sequence `syncv3_to_device_messages_seq`)

Both of these are critical for E2EE to work correctly as it ensures A) clients know up-to-date devices for users, B) clients can reliably send messages directly to devices.
If the device list changes are deleted, Alice may not realise that Bob logged in on a new device and hence not encrypt for that new device. Encryption keys are sent in to-device messages, so deleting them will inevitably cause some messages to become undecryptable. If you don't care about causing undecryptable messages then you can just delete the database and start over. If you do care, preserve those tables/sequences. We cannot guarantee that the database will remain in a consistent state (things may break if you keep some tables) as we do not test this scenario.

### Profiling

To help debug performance issues, you can make the proxy listen for PPROF requests by passing `SYNCV3_PPROF=:6060` to listen on `:6060`.
To debug **why a request is slow**:
```
wget -O 'trace.pprof' 'http://localhost:6060/debug/pprof/trace?seconds=20'
```
Then perform the slow request within 20 seconds. Send `trace.pprof` to someone who will then run `go tool trace trace.pprof` and look at "User-defined Tasks" for slow HTTP requests.

To debug **why the proxy is consuming lots of memory**, run:
```
wget -O 'heap.pprof' 'http://localhost:6060/debug/pprof/heap'
```
Then send `heap.pprof` to someone who will then run `go tool pprof heap.pprof` and probably type something like `top10`:
```
(pprof) top10
Showing nodes accounting for 83.13MB, 100% of 83.13MB total
Showing top 10 nodes out of 82
      flat  flat%   sum%        cum   cum%
   43.01MB 51.74% 51.74%    43.01MB 51.74%  github.com/tidwall/gjson.ParseBytes
   31.85MB 38.31% 90.05%    31.85MB 38.31%  github.com/matrix-org/sliding-sync/sync3.(*JoinedRoomsTracker).Startup
       4MB  4.82% 94.87%        4MB  4.82%  runtime.allocm
    1.76MB  2.12% 96.99%     1.76MB  2.12%  compress/flate.NewWriter
    0.50MB  0.61% 97.59%        1MB  1.21%  github.com/matrix-org/sliding-sync/sync3.(*SortableRooms).Sort
    0.50MB   0.6% 98.20%     0.50MB   0.6%  runtime.malg
    0.50MB   0.6% 98.80%     0.50MB   0.6%  github.com/matrix-org/sliding-sync/sync3.(*InternalRequestLists).Room
    0.50MB   0.6% 99.40%     0.50MB   0.6%  github.com/matrix-org/sliding-sync/sync3.(*Dispatcher).notifyListeners
    0.50MB   0.6%   100%     0.50MB   0.6%  runtime.acquireSudog
         0     0%   100%     1.76MB  2.12%  bufio.(*Writer).Flush
```

To debug **why the proxy is using 100% CPU**, run:
```
wget -O 'profile.pprof' 'http://localhost:6060/debug/pprof/profile?seconds=10'
```
Then send `profile.pprof` to someone who will then run `go tool pprof -http :5656 profile.pprof` and typically view the flame graph: View -> Flame Graph.

To build **with PGO**, run:
```
$ wget -O 'default.pgo' 'http://localhost:6060/debug/pprof/profile?seconds=30'
$ CGO_ENABLED=0 go build -o after_pgo ./cmd/syncv3
```


### Developers' cheat sheet

Sanity check everything builds:

```shell
CGO_ENABLED=0 go build ./cmd/syncv3
go list ./... | xargs -n1 go test -c -o /dev/null
```

Run all unit and integration tests:

```shell
go test -p 1 -count 1 $(go list ./... | grep -v tests-e2e) -timeout 120s
```

Run end-to-end tests:

```shell
# Will need to `docker login` to ghcr and pull the image.
docker run -d --rm -e "SYNAPSE_COMPLEMENT_DATABASE=sqlite" -e "SERVER_NAME=synapse" -p 8888:8008 ghcr.io/matrix-org/synapse-service:v1.94.0

export SYNCV3_SECRET=foobar
export SYNCV3_SERVER=http://localhost:8888
export SYNCV3_DB="user=$(whoami) dbname=syncv3_test sslmode=disable"

(go build ./cmd/syncv3 && dropdb syncv3_test && createdb syncv3_test && cd tests-e2e && ./run-tests.sh -count=1 .)
```
