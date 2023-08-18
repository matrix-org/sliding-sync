#!/bin/bash -eu
export SYNCV3_BINDADDR=0.0.0.0:8844
export SYNCV3_ADDR='http://localhost:8844'
export SYNCV3_DEBUG=1

# Run synapse and stop it afterwards.
python -m synapse.app.homeserver -c synapse-homeserver.yaml &
SYNAPSE_PID=$!

# Run the binary and stop it afterwards.
# Direct stderr into stdout, and optionally redirect both to a file.
../syncv3 &> "${E2E_TEST_SERVER_STDOUT:-/dev/stdout}" &
SYNCV3_PID=$!
trap "kill $SYNCV3_PID; kill $SYNAPSE_PID" EXIT

# wait for the server to be listening, we want this endpoint to 404 instead of connrefused
until [ \
  "$(curl -s -w '%{http_code}' -o /dev/null "http://localhost:8844/idonotexist")" \
  -eq 404 ]
do
  echo 'Waiting for server to start...'
  sleep 1
done

go test "$@"