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

# this script brings up new tablets for the two new shards that we will
# be creating in the customer keyspace and copies the schema

source ../common/env.sh

vtctldclient CreateKeyspace --sidecar-db-name="_vt" --durability-policy=semi_sync main3 || fail "Failed to create and configure the main3 keyspace"
vtctldclient CreateKeyspace --sidecar-db-name="_vt" --durability-policy=semi_sync main4 || fail "Failed to create and configure the main4 keyspace"

for i in 300 301 302; do
    CELL=zone1 TABLET_UID=$i ../common/scripts/mysqlctl-up.sh
    CELL=zone1 KEYSPACE=main3 TABLET_UID=$i ../common/scripts/vttablet-up.sh
done

for i in 400 401 402; do
    CELL=zone1 TABLET_UID=$i ../common/scripts/mysqlctl-up.sh
    CELL=zone1 KEYSPACE=main4 TABLET_UID=$i ../common/scripts/vttablet-up.sh
done

wait_for_healthy_shard main3 0 || exit 1
wait_for_healthy_shard main4 0 || exit 1


# Customer is currently located on the "main2/0" physical shard
# For the reshard, I am moving it to "main3/0"
vtctldclient CreateVirtualShard customer/-80 main3/0 || fail "Failed to create virtual shard 'customer/-80'"
vtctldclient CreateVirtualShard customer/80- main4/0 || fail "Failed to create virtual shard 'customer/80-'"
