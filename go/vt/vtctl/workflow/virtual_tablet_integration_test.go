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

package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"vitess.io/vitess/go/vt/topo"
	"vitess.io/vitess/go/vt/topo/topoproto"

	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	vschemapb "vitess.io/vitess/go/vt/proto/vschema"
)

// TestVirtualTabletIntegration tests the complete VIRTUAL tablet integration
// This test validates that virtual keyspaces work seamlessly with VIRTUAL tablets
func TestVirtualTabletIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Define the physical keyspaces that will host the virtual keyspaces
	mainPhysicalKeyspace := &testKeyspace{"main", []string{"0"}}
	main2PhysicalKeyspace := &testKeyspace{"main2", []string{"0"}}

	// Create test environment with the physical keyspaces
	env := newTestEnv(t, ctx, defaultCellName, mainPhysicalKeyspace, main2PhysicalKeyspace)
	defer env.close()

	// Test 1: Create virtual keyspaces with VIRTUAL tablets
	t.Run("CreateVirtualKeyspacesWithVirtualTablets", func(t *testing.T) {
		// Create virtual keyspaces
		err := env.ts.CreateVirtualKeyspace(ctx, "commerce", "main", "vt_commerce_0")
		require.NoError(t, err)
		err = env.ts.CreateVirtualKeyspace(ctx, "customer", "main2", "vt_customer_0")
		require.NoError(t, err)

		// Create virtual keyspace shards with VIRTUAL tablets
		err = env.ts.CreateVirtualKeyspaceShard(ctx, "commerce", "main", "0", "vt_commerce_0")
		require.NoError(t, err)
		err = env.ts.CreateVirtualKeyspaceShard(ctx, "customer", "main2", "0", "vt_customer_0")
		require.NoError(t, err)

		// Verify virtual keyspaces were created
		commerceKS, err := env.ts.GetVirtualKeyspace(ctx, "commerce")
		require.NoError(t, err)
		require.Equal(t, "commerce", commerceKS.VirtualKeyspaceName())
		require.Equal(t, "main", commerceKS.PhysicalKeyspace)
		require.Equal(t, "vt_commerce_0", commerceKS.SchemaName)

		customerKS, err := env.ts.GetVirtualKeyspace(ctx, "customer")
		require.NoError(t, err)
		require.Equal(t, "customer", customerKS.VirtualKeyspaceName())
		require.Equal(t, "main2", customerKS.PhysicalKeyspace)
		require.Equal(t, "vt_customer_0", customerKS.SchemaName)

		t.Logf("✅ Virtual keyspaces created successfully with proper metadata")
	})

	// Test 2: Verify VIRTUAL tablets were created
	t.Run("VerifyVirtualTablets", func(t *testing.T) {
		// Get shard info for virtual keyspaces
		commerceShard, err := env.ts.GetShard(ctx, "commerce", "0")
		require.NoError(t, err)
		customerShard, err := env.ts.GetShard(ctx, "customer", "0")
		require.NoError(t, err)

		// Verify shards have VIRTUAL tablets
		require.NotNil(t, commerceShard.PrimaryAlias)
		require.NotNil(t, customerShard.PrimaryAlias)

		// Get the VIRTUAL tablets
		commerceTablet, err := env.ts.GetTablet(ctx, commerceShard.PrimaryAlias)
		require.NoError(t, err)
		customerTablet, err := env.ts.GetTablet(ctx, customerShard.PrimaryAlias)
		require.NoError(t, err)

		// Verify tablets are VIRTUAL type
		require.Equal(t, topodatapb.TabletType_VIRTUAL, commerceTablet.Type)
		require.Equal(t, topodatapb.TabletType_VIRTUAL, customerTablet.Type)

		// Verify VIRTUAL tablets have correct metadata
		require.Equal(t, "commerce", commerceTablet.Keyspace)
		require.Equal(t, "0", commerceTablet.Shard)
		require.Equal(t, "customer", customerTablet.Keyspace)
		require.Equal(t, "0", customerTablet.Shard)

		// Verify VIRTUAL tablets have physical tablet references
		require.NotEmpty(t, commerceTablet.Tags["physical_tablet"])
		require.NotEmpty(t, customerTablet.Tags["physical_tablet"])
		require.Equal(t, "commerce", commerceTablet.Tags["virtual_keyspace"])
		require.Equal(t, "customer", customerTablet.Tags["virtual_keyspace"])
		require.Equal(t, "vt_commerce_0", commerceTablet.Tags["schema_name"])
		require.Equal(t, "vt_customer_0", customerTablet.Tags["schema_name"])

		t.Logf("✅ VIRTUAL tablets created successfully with proper metadata")
	})

	// Test 3: Test VIRTUAL tablet resolution
	t.Run("TestVirtualTabletResolution", func(t *testing.T) {
		// Get the VIRTUAL tablet
		commerceShard, err := env.ts.GetShard(ctx, "commerce", "0")
		require.NoError(t, err)
		commerceTablet, err := env.ts.GetTablet(ctx, commerceShard.PrimaryAlias)
		require.NoError(t, err)

		// Test tablet resolution
		physicalTabletAlias := commerceTablet.Tags["physical_tablet"]
		require.NotEmpty(t, physicalTabletAlias)

		// Parse the physical tablet alias
		physicalAlias, err := topoproto.ParseTabletAlias(physicalTabletAlias)
		require.NoError(t, err)

		// Get the physical tablet
		physicalTablet, err := env.ts.GetTablet(ctx, physicalAlias)
		require.NoError(t, err)

		// Verify the physical tablet is in the correct physical keyspace
		require.Equal(t, "main", physicalTablet.Keyspace)
		require.Equal(t, "0", physicalTablet.Shard)
		require.Equal(t, topodatapb.TabletType_PRIMARY, physicalTablet.Type)

		t.Logf("✅ VIRTUAL tablet resolution working correctly")
	})

	// Test 4: Test virtual keyspace listing
	t.Run("TestVirtualKeyspaceListing", func(t *testing.T) {
		// List virtual keyspaces
		virtualKeyspaces, err := env.ts.ListVirtualKeyspaces(ctx)
		require.NoError(t, err)
		require.Contains(t, virtualKeyspaces, "commerce")
		require.Contains(t, virtualKeyspaces, "customer")

		// List all keyspaces (should include both physical and virtual)
		allKeyspaces, err := env.ts.GetKeyspaces(ctx)
		require.NoError(t, err)
		require.Contains(t, allKeyspaces, "main")
		require.Contains(t, allKeyspaces, "main2")
		require.Contains(t, allKeyspaces, "commerce")
		require.Contains(t, allKeyspaces, "customer")

		t.Logf("✅ Virtual keyspace listing working correctly")
	})

	// Test 5: Test VSchema integration
	t.Run("TestVSchemaIntegration", func(t *testing.T) {
		// Create VSchemas for virtual keyspaces
		commerceVSchema := &vschemapb.Keyspace{
			Sharded: false,
			Tables: map[string]*vschemapb.Table{
				"orders": {
					Type: "sequence",
				},
			},
		}
		err := env.ts.SaveVSchema(ctx, &topo.KeyspaceVSchemaInfo{
			Name:     "commerce",
			Keyspace: commerceVSchema,
		})
		require.NoError(t, err)

		customerVSchema := &vschemapb.Keyspace{
			Sharded: false,
			Tables: map[string]*vschemapb.Table{
				"customers": {
					Type: "sequence",
				},
			},
		}
		err = env.ts.SaveVSchema(ctx, &topo.KeyspaceVSchemaInfo{
			Name:     "customer",
			Keyspace: customerVSchema,
		})
		require.NoError(t, err)

		// Verify VSchemas were saved correctly
		savedCommerceVSchema, err := env.ts.GetVSchema(ctx, "commerce")
		require.NoError(t, err)
		require.NotNil(t, savedCommerceVSchema.Keyspace)
		require.Contains(t, savedCommerceVSchema.Keyspace.Tables, "orders")

		savedCustomerVSchema, err := env.ts.GetVSchema(ctx, "customer")
		require.NoError(t, err)
		require.NotNil(t, savedCustomerVSchema.Keyspace)
		require.Contains(t, savedCustomerVSchema.Keyspace.Tables, "customers")

		t.Logf("✅ VSchema integration working correctly")
	})

	// Test 6: Test virtual keyspace deletion
	t.Run("TestVirtualKeyspaceDeletion", func(t *testing.T) {
		// Delete a virtual keyspace
		err := env.ts.DeleteVirtualKeyspace(ctx, "commerce")
		require.NoError(t, err)

		// Verify it was deleted
		_, err = env.ts.GetVirtualKeyspace(ctx, "commerce")
		require.Error(t, err)

		// Verify the other virtual keyspace still exists
		customerKS, err := env.ts.GetVirtualKeyspace(ctx, "customer")
		require.NoError(t, err)
		require.Equal(t, "customer", customerKS.VirtualKeyspaceName())

		// Verify physical keyspaces are unaffected
		mainKS, err := env.ts.GetKeyspace(ctx, "main")
		require.NoError(t, err)
		require.False(t, mainKS.IsVirtual)

		t.Logf("✅ Virtual keyspace deletion working correctly")
	})

	t.Logf("✅ VIRTUAL tablet integration test completed successfully!")
	t.Logf("   - Virtual keyspaces created with VIRTUAL tablets")
	t.Logf("   - VIRTUAL tablets properly reference physical tablets")
	t.Logf("   - Virtual keyspace resolution working correctly")
	t.Logf("   - VSchema integration functional")
	t.Logf("   - Virtual keyspace lifecycle management working")
}

