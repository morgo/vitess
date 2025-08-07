/*
Copyright 2025 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package topo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
)

func TestVirtualTabletType(t *testing.T) {
	// Test tablet type helper functions
	assert.True(t, IsInServingGraph(topodatapb.TabletType_VIRTUAL))
	assert.False(t, IsRunningQueryService(topodatapb.TabletType_VIRTUAL))
	assert.False(t, IsRunningUpdateStream(topodatapb.TabletType_VIRTUAL))
	assert.False(t, IsReplicaType(topodatapb.TabletType_VIRTUAL))
	assert.True(t, IsVirtualType(topodatapb.TabletType_VIRTUAL))
}

func TestVirtualTabletHelpers(t *testing.T) {
	// Create a physical tablet
	physicalTablet := &topodatapb.Tablet{
		Alias: &topodatapb.TabletAlias{
			Cell: "cell1",
			Uid:  100,
		},
		Keyspace: "physical_ks",
		Shard:    "0",
		Type:     topodatapb.TabletType_PRIMARY,
		Hostname: "physical-host",
		PortMap: map[string]int32{
			"vt": 15100,
		},
	}

	// Create a virtual tablet that references the physical tablet
	virtualTablet := &topodatapb.Tablet{
		Alias: &topodatapb.TabletAlias{
			Cell: "cell1",
			Uid:  200,
		},
		Keyspace:       "virtual_ks",
		Shard:          "0",
		Type:           topodatapb.TabletType_VIRTUAL,
		DbNameOverride: "test_schema",
		Tags: map[string]string{
			"physical_keyspace": physicalTablet.Keyspace,
			"physical_shard":    physicalTablet.Shard,
		},
	}

	// Test helper functions
	assert.True(t, IsVirtualTablet(virtualTablet))
	assert.False(t, IsVirtualTablet(physicalTablet))

	virtualKeyspace, err := GetVirtualKeyspaceName(virtualTablet)
	require.NoError(t, err)
	assert.Equal(t, "virtual_ks", virtualKeyspace)

	// Test accessing DbNameOverride directly
	assert.Equal(t, "test_schema", virtualTablet.DbNameOverride)

	// Test error cases
	_, err = GetVirtualKeyspaceName(physicalTablet)
	assert.Error(t, err)
}
