#!/bin/bash

# Copyright 2023 The Vitess Authors.
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

# This file contains utility functions that can be used throughout the
# various examples.

# Check for required binaries
for binary in mysqld etcd etcdctl curl vtctldclient vttablet vtgate vtctld mysqlctl yq; do
  command -v "$binary" > /dev/null || fail "${binary} is not installed in PATH. See https://vitess.io/docs/get-started/local/ for install instructions."
done;

# Wait for the given number of tablets to show up in the topology server
# for the keyspace/shard. Example (wait for 2 tablets in commerce/0):
#   wait_for_shard_tablets commerce 0 2
function wait_for_shard_tablets() {
	if [[ -z ${1} || -z ${2} || -z ${3} ]]; then
		fail "A keyspace, shard, and number of tablets must be specified when waiting for tablets to come up"
	fi
	local keyspace=${1}
	local shard=${2}
	local num_tablets=${3}
	local wait_secs=30

	for _ in $(seq 1 ${wait_secs}); do
		cur_tablets=$(vtctldclient GetTablets --keyspace "${keyspace}" --shard "${shard}" | wc -l)
		if [[ ${cur_tablets} -eq ${num_tablets} ]]; then
			break
		fi
		sleep 1
	done;

	cur_tablets=$(vtctldclient GetTablets --keyspace "${keyspace}" --shard "${shard}" | wc -l)
	if [[ ${cur_tablets} -lt ${num_tablets} ]]; then
		fail "Timed out after ${wait_secs} seconds waiting for tablets to come up in ${keyspace}/${shard}"
	fi
}

# Wait for a primary tablet to be elected and become healthy and serving
# in the given keyspace/shard. Example:
#  wait_for_healthy_shard commerce 0
function wait_for_healthy_shard_primary() {
	if [[ -z ${1} || -z ${2} ]]; then
		fail "A keyspace and shard must be specified when waiting for the shard's primary to be healthy"
	fi
	local keyspace=${1}
	local shard=${2}
	local unhealthy_indicator='"primary_alias": null'
	local wait_secs=180

	for _ in $(seq 1 ${wait_secs}); do
		if ! vtctldclient GetShard "${keyspace}/${shard}" | grep -qi "${unhealthy_indicator}"; then
			break
		fi
		sleep 1
	done;

	if vtctldclient GetShard "${keyspace}/${shard}" | grep -qi "${unhealthy_indicator}"; then
		fail "Timed out after ${wait_secs} seconds waiting for a primary tablet to be elected and become healthy in ${keyspace}/${shard}"
	fi
}

# Wait for a primary tablet to be writeable, ie read_only=0 and super_read_only=0
function wait_for_writeable_shard_primary() {
	if [[ -z ${1} || -z ${2} ]]; then
		fail "A keyspace and shard must be specified when waiting for the shard's primary to be healthy"
	fi
	local keyspace=${1}
	local shard=${2}
	local wait_secs=30

	PRIMARY_TABLET="$(vtctldclient GetTablets --keyspace "$keyspace" --shard "$shard" | grep -w "primary" | awk '{print $1}')"
	if [ -z "$PRIMARY_TABLET" ] ; then
		fail "Cannot determine primary tablet for keyspace/shard $keyspace/$shard"
	fi

	for _ in $(seq 1 ${wait_secs}); do
		if vtctldclient GetFullStatus "$PRIMARY_TABLET" | grep "super_read_only" | grep --quiet "false" ; then
			break
		fi
		sleep 1
	done
	if vtctldclient GetFullStatus "$PRIMARY_TABLET" | grep "super_read_only" | grep --quiet "true" ; then
		fail "Timed out after ${wait_secs} seconds waiting for a primary tablet $PRIMARY_TABLET to be writeable in ${keyspace}/${shard}"
	fi
}

# Wait for the shard primary tablet's VReplication engine to open.
# There is currently no API call or client command that can be specifically used
# to check the VReplication engine's status (no vars in /debug/vars etc. either).
# So we use the Workflow listall client command as the method to check for that
# as it will return an error when the engine is closed -- even when there are
# no workflows.
function wait_for_shard_vreplication_engine() {
        if [[ -z ${1} || -z ${2} ]]; then
                fail "A keyspace and shard must be specified when waiting for the shard primary tablet's VReplication engine to open"
        fi
        local keyspace=${1}
        local shard=${2}
        local wait_secs=90

        for _ in $(seq 1 ${wait_secs}); do
                if vtctldclient workflow --keyspace "${keyspace}" list &>/dev/null; then
                        break
                fi
                sleep 1
        done;

        if ! vtctldclient workflow --keyspace "${keyspace}" list &>/dev/null; then
                fail "Timed out after ${wait_secs} seconds waiting for the primary tablet's VReplication engine to open in ${keyspace}/${shard}"
        fi
}

# Wait for a specified number of the keyspace/shard's tablets to show up
# in the topology server (3 is the default if no value is specified) and
# then wait for one of the tablets to be promoted to primary and become
# healthy and serving. Lastly, wait for the new primary tablet's
# VReplication engine to fully open. Example:
#  wait_for_healthy_shard commerce 0
function wait_for_healthy_shard() {
	if [[ -z ${1} || -z ${2} ]]; then
		fail "A keyspace and shard must be specified when waiting for tablets to come up"
	fi
	local keyspace=${1}
	local shard=${2}
	local num_tablets=${3:-3}
  echo "waiting for ${num_tablets} tablets in ${keyspace}/${shard} to come up and for the primary tablet to be healthy and writeable"

	wait_for_shard_tablets "${keyspace}" "${shard}" "${num_tablets}"
	wait_for_healthy_shard_primary "${keyspace}" "${shard}"
	wait_for_writeable_shard_primary "${keyspace}" "${shard}"
	wait_for_shard_vreplication_engine "${keyspace}" "${shard}"
}

