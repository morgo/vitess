/*
Copyright 2024 The Vitess Authors.

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

package vreplication

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vitess.io/vitess/go/mysql/capabilities"
	"vitess.io/vitess/go/sqltypes"
	"vitess.io/vitess/go/vt/binlog/binlogplayer"
	"vitess.io/vitess/go/vt/discovery"
	"vitess.io/vitess/go/vt/mysqlctl"
	"vitess.io/vitess/go/vt/topo/memorytopo"

	binlogdatapb "vitess.io/vitess/go/vt/proto/binlogdata"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
)

// TestMoveTablesWorkflowSchemaSelection tests the core schema selection logic
// This test demonstrates how MoveTables workflows should select the correct database
func TestMoveTablesWorkflowSchemaSelection(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Create ONLY the main keyspace - this is the physical keyspace
	// Virtual keyspaces (commerce, customer) don't have their own keyspaces/shards
	err := ts.CreateKeyspace(ctx, "main", &topodatapb.Keyspace{})
	require.NoError(t, err)

	// Create shard for the main keyspace only
	err = ts.CreateShard(ctx, "main", "0")
	require.NoError(t, err)

	dbClientFactory := func() binlogplayer.DBClient {
		return binlogplayer.NewMockDBClient(t)
	}

	mysqld := &mysqlctl.FakeMysqlDaemon{}

	// Create VReplication engine with main as the physical keyspace
	// This simulates a tablet that has main as its primary keyspace
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	// Initialize with main as physical keyspace
	err = vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Add virtual keyspaces - these are schemas within the main keyspace, not separate keyspaces
	// For virtual keyspaces, the database name follows the pattern: vt_{keyspace}_{shard}
	err = vre.AddVirtualKeyspace("commerce", "vt_commerce_0")
	require.NoError(t, err)
	err = vre.AddVirtualKeyspace("customer", "vt_customer_0")
	require.NoError(t, err)

	// Test different MoveTables workflow scenarios
	testCases := []struct {
		name                      string
		targetKeyspace            string
		dbName                    string
		expectedSchema            string
		shouldUseDifferentFactory bool
	}{
		{
			name:                      "MoveTables targeting main keyspace (physical)",
			targetKeyspace:            "main",
			dbName:                    "vt_main",
			expectedSchema:            "vt_main",
			shouldUseDifferentFactory: false, // Should use default factory
		},
		{
			name:                      "MoveTables targeting commerce keyspace (virtual)",
			targetKeyspace:            "commerce",
			dbName:                    "vt_commerce_0", // Correct naming: vt_{keyspace}_{shard}
			expectedSchema:            "vt_commerce_0",
			shouldUseDifferentFactory: true, // Should use schema-specific factory
		},
		{
			name:                      "MoveTables targeting customer keyspace (virtual)",
			targetKeyspace:            "customer",
			dbName:                    "vt_customer_0", // Correct naming: vt_{keyspace}_{shard}
			expectedSchema:            "vt_customer_0",
			shouldUseDifferentFactory: true, // Should use schema-specific factory
		},
		{
			name:                      "MoveTables with unknown keyspace (fallback to legacy)",
			targetKeyspace:            "unknown",
			dbName:                    "vt_unknown",
			expectedSchema:            "vt_main", // Should fall back to physical keyspace
			shouldUseDifferentFactory: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create controller parameters simulating a MoveTables workflow
			params := map[string]string{
				"id":              "1",
				"workflow":        "test_workflow_" + tc.targetKeyspace,
				"source":          `keyspace:"main" shard:"0" filter:{rules:{match:"test_table" filter:"select * from test_table"}}`,
				"state":           "Stopped",
				"target_keyspace": tc.targetKeyspace,
				"db_name":         tc.dbName,
				"options":         "{}",
			}

			controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
			require.NoError(t, err)
			defer controller.Stop()

			// Verify the controller is configured correctly
			assert.Equal(t, tc.targetKeyspace, controller.targetKeyspace, "Controller should target %s keyspace", tc.targetKeyspace)
			assert.Equal(t, tc.expectedSchema, controller.targetSchema, "Controller should use %s schema", tc.expectedSchema)

			// Test the schema-specific factory
			factory := controller.getSchemaSpecificDBClientFactory()
			assert.NotNil(t, factory)

			// Check if the factory is different from the default factory
			defaultFactory := controller.dbClientFactory
			if tc.shouldUseDifferentFactory {
				assert.NotEqual(t, fmt.Sprintf("%p", factory), fmt.Sprintf("%p", defaultFactory),
					"Schema-specific factory should be different from default factory for virtual keyspace")
			} else {
				assert.Equal(t, fmt.Sprintf("%p", factory), fmt.Sprintf("%p", defaultFactory),
					"Schema-specific factory should be same as default factory for physical keyspace")
			}

			// Test that the factory creates a client
			client := factory()
			assert.NotNil(t, client)

			// Log the configuration for debugging
			t.Logf("MoveTables workflow configuration:")
			t.Logf("  Target keyspace: %s", controller.targetKeyspace)
			t.Logf("  Target schema: %s", controller.targetSchema)
			t.Logf("  Source keyspace: %s", controller.source.Keyspace)
			t.Logf("  Source shard: %s", controller.source.Shard)
		})
	}
}

// TestMoveTablesWorkflowBugScenario tests the specific bug scenario
// This test simulates the exact command: vtctldclient MoveTables --workflow commerce2customer --target-keyspace customer create --source-keyspace commerce --tables "customer,corder"
func TestMoveTablesWorkflowBugScenario(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Create keyspaces - this simulates the exact scenario from the bug report
	err := ts.CreateKeyspace(ctx, "main", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateKeyspace(ctx, "commerce", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateKeyspace(ctx, "customer", &topodatapb.Keyspace{})
	require.NoError(t, err)

	// Create shards
	err = ts.CreateShard(ctx, "main", "0")
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "commerce", "0")
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "customer", "0")
	require.NoError(t, err)

	dbClientFactory := func() binlogplayer.DBClient {
		return binlogplayer.NewMockDBClient(t)
	}

	mysqld := &mysqlctl.FakeMysqlDaemon{}

	// Create VReplication engine - this simulates a tablet that:
	// 1. Has "main" as its physical keyspace (uses vt_main database)
	// 2. But needs to handle MoveTables workflows targeting "customer" keyspace (should use vt_customer database)
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	// Initialize with main as physical keyspace - this is the typical setup
	err = vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Add customer as virtual keyspace - this is what enables multi-keyspace support
	// For virtual keyspaces, the database name follows the pattern: vt_{keyspace}_{shard}
	err = vre.AddVirtualKeyspace("customer", "vt_customer_0")
	require.NoError(t, err)

	// Simulate the exact MoveTables workflow creation from the vtctldclient command
	// vtctldclient MoveTables --workflow commerce2customer --target-keyspace customer create --source-keyspace commerce --tables "customer,corder"
	params := map[string]string{
		"id":              "1",
		"workflow":        "commerce2customer",
		"source":          `keyspace:"commerce" shard:"0" filter:{rules:{match:"customer" filter:"select * from customer"} rules:{match:"corder" filter:"select * from corder"}}`,
		"state":           "Stopped",
		"target_keyspace": "customer",      // This is the key - targeting customer keyspace
		"db_name":         "vt_customer_0", // This should make it use vt_customer_0 database (correct naming)
		"options":         "{}",
	}

	controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
	require.NoError(t, err)
	defer controller.Stop()

	// These are the critical assertions that demonstrate the bug fix
	assert.Equal(t, "customer", controller.targetKeyspace, "Controller should target customer keyspace")
	assert.Equal(t, "vt_customer_0", controller.targetSchema, "Controller should use vt_customer_0 database, NOT vt_main")

	// Verify that the source is correctly configured
	assert.Equal(t, "commerce", controller.source.Keyspace, "Source should be commerce keyspace")
	assert.NotNil(t, controller.source.Filter, "Source should have filter")
	assert.Len(t, controller.source.Filter.Rules, 2, "Should have 2 table rules")

	// Verify table filtering
	tableNames := make([]string, 0, len(controller.source.Filter.Rules))
	for _, rule := range controller.source.Filter.Rules {
		tableNames = append(tableNames, rule.Match)
	}
	assert.Contains(t, tableNames, "customer", "Should filter customer table")
	assert.Contains(t, tableNames, "corder", "Should filter corder table")

	// Test the schema-specific DB client factory - this is where the bug would manifest
	factory := controller.getSchemaSpecificDBClientFactory()
	assert.NotNil(t, factory, "Should have schema-specific factory")

	// For virtual keyspace, factory should be different from default
	defaultFactory := controller.dbClientFactory
	assert.NotEqual(t, fmt.Sprintf("%p", factory), fmt.Sprintf("%p", defaultFactory),
		"Schema-specific factory should be different from default factory for virtual keyspace")

	// Test that the factory creates a client
	client := factory()
	assert.NotNil(t, client, "Factory should create a valid client")

	// Log the configuration for debugging
	t.Logf("MoveTables workflow configuration:")
	t.Logf("  Workflow: %s", controller.workflow)
	t.Logf("  Source keyspace: %s", controller.source.Keyspace)
	t.Logf("  Target keyspace: %s", controller.targetKeyspace)
	t.Logf("  Target schema: %s", controller.targetSchema)
	t.Logf("  Physical keyspace: %s", vre.GetPhysicalKeyspace())

	// The key insight: In the bug scenario, vreplication would try to use vt_main
	// instead of vt_customer_0. Our implementation should prevent this.
	assert.NotEqual(t, "vt_main", controller.targetSchema,
		"BUG CHECK: Controller should NOT use vt_main database for customer keyspace workflow")
}

// TestVirtualKeyspaceEngineSchemaMapping tests the engine's virtual keyspace functionality
func TestVirtualKeyspaceEngineSchemaMapping(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	dbClientFactory := func() binlogplayer.DBClient {
		return binlogplayer.NewMockDBClient(t)
	}

	mysqld := &mysqlctl.FakeMysqlDaemon{}
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	// Initialize with main as physical keyspace
	err := vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Test the schema mapping functionality
	testCases := []struct {
		name           string
		keyspace       string
		expectedSchema string
		shouldError    bool
	}{
		{
			name:           "Physical keyspace",
			keyspace:       "main",
			expectedSchema: "vt_main",
			shouldError:    false,
		},
		{
			name:           "Nonexistent keyspace",
			keyspace:       "nonexistent",
			expectedSchema: "",
			shouldError:    true,
		},
	}

	// Test before adding virtual keyspaces
	for _, tc := range testCases {
		t.Run("Before_"+tc.name, func(t *testing.T) {
			schema, err := vre.GetSchemaForKeyspace(tc.keyspace)
			if tc.shouldError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "not found")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedSchema, schema)
			}
		})
	}

	// Add virtual keyspaces
	err = vre.AddVirtualKeyspace("customer", "vt_customer")
	require.NoError(t, err)
	err = vre.AddVirtualKeyspace("commerce", "vt_commerce")
	require.NoError(t, err)

	// Update test cases to include virtual keyspaces
	testCases = append(testCases, []struct {
		name           string
		keyspace       string
		expectedSchema string
		shouldError    bool
	}{
		{
			name:           "Virtual keyspace customer",
			keyspace:       "customer",
			expectedSchema: "vt_customer",
			shouldError:    false,
		},
		{
			name:           "Virtual keyspace commerce",
			keyspace:       "commerce",
			expectedSchema: "vt_commerce",
			shouldError:    false,
		},
	}...)

	// Test after adding virtual keyspaces
	for _, tc := range testCases {
		t.Run("After_"+tc.name, func(t *testing.T) {
			schema, err := vre.GetSchemaForKeyspace(tc.keyspace)
			if tc.shouldError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "not found")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedSchema, schema)
			}
		})
	}

	// Test schema listing
	schemas := vre.ListManagedSchemas()
	assert.Len(t, schemas, 3)
	assert.Contains(t, schemas, "vt_main")
	assert.Contains(t, schemas, "vt_customer")
	assert.Contains(t, schemas, "vt_commerce")
}

func TestMoveTablesVirtualKeyspaceBugReproduction(t *testing.T) {
	ctx := context.Background()

	// Create test environment
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Create mock MySQL daemon
	mysqld := &mysqlctl.FakeMysqlDaemon{}

	// Mock database clients for testing
	dbClientFactory := func() binlogplayer.DBClient {
		return &testFakeBinlogClient{
			dbName:  "vt_main", // Start with physical keyspace database
			queries: make([]string, 0),
		}
	}

	// Create VReplication engine
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	// Initialize with physical keyspace
	err := vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Add virtual keyspaces
	err = vre.AddVirtualKeyspace("commerce", "vt_commerce_0")
	require.NoError(t, err)

	err = vre.AddVirtualKeyspace("customer", "vt_customer_0")
	require.NoError(t, err)

	// Create topology entries only for physical keyspace
	err = ts.CreateKeyspace(ctx, "main", &topodatapb.Keyspace{})
	require.NoError(t, err)

	err = ts.CreateShard(ctx, "main", "0")
	require.NoError(t, err)

	// Create a tablet for the physical keyspace
	tablet := &topodatapb.Tablet{
		Alias:    &topodatapb.TabletAlias{Cell: "cell1", Uid: 100},
		Keyspace: "main",
		Shard:    "0",
		Type:     topodatapb.TabletType_PRIMARY,
		PortMap:  map[string]int32{"vt": 8080},
	}
	err = ts.CreateTablet(ctx, tablet)
	require.NoError(t, err)

	// Open the engine
	vre.Open(ctx)
	defer vre.Close()

	// Create a MoveTables workflow from commerce (virtual) to customer (virtual)
	sourceSpec := `keyspace:"commerce" shard:"0" filter:{rules:{match:"customer" filter:"select * from customer"} rules:{match:"corder" filter:"select * from corder"}}`

	// Simulate the MoveTables workflow creation
	params := map[string]string{
		"id":                    "1",
		"workflow":              "commerce2customer",
		"source":                sourceSpec,
		"pos":                   "",
		"stop_pos":              "",
		"max_tps":               "9999",
		"max_replication_lag":   "9999",
		"cell":                  "cell1",
		"tablet_types":          "PRIMARY",
		"time_updated":          "1234567890",
		"transaction_timestamp": "0",
		"state":                 binlogdatapb.VReplicationWorkflowState_Running.String(),
		"db_name":               "vt_customer_0", // Target virtual keyspace database
		"target_keyspace":       "customer",      // Target virtual keyspace
		"workflow_type":         "1",
		"workflow_sub_type":     "0",
		"defer_secondary_keys":  "false",
		"options":               "{}",
	}

	// Create the controller
	ct, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
	require.NoError(t, err)
	defer ct.Stop()

	// Verify that the controller has the correct target schema
	assert.Equal(t, "customer", ct.targetKeyspace, "Target keyspace should be 'customer'")
	assert.Equal(t, "vt_customer_0", ct.targetSchema, "Target schema should be 'vt_customer_0'")

	// Test the schema-specific DB client factory
	dbClient := ct.getSchemaSpecificDBClientFactory()()

	// Verify that the client is configured to use the correct database
	if fakeClient, ok := dbClient.(*testFakeBinlogClient); ok {
		// The SetDBName method should have been called to switch to the target schema
		assert.Equal(t, "vt_customer_0", fakeClient.dbName, "DB client should be configured to use target schema 'vt_customer_0'")
	} else {
		t.Fatal("Expected testFakeBinlogClient type")
	}

	// Test engine schema mapping
	schema, err := vre.GetSchemaForKeyspace("customer")
	require.NoError(t, err)
	assert.Equal(t, "vt_customer_0", schema, "Engine should map 'customer' keyspace to 'vt_customer_0' schema")

	schema, err = vre.GetSchemaForKeyspace("commerce")
	require.NoError(t, err)
	assert.Equal(t, "vt_commerce_0", schema, "Engine should map 'commerce' keyspace to 'vt_commerce_0' schema")

	schema, err = vre.GetSchemaForKeyspace("main")
	require.NoError(t, err)
	assert.Equal(t, "vt_main", schema, "Engine should map 'main' keyspace to 'vt_main' schema")
}

// testFakeBinlogClient implements binlogplayer.DBClient for testing
type testFakeBinlogClient struct {
	dbName  string
	queries []string
}

func (f *testFakeBinlogClient) DBName() string {
	return f.dbName
}

func (f *testFakeBinlogClient) Connect() error {
	return nil
}

func (f *testFakeBinlogClient) Begin() error {
	return nil
}

func (f *testFakeBinlogClient) Commit() error {
	return nil
}

func (f *testFakeBinlogClient) Rollback() error {
	return nil
}

func (f *testFakeBinlogClient) Close() {
}

func (f *testFakeBinlogClient) IsClosed() bool {
	return false
}

func (f *testFakeBinlogClient) ExecuteFetch(query string, maxrows int) (*sqltypes.Result, error) {
	f.queries = append(f.queries, query)
	return &sqltypes.Result{}, nil
}

func (f *testFakeBinlogClient) ExecuteFetchMulti(query string, maxrows int) ([]*sqltypes.Result, error) {
	f.queries = append(f.queries, query)
	return []*sqltypes.Result{{}}, nil
}

func (f *testFakeBinlogClient) SupportsCapability(capability capabilities.FlavorCapability) (bool, error) {
	return false, nil
}

func (f *testFakeBinlogClient) SetDBName(dbName string) {
	f.dbName = dbName
}
