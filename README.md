# Sliding Sync

Run a sliding sync proxy. An implementation of [MSC3575](https://github.com/matrix-org/matrix-doc/blob/kegan/sync-v3/proposals/3575-sync.md).

Proxy version to MSC API specification:

-   Version 0.1.x: [2022/04/01](https://github.com/matrix-org/matrix-spec-proposals/blob/615e8f5a7bfe4da813bc2db661ed0bd00bccac20/proposals/3575-sync.md)
    -   First release
-   Version 0.2.x: [2022/06/09](https://github.com/matrix-org/matrix-spec-proposals/blob/3b2b3d547b41e4aeebbde2ad6e89606dd684a86c/proposals/3575-sync.md)
    -   Reworked where lists and ops are situated in the response JSON. Added new filters like `room_name_like`. Added `slow_get_all_rooms`. Standardised on env vars for configuring the proxy. Persist access tokens, encrypted with `SYNCV3_SECRET`.
-   Version 0.3.x: [2022/08/05](https://github.com/matrix-org/matrix-spec-proposals/blob/61decae837b5448b073fc5c718172f9b4d1e5e18/proposals/3575-sync.md)
    -   Spaces support, `txn_id` support.
-   Version 0.4.x [2022/08/23](https://github.com/matrix-org/matrix-spec-proposals/blob/59c83a857b4cf3cf6aca593c34efb44709b10d17/proposals/3575-sync.md)
    -   Support for `tags` and `not_tags`.
-   Version 0.98.x [2022/12/16](https://github.com/matrix-org/matrix-spec-proposals/blob/2538552705487ecef34abf1dd1afb61e25a06f28/proposals/3575-sync.md)
    -   Preparing for major v1.x release: add Prometheus metrics, PPROF, etc.
    -   Support `typing` and `receipts` extensions.
    -   Support for `num_live`, `joined_count` and `invited_count`.
    -   Support for `by_notification_level` and `include_old_rooms`.
    -   Support for `$ME` and `$LAZY`.
    -   Support for `errcode` when sessions expire.
-   Version 0.99.1 [2023/01/20](https://github.com/matrix-org/matrix-spec-proposals/blob/b4b4e7ff306920d2c862c6ff4d245110f6fa5bc7/proposals/3575-sync.md)
    -   Preparing for major v1.x release: lists-as-keys support.
-   Version 0.99.2 [2024/07/27](https://github.com/matrix-org/matrix-spec-proposals/blob/eab643cb3ca63b03537a260fa343e1fb2d1ee284/proposals/3575-sync.md)
    -   Experimental support for `bump_event_types` when ordering rooms by recency.
    -   Support for opting in to extensions on a per-list and per-room basis.
    -   Sentry support.

## Usage

Requires Postgres 13+.

```bash
$ createdb syncv3
$ echo -n "$(openssl rand -hex 32)" > .secret # this MUST remain the same throughout the lifetime of the database created above.
```

Compiling from source and running:
```bash
$ go build ./cmd/syncv3
$ SYNCV3_SECRET=$(cat .secret) SYNCV3_SERVER="https://matrix-client.matrix.org" SYNCV3_DB="user=$(whoami) dbname=syncv3 sslmode=disable" SYNCV3_BINDADDR=0.0.0.0:8008 ./syncv3

# China-based users first need to run:
$ go env -w GO111MODULE=on
$ go env -w GOPROXY=https://goproxy.cn,direct
```
Using a Docker image:
```
docker run --rm -e "SYNCV3_SERVER=https://matrix-client.matrix.org" -e "SYNCV3_SECRET=$(cat .secret)" -e "SYNCV3_BINDADDR=:8008" -e "SYNCV3_DB=user=$(whoami) dbname=syncv3 sslmode=disable host=host.docker.internal" -p 8008:8008 ghcr.io/matrix-org/sliding-sync:latest
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


### Developers' cheat sheet

Sanity check everything builds:

```shell
go build ./cmd/syncv3
go list ./... | xargs -n1 go test -c -o /dev/null
```

Run all unit and integration tests:

```shell
go test -p 1 -count 1 $(go list ./... | grep -v tests-e2e) -timeout 120s
```

Run end-to-end tests:

```shell
# Run each line in a separate terminal windows. Will need to `docker login`
# to ghcr and pull the image.
docker run --rm -e "SYNAPSE_COMPLEMENT_DATABASE=sqlite" -e "SERVER_NAME=synapse" -p 8888:8008 ghcr.io/matrix-org/synapse-service:v1.72.0
(go build ./cmd/syncv3 && dropdb syncv3_test && createdb syncv3_test && cd tests-e2e && ./run-tests.sh -count=1 .)
```
