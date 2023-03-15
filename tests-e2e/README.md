### End-to-End Tests

This directory contains end-to-end tests, running against a real Synapse server, and a real Proxy binary.

End-to-End tests meet the following criteria:

-   The test exclusively uses the public sliding sync API for test assertions.
-   The test exclusively uses the public sync v2 API for configuring the test.

Some examples of this include testing core functionality of the API like room subscriptions, multiple lists, filters, extensions, etc.

Counter examples include testing restarts of the proxy, testing out-of-order responses and testing connection handling. These should be integration tests.

#### Development

Run a Synapse in a separate terminal:

```bash
docker run --rm -e "SYNAPSE_COMPLEMENT_DATABASE=sqlite" -e "SERVER_NAME=synapse" -p 8008:8008 ghcr.io/matrix-org/synapse-service:v1.62.0
```

(You will need to [`docker login` to the GitHub container registry](
    https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry#authenticating-with-a-personal-access-token-classic
) to pull that image.)

Keep it running. Then run the tests on a fresh postgres database (run in the root of this repository):

```bash
export SYNCV3_SERVER=http://localhost:8008
export SYNCV3_DB="user=$(whoami) dbname=syncv3_test sslmode=disable"
export SYNCV3_SECRET=secret
(dropdb syncv3_test && createdb syncv3_test && cd tests-e2e && ./run-tests.sh .)
```

All args after `run-test.sh` are passed to `go test` so you can set timeouts/run individual tests that way, hence the `.` in the above example as that translated to `go test .`.
