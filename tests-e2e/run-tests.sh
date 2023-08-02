#!/bin/bash -eu
export SYNCV3_BINDADDR=0.0.0.0:8844
export SYNCV3_ADDR='http://localhost:8844'
export SYNCV3_DEBUG=1

# Run the binary and stop it afterwards.
# Direct stderr into stdout, and optionally redirect both to a file.
../syncv3 &> "${E2E_TEST_SERVER_STDOUT:-/dev/stdout}" &
SYNCV3_PID=$!
trap "kill $SYNCV3_PID" EXIT

waits=0
# wait for the server to be listening, we want this endpoint to 404 instead of connrefused
until [ \
  "$(curl -s -w '%{http_code}' -o /dev/null "http://localhost:8844/idonotexist")" \
  -eq 404 ]
do
  echo 'Waiting for server to start...'
  waits=$((waits+1))
  if [ "$waits" -ge 60 ]; then
    echo "Server did not start within a minute" > /dev/stderr
    exit 1
  fi
  sleep 1
done

# if the metrics env var is set, sanity check that metrics are being served
if [ -n "${SYNCV3_PROM+1}" ]; then
  status="$(curl --silent --write-out '%{http_code}' -o /dev/null http://localhost:2112/metrics)"
  if [ "$status" != 200 ]; then
    echo "Metrics endpoint returned $status, expected 200" > /dev/stderr
    exit 1
  else
    echo "Metrics endpoint returned 200 OK."
  fi
fi

go test "$@"