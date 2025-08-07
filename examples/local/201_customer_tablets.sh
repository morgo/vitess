#!/bin/bash

# Copyright 2020 The Vitess Authors.
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

# this script creates the tablets and initializes them for vertical
# resharding it also splits the vschema between the two keyspaces
# old (commerce) and new (customer)

source ../common/env.sh

physical_keyspace=main
# vtctldclient CreateKeyspace --sidecar-db-name="_vt" --durability-policy=semi_sync main2 || fail "Failed to create and configure the main2 keyspace"
#
# for i in 200 201 202; do
# 	CELL=zone1 TABLET_UID=$i ../common/scripts/mysqlctl-up.sh
# 	CELL=zone1 KEYSPACE=main2 TABLET_UID=$i ../common/scripts/vttablet-up.sh
# done

# Wait for all the tablets to be up and registered in the topology server
# and for a primary tablet to be elected in the shard and become healthy/serving.
wait_for_healthy_shard "$physical_keyspace" 0 || exit 1

vtctldclient CreateKeyspace --sidecar-db-name="_vt" --durability-policy=semi_sync customer || fail "Failed to create keyspace 'customer'"

vtctldclient CreateVirtualShard customer/0 "$physical_keyspace"/0 || fail "Failed to create virtual shard 'customer/0'"

# TODO: I will figure out how to automatically rebuild the keyspace graph later.
vtctldclient RebuildKeyspaceGraph --cells=zone1 customer
vtctldclient ApplyVSchema --vschema "{}" customer

