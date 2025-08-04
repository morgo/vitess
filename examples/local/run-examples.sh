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

# This test runs through the scripts in examples/local to make sure they work.
# It should be kept in sync with the steps in https://vitess.io/docs/get-started/local/
# So we can detect if a regression affecting a tutorial is introduced.

pkill -9 -f vtdataroot
rm -rf vtdataroot
killall -9 vtorc

export HOSTNAME=localhost
export hostname=localhost

source ../common/env.sh # Required so that "mysql" works from alias

set -e

./101_initial_cluster.sh

mysql < ../common/insert_commerce_data.sql
mysql --table < ../common/select_commerce_data.sql

./201_customer_tablets.sh

./202_move_tables.sh

./203_switch_reads.sh

./204_switch_writes.sh

./205_clean_commerce.sh

./301_customer_sharded.sh 

./302_new_shards.sh 

./303_reshard.sh

./304_switch_reads.sh

./305_switch_writes.sh

./306_down_shard_0.sh

./307_delete_shard_0.sh