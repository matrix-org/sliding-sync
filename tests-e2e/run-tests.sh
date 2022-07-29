#!/bin/bash -eu
export SYNCV3_BINDADDR=0.0.0.0:8844

# Run the binary and stop it afterwards.
../syncv3 &
SYNCV3_PID=$!
trap "kill $SYNCV3_PID" EXIT

# wait for the server to be listening, we want this endpoint to 404 instead of connrefused
until [ \
  "$(curl -s -w '%{http_code}' -o /dev/null "http://localhost:8844/idonotexist")" \
  -eq 404 ]
do
  echo 'Waiting for server to start...'
  sleep 1
done

go test "$@"