// TestVirtualTabletTypeHelpers tests the tablet type helper functions
func TestVirtualTabletTypeHelpers(t *testing.T) {
	// Test tablet type constants
	require.Equal(t, topodatapb.TabletType_VIRTUAL, topodatapb.TabletType(9))

	// Test that VIRTUAL is a valid tablet type
	require.True(t, topodatapb.TabletType_VIRTUAL.String() == "VIRTUAL")

	t.Logf("✅ VIRTUAL tablet type constants working correctly")
}

// TestVirtualTabletResolver tests the virtual tablet resolver
func TestVirtualTabletResolver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create test environment
	mainPhysicalKeyspace := &testKeyspace{"main", []string{"0"}}
	dummyKeyspace := &testKeyspace{"dummy", []string{"0"}}
	env := newTestEnv(t, ctx, defaultCellName, mainPhysicalKeyspace, dummyKeyspace)
	defer env.close()

	// Create virtual keyspace with VIRTUAL tablet
	err := env.ts.CreateVirtualKeyspace(ctx, "commerce", "main", "vt_commerce_0")
	require.NoError(t, err)
	err = env.ts.CreateVirtualKeyspaceShard(ctx, "commerce", "main", "0", "vt_commerce_0")
	require.NoError(t, err)

	// Get the VIRTUAL tablet
	commerceShard, err := env.ts.GetShard(ctx, "commerce", "0")
	require.NoError(t, err)
	virtualTablet, err := env.ts.GetTablet(ctx, commerceShard.PrimaryAlias)
	require.NoError(t, err)

	// Test tablet resolution
	physicalTabletAlias := virtualTablet.Tags["physical_tablet"]
	require.NotEmpty(t, physicalTabletAlias)

	// Parse and get physical tablet
	physicalAlias, err := topoproto.ParseTabletAlias(physicalTabletAlias)
	require.NoError(t, err)
	physicalTablet, err := env.ts.GetTablet(ctx, physicalAlias)
	require.NoError(t, err)

	// Verify resolution
	require.Equal(t, "main", physicalTablet.Keyspace)
	require.Equal(t, topodatapb.TabletType_PRIMARY, physicalTablet.Type)

	t.Logf("✅ Virtual tablet resolver working correctly")
}
