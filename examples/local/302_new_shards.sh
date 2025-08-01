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

# Customer is currently located on the "main2/0" physical shard
# For the reshard, I am moving it to "main/0"
# TODO: This is currently returning an error, we need to fix it:
# E0801 09:01:22.925258   55538 main.go:60] unknown shorthand flag: '8' in -80
vtctldclient CreateVirtualShard customer "-80" main 0 || fail "Failed to create virtual shard 'customer/-80'"
vtctldclient CreateVirtualShard customer "80-" main 0 || fail "Failed to create virtual shard 'customer/80-'"

for shard in "-80" "80-"; do
    # Wait for all the tablets to be up and registered in the topology server
    # and for a primary tablet to be elected in the shard and become healthy/serving.
    wait_for_healthy_shard customer "${shard}" || exit 1
done;