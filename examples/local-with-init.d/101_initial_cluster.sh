#!/bin/bash

# Copyright 2019 The Vitess Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Copy the example configuration file to /etc/vitess.yaml
# You may need to adjust to use sudo cp here.
cp ../common/config/vitess.yaml.101-example /etc/vitess.yaml

# Source the utility functions for this script.
# This is required for wait_for_healthy_shard and fail functions.
source ../init.d/utils.sh

# This is done here as a means to support testing the experimental
# custom sidecar database name work in a wide variety of scenarios
# as the local examples are used to test many features locally.
# This is NOT here to indicate that you should normally use a
# non-default (_vt) value or that it is somehow a best practice
# to do so. In production, you should ONLY use a non-default
# sidecar database name when it's truly needed.
SIDECAR_DB_NAME=${SIDECAR_DB_NAME:-"_vt"}

# Step 1. Start the topology server (topo) and vtctld.
# This is the first step in bringing up the Vitess cluster.
../init.d/topo start
../init.d/vtctld start

# Step 2. create our first keyspace for commerce.

if vtctldclient GetKeyspace commerce > /dev/null 2>&1 ; then
	# Keyspace already exists: we could be running this 101 example on an non-empty VTDATAROOT
	vtctldclient SetKeyspaceDurabilityPolicy --durability-policy=semi_sync commerce || fail "Failed to set keyspace durability policy on the commerce keyspace"
else
	# Create the keyspace with the sidecar database name and set the
	# correct durability policy. Please see the comment above for
	# more context on using a custom sidecar database name in your
	# Vitess clusters.
	vtctldclient CreateKeyspace --sidecar-db-name="${SIDECAR_DB_NAME}" --durability-policy=semi_sync commerce || fail "Failed to create and configure the commerce keyspace"
fi

# Step 3. Start all mysqlctls and tablets.
../init.d/mysqlctl-vttablet start

# Step 4. start vtorc. This will help pick a primary tablet
../init.d/vtorc start

# Wait for all the tablets to be up and registered in the topology server
# and for a primary tablet to be elected in the shard and become healthy/serving.
echo "Waiting for tablets to be healthy and serving in the commerce keyspace..."
wait_for_healthy_shard commerce 0 || exit 1

# create the schema
vtctldclient ApplySchema --sql-file create_commerce_schema.sql commerce || fail "Failed to apply schema for the commerce keyspace"

# create the vschema
vtctldclient ApplyVSchema --vschema-file vschema_commerce_initial.json commerce || fail "Failed to apply vschema for the commerce keyspace"

# start vtgate
../init.d/vtgate start

# start vtadmin
../init.d/vtadmin start

echo "Cluster is now running!"
echo "Access vtgate at: http://localhost:15001/debug/status"
echo "Access vtctld at: http://localhost:15000/debug/status"
echo "Access vtadmin at: http://localhost:14200"