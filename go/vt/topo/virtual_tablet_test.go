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
	"vitess.io/vitess/go/vt/topo/topoproto"
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
		Keyspace: "virtual_ks",
		Shard:    "0",
		Type:     topodatapb.TabletType_VIRTUAL,
		Tags: map[string]string{
			"physical_tablet":  topoproto.TabletAliasString(physicalTablet.Alias),
			"virtual_keyspace": "virtual_ks",
			"schema_name":      "test_schema",
		},
	}

	// Test helper functions
	assert.True(t, IsVirtualTablet(virtualTablet))
	assert.False(t, IsVirtualTablet(physicalTablet))

	// Test extracting metadata
	physicalAlias, err := GetPhysicalTabletAlias(virtualTablet)
	require.NoError(t, err)
	assert.Equal(t, physicalTablet.Alias, physicalAlias)

	virtualKeyspace, err := GetVirtualKeyspaceName(virtualTablet)
	require.NoError(t, err)
	assert.Equal(t, "virtual_ks", virtualKeyspace)

	schemaName, err := GetSchemaName(virtualTablet)
	require.NoError(t, err)
	assert.Equal(t, "test_schema", schemaName)

	// Test error cases
	_, err = GetPhysicalTabletAlias(physicalTablet)
	assert.Error(t, err)

	_, err = GetVirtualKeyspaceName(physicalTablet)
	assert.Error(t, err)

	_, err = GetSchemaName(physicalTablet)
	assert.Error(t, err)
}

func TestCreateVirtualKeyspaceShard(t *testing.T) {
	// This test would normally test CreateVirtualKeyspaceShard but we can't import memorytopo
	// in the same package due to import cycles. The functionality is tested in integration tests.
	t.Skip("Skipping due to import cycle - tested in integration tests")
}

func TestGetOrCreateVirtualKeyspaceShard(t *testing.T) {
	// This test would normally test GetOrCreateVirtualKeyspaceShard but we can't import memorytopo
	// in the same package due to import cycles. The functionality is tested in integration tests.
	t.Skip("Skipping due to import cycle - tested in integration tests")
}

func TestCreateVirtualKeyspaceShardErrors(t *testing.T) {
	// This test would normally test CreateVirtualKeyspaceShard error cases but we can't import memorytopo
	// in the same package due to import cycles. The functionality is tested in integration tests.
	t.Skip("Skipping due to import cycle - tested in integration tests")
}
