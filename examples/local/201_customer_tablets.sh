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

vtctldclient CreateKeyspace --sidecar-db-name="_vt" --durability-policy=semi_sync customer || fail "Failed to create keyspace 'customer'"

vtctldclient CreateVirtualShard customer/0 main/0 || fail "Failed to create virtual shard 'customer/0'"