# Wait for a workflow to reach the running state. Example:
#  wait_for_workflow_running customer customer2customer
function wait_for_workflow_running() {
    if [[ -z ${1} || -z ${2} ]]; then
        fail "A keyspace and workflow must be specified when waiting for a workflow to reach the running state"
    fi

    local keyspace=${1}
    local workflow=${2}
    local wait_secs=90
    local result=""

    echo "Waiting for the ${workflow} workflow in the ${keyspace} keyspace to finish the copy phase..." 

    for _ in $(seq 1 ${wait_secs}); do
        result=$(vtctldclient Workflow --keyspace="${keyspace}" show --workflow="${workflow}" 2>/dev/null | grep "Copy phase completed")
        if [[ ${result} != "" ]]; then
            break
        fi
        sleep 1
    done;

    if [[ ${result} == "" ]]; then
        fail "Timed out after ${wait_secs} seconds waiting for the ${workflow} workflow in the ${keyspace} keyspace to reach the running state"
    fi

    echo "The ${workflow} workflow in the ${keyspace} keyspace is now running. $(sed -rn 's/.*"(Copy phase.*)".*/\1/p' <<< "${result}")."
}

# Stop the specified binary name using the provided PID file.
# Example:
#  stop_process "vtadmin-web" "$VTDATAROOT/tmp/vtadmin-web.pid"
function stop_process() {
	if [[ -z ${1} || -z ${2} ]]; then
		fail "A binary name and PID file must be specified when attempting to shutdown a process"
	fi

	local binary_name="${1}"
	local pidfile="${2}"
	local pid=""
	local wait_secs=10

	if [[ -e "${pidfile}" ]]; then
		pid=$(cat "${pidfile}")
		if ps -p "${pid}" > /dev/null; then
			echo "Stopping ${binary_name}..."
			kill "${pid}"

			# Wait for the process to terminate
			for _ in $(seq 1 ${wait_secs}); do
				if ! ps -p "${pid}" > /dev/null; then
					break
				fi
				sleep 1
			done
			if ps -p "${pid}" > /dev/null; then
				echo "Warning: Timed out after ${wait_secs} seconds waiting for ${binary_name} (PID: ${pid}) to terminate"
			else
				echo "${binary_name} stopped successfully"
				# Remove the PID file after successful stop
				if [[ -e "${pidfile}" ]]; then
					rm "${pidfile}"
				fi
			fi
		else
			echo "${binary_name} is not running (stale PID file exists)"
			rm "${pidfile}"
		fi
	else
		echo "${binary_name} is not running (no PID file)"
	fi
}

# Print error message and exit with error code.
function fail() {
	echo "ERROR: ${1}"
	exit 1
}

function output() {
  echo -e "$@"
}

# Configuration file utilities
# Default configuration file location
CONFIG_FILE=${CONFIG_FILE:-"/etc/vitess.yaml"}
FALLBACK_CONFIG_FILE="./vitess.yaml"

# Function to read YAML configuration
read_yaml_config() {
  if [ ! -f "$CONFIG_FILE" ]; then
    # Try fallback config if default doesn't exist
    if [ "$CONFIG_FILE" = "/etc/vitess.yaml" ] && [ -f "$FALLBACK_CONFIG_FILE" ]; then
      # Use stderr for messages to avoid affecting function output
      echo "Configuration file $CONFIG_FILE not found, using fallback $FALLBACK_CONFIG_FILE" >&2
      CONFIG_FILE="$FALLBACK_CONFIG_FILE"
    else
      echo "Warning: Configuration file $CONFIG_FILE not found, using default values" >&2
      return 1
    fi
  fi
  return 0
}

# Function to get configuration value from YAML
# Usage: get_config_value <section> <key> <default_value>
get_config_value() {
  local section=$1
  local key=$2
  local default_value=$3
  
  if ! read_yaml_config; then
    echo "$default_value"
    return
  fi
  
  # Use yq to extract the value
  local value=$(yq eval ".$section.$key" "$CONFIG_FILE" 2>/dev/null)
  if [ -z "$value" ] || [ "$value" = "null" ]; then
    echo "$default_value"
  else
    echo "$value"
  fi
}

# Function to get tablet UIDs from configuration
# Usage: get_tablet_uids
get_tablet_uids() {
  if ! read_yaml_config; then
    return 1
  fi
  
  # Use yq to extract tablet UIDs
  TABLET_UIDS=$(yq eval '.tablets[].uid' "$CONFIG_FILE" 2>/dev/null)
  if [ -z "$TABLET_UIDS" ] || [ "$TABLET_UIDS" = "null" ]; then
    echo "No tablet UIDs found in configuration file" >&2
    return 1
  fi
  
  return 0
}

# Function to get tablet configuration by UID
# Usage: get_tablet_config <uid> <key> <default_value>
get_tablet_config() {
  local uid=$1
  local key=$2
  local default_value=$3
  
  if ! read_yaml_config; then
    echo "$default_value"
    return
  fi
  
  # Use yq to extract the value for the specific tablet
  local value=$(yq eval ".tablets[] | select(.uid == $uid) | .$key" "$CONFIG_FILE" 2>/dev/null)
  if [ -z "$value" ] || [ "$value" = "null" ]; then
    echo "$default_value"
  else
    echo "$value"
  fi
}

# Get VTDATAROOT from config and expand tilde if present
vtdataroot_raw=$(get_config_value "global" "vtdataroot" "~/vtdataroot")
VTDATAROOT="${vtdataroot_raw/#\~/$HOME}"
hostname=$(get_config_value "global" "hostname" "localhost")
mkdir -p "$VTDATAROOT/tmp" || { echo "Failed to create tmp directory"; exit 1; }
