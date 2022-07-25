#!/bin/bash

ping -c 2 synapse
dig synapse
curl -v -XGET 'http://synapse:8008/_matrix/client/versions'