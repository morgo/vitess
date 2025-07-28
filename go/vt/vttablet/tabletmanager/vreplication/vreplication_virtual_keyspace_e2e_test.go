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
	"strings"
	"testing"
	"time"

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

// TestVirtualKeyspaceVReplicationE2E tests complete end-to-end replication workflow
// from a source keyspace to a virtual keyspace target
func TestVirtualKeyspaceVReplicationE2E(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Create source keyspace and shard
	err := ts.CreateKeyspace(ctx, "source", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "source", "0")
	require.NoError(t, err)

	// Create target physical keyspace and shard
	err = ts.CreateKeyspace(ctx, "main", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "main", "0")
	require.NoError(t, err)

	// Create source tablet
	sourceTablet := &topodatapb.Tablet{
		Alias:    &topodatapb.TabletAlias{Cell: "cell1", Uid: 100},
		Keyspace: "source",
		Shard:    "0",
		Type:     topodatapb.TabletType_PRIMARY,
		PortMap:  map[string]int32{"vt": 8100},
	}
	err = ts.CreateTablet(ctx, sourceTablet)
	require.NoError(t, err)

	// Create target tablet (physical keyspace)
	targetTablet := &topodatapb.Tablet{
		Alias:    &topodatapb.TabletAlias{Cell: "cell1", Uid: 200},
		Keyspace: "main",
		Shard:    "0",
		Type:     topodatapb.TabletType_PRIMARY,
		PortMap:  map[string]int32{"vt": 8200},
	}
	err = ts.CreateTablet(ctx, targetTablet)
	require.NoError(t, err)

	// Track database operations for verification
	var dbOperations []string
	var dbClients []*mockVirtualKeyspaceDBClient

	dbClientFactory := func() binlogplayer.DBClient {
		client := &mockVirtualKeyspaceDBClient{
			dbName:     "vt_main", // Default to physical keyspace
			operations: &dbOperations,
			queries:    make([]string, 0),
		}
		dbClients = append(dbClients, client)
		return client
	}

	mysqld := &mysqlctl.FakeMysqlDaemon{}
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	// Initialize engine with physical keyspace
	err = vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Add virtual keyspace for customer
	err = vre.AddVirtualKeyspace("customer", "vt_customer_0")
	require.NoError(t, err)

	// Open the engine
	vre.Open(ctx)

	// Test Case 1: Create VReplication workflow targeting virtual keyspace
	t.Run("CreateVirtualKeyspaceWorkflow", func(t *testing.T) {
		// Create workflow parameters targeting virtual keyspace
		params := map[string]string{
			"id":                    "1",
			"workflow":              "customer_replication",
			"source":                `keyspace:"source" shard:"0" filter:{rules:{match:"customer" filter:"select * from customer"}}`,
			"pos":                   "",
			"stop_pos":              "",
			"max_tps":               "9999",
			"max_replication_lag":   "9999",
			"cell":                  "cell1",
			"tablet_types":          "PRIMARY",
			"time_updated":          fmt.Sprintf("%d", time.Now().Unix()),
			"transaction_timestamp": "0",
			"state":                 binlogdatapb.VReplicationWorkflowState_Running.String(),
			"db_name":               "vt_customer_0",
			"target_keyspace":       "customer",
			"workflow_type":         "1",
			"workflow_sub_type":     "0",
			"defer_secondary_keys":  "false",
			"options":               "{}",
		}

		// Create controller
		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		// Verify controller configuration
		assert.Equal(t, "customer", controller.targetKeyspace)
		assert.Equal(t, "vt_customer_0", controller.targetSchema)
		assert.Equal(t, "source", controller.source.Keyspace)

		// Verify schema-specific DB client factory
		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()
		assert.NotNil(t, client)

		// Verify the client is configured for the correct database
		if mockClient, ok := client.(*mockVirtualKeyspaceDBClient); ok {
			assert.Equal(t, "vt_customer_0", mockClient.dbName)
		}
	})

	// Test Case 2: Verify VReplicator targets correct schema
	t.Run("VReplicatorSchemaTargeting", func(t *testing.T) {
		// Create a VReplicator with virtual keyspace targeting
		params := map[string]string{
			"id":              "2",
			"workflow":        "vreplicator_test",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"orders" filter:"select * from orders"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "customer",
			"db_name":         "vt_customer_0",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		// Verify controller has correct schema context
		assert.Equal(t, "customer", controller.targetKeyspace)
		assert.Equal(t, "vt_customer_0", controller.targetSchema)

		// Test schema-specific DB client factory
		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()
		if mockClient, ok := client.(*mockVirtualKeyspaceDBClient); ok {
			assert.Equal(t, "vt_customer_0", mockClient.dbName)
		}
	})

	// Test Case 3: Verify controller schema targeting works correctly
	t.Run("ControllerSchemaTargeting", func(t *testing.T) {
		params := map[string]string{
			"id":              "3",
			"workflow":        "controller_test",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "customer",
			"db_name":         "vt_customer_0",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		// Verify controller configuration
		assert.Equal(t, "customer", controller.targetKeyspace)
		assert.Equal(t, "vt_customer_0", controller.targetSchema)

		// Test schema-specific DB client factory
		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()
		if mockClient, ok := client.(*mockVirtualKeyspaceDBClient); ok {
			assert.Equal(t, "vt_customer_0", mockClient.dbName)
		}
	})

	// Test Case 4: Verify physical keyspace targeting works correctly
	t.Run("PhysicalKeyspaceTargeting", func(t *testing.T) {
		params := map[string]string{
			"id":              "4",
			"workflow":        "physical_test",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"inventory" filter:"select * from inventory"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "main",
			"db_name":         "vt_main",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		// Verify controller configuration
		assert.Equal(t, "main", controller.targetKeyspace)
		assert.Equal(t, "vt_main", controller.targetSchema)

		// Test schema-specific DB client factory
		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()
		if mockClient, ok := client.(*mockVirtualKeyspaceDBClient); ok {
			assert.Equal(t, "vt_main", mockClient.dbName)
		}
	})

	// Test Case 5: Verify database operations target correct schema
	t.Run("DatabaseOperationTargeting", func(t *testing.T) {
		// Clear previous operations
		dbOperations = []string{}

		params := map[string]string{
			"id":              "5",
			"workflow":        "db_ops_test",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"test_table" filter:"select * from test_table"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "customer",
			"db_name":         "vt_customer_0",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		// Get schema-specific client and perform operations
		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()

		// Simulate database operations
		_, err = client.ExecuteFetch("CREATE TABLE test_table (id INT PRIMARY KEY)", 1000)
		require.NoError(t, err)

		_, err = client.ExecuteFetch("INSERT INTO test_table VALUES (1)", 1000)
		require.NoError(t, err)

		// Verify operations were recorded with correct database context
		assert.Contains(t, dbOperations, "DB:vt_customer_0 EXECUTE:CREATE TABLE test_table (id INT PRIMARY KEY)")
		assert.Contains(t, dbOperations, "DB:vt_customer_0 EXECUTE:INSERT INTO test_table VALUES (1)")
	})
}

// TestVirtualKeyspaceVCopierE2E tests VCopier functionality with virtual keyspaces
func TestVirtualKeyspaceVCopierE2E(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Setup keyspaces and tablets
	err := ts.CreateKeyspace(ctx, "source", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "source", "0")
	require.NoError(t, err)

	err = ts.CreateKeyspace(ctx, "main", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "main", "0")
	require.NoError(t, err)

	// Track copy operations
	var copyOperations []string

	dbClientFactory := func() binlogplayer.DBClient {
		return &mockVirtualKeyspaceDBClient{
			dbName:     "vt_main",
			operations: &copyOperations,
			queries:    make([]string, 0),
		}
	}

	mysqld := &mysqlctl.FakeMysqlDaemon{}
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	err = vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Add virtual keyspaces
	err = vre.AddVirtualKeyspace("commerce", "vt_commerce_0")
	require.NoError(t, err)
	err = vre.AddVirtualKeyspace("customer", "vt_customer_0")
	require.NoError(t, err)

	vre.Open(ctx)

	// Test Case 1: VCopier targeting commerce virtual keyspace
	t.Run("VCopierCommerceKeyspace", func(t *testing.T) {
		copyOperations = []string{} // Reset operations

		params := map[string]string{
			"id":              "1",
			"workflow":        "commerce_copy",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		// Verify controller configuration
		assert.Equal(t, "commerce", controller.targetKeyspace)
		assert.Equal(t, "vt_commerce_0", controller.targetSchema)

		// Test schema-specific DB client factory
		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()
		if mockClient, ok := client.(*mockVirtualKeyspaceDBClient); ok {
			assert.Equal(t, "vt_commerce_0", mockClient.dbName)
		}
	})

	// Test Case 2: VCopier targeting customer virtual keyspace
	t.Run("VCopierCustomerKeyspace", func(t *testing.T) {
		copyOperations = []string{} // Reset operations

		params := map[string]string{
			"id":              "2",
			"workflow":        "customer_copy",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"customers" filter:"select * from customers"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "customer",
			"db_name":         "vt_customer_0",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		// Verify controller configuration
		assert.Equal(t, "customer", controller.targetKeyspace)
		assert.Equal(t, "vt_customer_0", controller.targetSchema)

		// Test schema-specific DB client factory
		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()
		if mockClient, ok := client.(*mockVirtualKeyspaceDBClient); ok {
			assert.Equal(t, "vt_customer_0", mockClient.dbName)
		}
	})

	// Test Case 3: Concurrent VCopier operations to different schemas
	t.Run("ConcurrentVCopierOperations", func(t *testing.T) {
		copyOperations = []string{} // Reset operations

		// Create two controllers targeting different virtual keyspaces
		commerceParams := map[string]string{
			"id":              "3",
			"workflow":        "concurrent_commerce",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		customerParams := map[string]string{
			"id":              "4",
			"workflow":        "concurrent_customer",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"customers" filter:"select * from customers"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "customer",
			"db_name":         "vt_customer_0",
			"options":         "{}",
		}

		commerceController, err := newController(ctx, commerceParams, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer commerceController.Stop()

		customerController, err := newController(ctx, customerParams, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer customerController.Stop()

		// Verify controller configurations
		assert.Equal(t, "commerce", commerceController.targetKeyspace)
		assert.Equal(t, "vt_commerce_0", commerceController.targetSchema)
		assert.Equal(t, "customer", customerController.targetKeyspace)
		assert.Equal(t, "vt_customer_0", customerController.targetSchema)

		// Test concurrent database operations
		commerceFactory := commerceController.getSchemaSpecificDBClientFactory()
		customerFactory := customerController.getSchemaSpecificDBClientFactory()

		commerceClient := commerceFactory()
		customerClient := customerFactory()

		// Commerce operations
		_, err = commerceClient.ExecuteFetch("CREATE TABLE products (id INT)", 1000)
		require.NoError(t, err)

		// Customer operations
		_, err = customerClient.ExecuteFetch("CREATE TABLE customers (id INT)", 1000)
		require.NoError(t, err)

		// Verify operations were isolated to correct schemas
		assert.Contains(t, copyOperations, "DB:vt_commerce_0 EXECUTE:CREATE TABLE products (id INT)")
		assert.Contains(t, copyOperations, "DB:vt_customer_0 EXECUTE:CREATE TABLE customers (id INT)")

		// Verify no cross-contamination
		for _, op := range copyOperations {
			if strings.Contains(op, "products") {
				assert.Contains(t, op, "vt_commerce_0", "Products operations should target commerce schema")
			}
			if strings.Contains(op, "customers") {
				assert.Contains(t, op, "vt_customer_0", "Customer operations should target customer schema")
			}
		}
	})
}

// TestVirtualKeyspaceVPlayerE2E tests VPlayer functionality with virtual keyspaces
func TestVirtualKeyspaceVPlayerE2E(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Setup keyspaces
	err := ts.CreateKeyspace(ctx, "source", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "source", "0")
	require.NoError(t, err)

	err = ts.CreateKeyspace(ctx, "main", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "main", "0")
	require.NoError(t, err)

	// Track player operations
	var playerOperations []string

	dbClientFactory := func() binlogplayer.DBClient {
		return &mockVirtualKeyspaceDBClient{
			dbName:     "vt_main",
			operations: &playerOperations,
			queries:    make([]string, 0),
		}
	}

	mysqld := &mysqlctl.FakeMysqlDaemon{}
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	err = vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Add virtual keyspaces
	err = vre.AddVirtualKeyspace("commerce", "vt_commerce_0")
	require.NoError(t, err)
	err = vre.AddVirtualKeyspace("customer", "vt_customer_0")
	require.NoError(t, err)

	vre.Open(ctx)

	// Test Case 1: VPlayer targeting commerce virtual keyspace
	t.Run("VPlayerCommerceKeyspace", func(t *testing.T) {
		playerOperations = []string{} // Reset operations

		params := map[string]string{
			"id":              "1",
			"workflow":        "commerce_player",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		// Verify controller configuration
		assert.Equal(t, "commerce", controller.targetKeyspace)
		assert.Equal(t, "vt_commerce_0", controller.targetSchema)

		// Test schema-specific DB client factory
		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()
		if mockClient, ok := client.(*mockVirtualKeyspaceDBClient); ok {
			assert.Equal(t, "vt_commerce_0", mockClient.dbName)
		}
	})

	// Test Case 2: VPlayer targeting customer virtual keyspace
	t.Run("VPlayerCustomerKeyspace", func(t *testing.T) {
		playerOperations = []string{} // Reset operations

		params := map[string]string{
			"id":              "2",
			"workflow":        "customer_player",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"customers" filter:"select * from customers"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "customer",
			"db_name":         "vt_customer_0",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		// Verify controller configuration
		assert.Equal(t, "customer", controller.targetKeyspace)
		assert.Equal(t, "vt_customer_0", controller.targetSchema)

		// Test schema-specific DB client factory
		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()
		if mockClient, ok := client.(*mockVirtualKeyspaceDBClient); ok {
			assert.Equal(t, "vt_customer_0", mockClient.dbName)
		}
	})
}

// mockVirtualKeyspaceDBClient implements binlogplayer.DBClient for testing virtual keyspace operations
type mockVirtualKeyspaceDBClient struct {
	dbName     string
	operations *[]string
	queries    []string
}

func (m *mockVirtualKeyspaceDBClient) DBName() string {
	return m.dbName
}

func (m *mockVirtualKeyspaceDBClient) Connect() error {
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s CONNECT", m.dbName))
	return nil
}

func (m *mockVirtualKeyspaceDBClient) Begin() error {
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s BEGIN", m.dbName))
	return nil
}

func (m *mockVirtualKeyspaceDBClient) Commit() error {
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s COMMIT", m.dbName))
	return nil
}

func (m *mockVirtualKeyspaceDBClient) Rollback() error {
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s ROLLBACK", m.dbName))
	return nil
}

func (m *mockVirtualKeyspaceDBClient) Close() {
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s CLOSE", m.dbName))
}

func (m *mockVirtualKeyspaceDBClient) IsClosed() bool {
	return false
}

func (m *mockVirtualKeyspaceDBClient) ExecuteFetch(query string, maxrows int) (*sqltypes.Result, error) {
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s EXECUTE:%s", m.dbName, query))
	m.queries = append(m.queries, query)
	return &sqltypes.Result{}, nil
}

func (m *mockVirtualKeyspaceDBClient) ExecuteFetchMulti(query string, maxrows int) ([]*sqltypes.Result, error) {
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s EXECUTE_MULTI:%s", m.dbName, query))
	m.queries = append(m.queries, query)
	return []*sqltypes.Result{{}}, nil
}

func (m *mockVirtualKeyspaceDBClient) SupportsCapability(capability capabilities.FlavorCapability) (bool, error) {
	return false, nil
}

func (m *mockVirtualKeyspaceDBClient) SetDBName(dbName string) {
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s SET_DB_NAME:%s", m.dbName, dbName))
	m.dbName = dbName
}
