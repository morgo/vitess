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
	"sync"
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

// TestConcurrentVirtualKeyspaceReplication tests multiple workflows running simultaneously
// to different virtual keyspaces
func TestConcurrentVirtualKeyspaceReplication(t *testing.T) {
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

	// Track all database operations across all virtual keyspaces
	var allOperations []string
	var operationsMutex sync.Mutex

	dbClientFactory := func() binlogplayer.DBClient {
		return &concurrentMockDBClient{
			dbName:     "vt_main",
			operations: &allOperations,
			mutex:      &operationsMutex,
		}
	}

	mysqld := &mysqlctl.FakeMysqlDaemon{}
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	// Initialize engine with physical keyspace
	err = vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Add multiple virtual keyspaces
	err = vre.AddVirtualKeyspace("commerce", "vt_commerce_0")
	require.NoError(t, err)
	err = vre.AddVirtualKeyspace("customer", "vt_customer_0")
	require.NoError(t, err)
	err = vre.AddVirtualKeyspace("inventory", "vt_inventory_0")
	require.NoError(t, err)

	vre.Open(ctx)

	// Test Case 1: Concurrent workflow creation
	t.Run("ConcurrentWorkflowCreation", func(t *testing.T) {
		operationsMutex.Lock()
		allOperations = []string{} // Reset operations
		operationsMutex.Unlock()

		// Create multiple workflows concurrently
		var wg sync.WaitGroup
		controllers := make([]*controller, 3)

		workflows := []struct {
			id             string
			workflow       string
			targetKeyspace string
			targetSchema   string
			table          string
		}{
			{"1", "commerce_workflow", "commerce", "vt_commerce_0", "products"},
			{"2", "customer_workflow", "customer", "vt_customer_0", "customers"},
			{"3", "inventory_workflow", "inventory", "vt_inventory_0", "items"},
		}

		for i, w := range workflows {
			wg.Add(1)
			go func(idx int, workflow struct {
				id             string
				workflow       string
				targetKeyspace string
				targetSchema   string
				table          string
			}) {
				defer wg.Done()

				params := map[string]string{
					"id":              workflow.id,
					"workflow":        workflow.workflow,
					"source":          fmt.Sprintf(`keyspace:"source" shard:"0" filter:{rules:{match:"%s" filter:"select * from %s"}}`, workflow.table, workflow.table),
					"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
					"target_keyspace": workflow.targetKeyspace,
					"db_name":         workflow.targetSchema,
					"options":         "{}",
				}

				controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
				require.NoError(t, err)
				controllers[idx] = controller
			}(i, w)
		}

		wg.Wait()

		// Verify all controllers were created correctly
		for i, w := range workflows {
			require.NotNil(t, controllers[i])
			assert.Equal(t, w.targetKeyspace, controllers[i].targetKeyspace)
			assert.Equal(t, w.targetSchema, controllers[i].targetSchema)
		}

		// Clean up
		for _, controller := range controllers {
			if controller != nil {
				controller.Stop()
			}
		}
	})

	// Test Case 2: Concurrent database operations
	t.Run("ConcurrentDatabaseOperations", func(t *testing.T) {
		operationsMutex.Lock()
		allOperations = []string{} // Reset operations
		operationsMutex.Unlock()

		// Create controllers for each virtual keyspace
		controllers := make([]*controller, 3)

		workflows := []struct {
			id             string
			workflow       string
			targetKeyspace string
			targetSchema   string
			table          string
		}{
			{"10", "commerce_ops", "commerce", "vt_commerce_0", "products"},
			{"11", "customer_ops", "customer", "vt_customer_0", "customers"},
			{"12", "inventory_ops", "inventory", "vt_inventory_0", "items"},
		}

		// Create controllers
		for i, w := range workflows {
			params := map[string]string{
				"id":              w.id,
				"workflow":        w.workflow,
				"source":          fmt.Sprintf(`keyspace:"source" shard:"0" filter:{rules:{match:"%s" filter:"select * from %s"}}`, w.table, w.table),
				"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
				"target_keyspace": w.targetKeyspace,
				"db_name":         w.targetSchema,
				"options":         "{}",
			}

			controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
			require.NoError(t, err)
			controllers[i] = controller
		}

		// Perform concurrent database operations
		var wg sync.WaitGroup
		for i, w := range workflows {
			wg.Add(1)
			go func(idx int, workflow struct {
				id             string
				workflow       string
				targetKeyspace string
				targetSchema   string
				table          string
			}) {
				defer wg.Done()

				factory := controllers[idx].getSchemaSpecificDBClientFactory()
				client := factory()

				// Perform operations specific to this virtual keyspace
				_, err := client.ExecuteFetch(fmt.Sprintf("CREATE TABLE %s (id INT PRIMARY KEY)", workflow.table), 1000)
				require.NoError(t, err)

				_, err = client.ExecuteFetch(fmt.Sprintf("INSERT INTO %s VALUES (1)", workflow.table), 1000)
				require.NoError(t, err)

				_, err = client.ExecuteFetch(fmt.Sprintf("UPDATE %s SET id = 2 WHERE id = 1", workflow.table), 1000)
				require.NoError(t, err)
			}(i, w)
		}

		wg.Wait()

		// Verify operations were isolated to correct schemas
		operationsMutex.Lock()
		defer operationsMutex.Unlock()

		// Check that each virtual keyspace had its operations
		for _, w := range workflows {
			assert.Contains(t, allOperations, fmt.Sprintf("DB:%s EXECUTE:CREATE TABLE %s (id INT PRIMARY KEY)", w.targetSchema, w.table))
			assert.Contains(t, allOperations, fmt.Sprintf("DB:%s EXECUTE:INSERT INTO %s VALUES (1)", w.targetSchema, w.table))
			assert.Contains(t, allOperations, fmt.Sprintf("DB:%s EXECUTE:UPDATE %s SET id = 2 WHERE id = 1", w.targetSchema, w.table))
		}

		// Verify no cross-contamination
		for _, op := range allOperations {
			if strings.Contains(op, "products") {
				assert.Contains(t, op, "vt_commerce_0", "Products operations should target commerce schema")
			}
			if strings.Contains(op, "customers") {
				assert.Contains(t, op, "vt_customer_0", "Customer operations should target customer schema")
			}
			if strings.Contains(op, "items") {
				assert.Contains(t, op, "vt_inventory_0", "Items operations should target inventory schema")
			}
		}

		// Clean up
		for _, controller := range controllers {
			controller.Stop()
		}
	})

	// Test Case 3: Schema isolation verification
	t.Run("SchemaIsolationVerification", func(t *testing.T) {
		operationsMutex.Lock()
		allOperations = []string{} // Reset operations
		operationsMutex.Unlock()

		// Create controllers for different virtual keyspaces
		commerceParams := map[string]string{
			"id":              "20",
			"workflow":        "isolation_commerce",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		customerParams := map[string]string{
			"id":              "21",
			"workflow":        "isolation_customer",
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

		// Test that each controller targets the correct schema
		assert.Equal(t, "commerce", commerceController.targetKeyspace)
		assert.Equal(t, "vt_commerce_0", commerceController.targetSchema)
		assert.Equal(t, "customer", customerController.targetKeyspace)
		assert.Equal(t, "vt_customer_0", customerController.targetSchema)

		// Test that DB clients are isolated
		commerceFactory := commerceController.getSchemaSpecificDBClientFactory()
		customerFactory := customerController.getSchemaSpecificDBClientFactory()

		commerceClient := commerceFactory()
		customerClient := customerFactory()

		// Verify clients target different schemas
		if commerceMock, ok := commerceClient.(*concurrentMockDBClient); ok {
			assert.Equal(t, "vt_commerce_0", commerceMock.dbName)
		}
		if customerMock, ok := customerClient.(*concurrentMockDBClient); ok {
			assert.Equal(t, "vt_customer_0", customerMock.dbName)
		}

		// Perform operations that should be isolated
		_, err = commerceClient.ExecuteFetch("CREATE TABLE shared_name (id INT)", 1000)
		require.NoError(t, err)

		_, err = customerClient.ExecuteFetch("CREATE TABLE shared_name (id INT)", 1000)
		require.NoError(t, err)

		// Verify operations went to different schemas
		operationsMutex.Lock()
		defer operationsMutex.Unlock()

		assert.Contains(t, allOperations, "DB:vt_commerce_0 EXECUTE:CREATE TABLE shared_name (id INT)")
		assert.Contains(t, allOperations, "DB:vt_customer_0 EXECUTE:CREATE TABLE shared_name (id INT)")
	})
}

// TestVirtualKeyspaceIsolation tests that virtual keyspaces are properly isolated
func TestVirtualKeyspaceIsolation(t *testing.T) {
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

	// Track operations per schema
	schemaOperations := make(map[string][]string)
	var operationsMutex sync.Mutex

	dbClientFactory := func() binlogplayer.DBClient {
		return &isolationMockDBClient{
			dbName:           "vt_main",
			schemaOperations: schemaOperations,
			mutex:            &operationsMutex,
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

	// Test Case 1: Transaction isolation
	t.Run("TransactionIsolation", func(t *testing.T) {
		operationsMutex.Lock()
		schemaOperations = make(map[string][]string)
		operationsMutex.Unlock()

		// Create controllers
		commerceParams := map[string]string{
			"id":              "1",
			"workflow":        "transaction_commerce",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		customerParams := map[string]string{
			"id":              "2",
			"workflow":        "transaction_customer",
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

		// Perform concurrent transactions
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			factory := commerceController.getSchemaSpecificDBClientFactory()
			client := factory()

			client.Begin()
			client.ExecuteFetch("INSERT INTO products VALUES (1)", 1000)
			client.ExecuteFetch("INSERT INTO products VALUES (2)", 1000)
			client.Commit()
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			factory := customerController.getSchemaSpecificDBClientFactory()
			client := factory()

			client.Begin()
			client.ExecuteFetch("INSERT INTO customers VALUES (1)", 1000)
			client.ExecuteFetch("INSERT INTO customers VALUES (2)", 1000)
			client.Rollback()
		}()

		wg.Wait()

		// Verify transactions were isolated
		operationsMutex.Lock()
		defer operationsMutex.Unlock()

		commerceOps := schemaOperations["vt_commerce_0"]
		customerOps := schemaOperations["vt_customer_0"]

		// Commerce should have begin, inserts, and commit
		assert.Contains(t, commerceOps, "BEGIN")
		assert.Contains(t, commerceOps, "INSERT INTO products VALUES (1)")
		assert.Contains(t, commerceOps, "INSERT INTO products VALUES (2)")
		assert.Contains(t, commerceOps, "COMMIT")

		// Customer should have begin, inserts, and rollback
		assert.Contains(t, customerOps, "BEGIN")
		assert.Contains(t, customerOps, "INSERT INTO customers VALUES (1)")
		assert.Contains(t, customerOps, "INSERT INTO customers VALUES (2)")
		assert.Contains(t, customerOps, "ROLLBACK")

		// Verify no cross-contamination
		for _, op := range commerceOps {
			assert.NotContains(t, op, "customers", "Commerce operations should not contain customer data")
		}
		for _, op := range customerOps {
			assert.NotContains(t, op, "products", "Customer operations should not contain product data")
		}
	})
}

// concurrentMockDBClient implements binlogplayer.DBClient for concurrent testing
type concurrentMockDBClient struct {
	dbName     string
	operations *[]string
	mutex      *sync.Mutex
}

func (m *concurrentMockDBClient) DBName() string {
	return m.dbName
}

func (m *concurrentMockDBClient) Connect() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s CONNECT", m.dbName))
	return nil
}

func (m *concurrentMockDBClient) Begin() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s BEGIN", m.dbName))
	return nil
}

func (m *concurrentMockDBClient) Commit() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s COMMIT", m.dbName))
	return nil
}

func (m *concurrentMockDBClient) Rollback() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s ROLLBACK", m.dbName))
	return nil
}

func (m *concurrentMockDBClient) Close() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s CLOSE", m.dbName))
}

func (m *concurrentMockDBClient) IsClosed() bool {
	return false
}

func (m *concurrentMockDBClient) ExecuteFetch(query string, maxrows int) (*sqltypes.Result, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s EXECUTE:%s", m.dbName, query))
	return &sqltypes.Result{}, nil
}

func (m *concurrentMockDBClient) ExecuteFetchMulti(query string, maxrows int) ([]*sqltypes.Result, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s EXECUTE_MULTI:%s", m.dbName, query))
	return []*sqltypes.Result{{}}, nil
}

func (m *concurrentMockDBClient) SupportsCapability(capability capabilities.FlavorCapability) (bool, error) {
	return false, nil
}

func (m *concurrentMockDBClient) SetDBName(dbName string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	*m.operations = append(*m.operations, fmt.Sprintf("DB:%s SET_DB_NAME:%s", m.dbName, dbName))
	m.dbName = dbName
}

// isolationMockDBClient implements binlogplayer.DBClient for isolation testing
type isolationMockDBClient struct {
	dbName           string
	schemaOperations map[string][]string
	mutex            *sync.Mutex
}

func (m *isolationMockDBClient) DBName() string {
	return m.dbName
}

func (m *isolationMockDBClient) Connect() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.schemaOperations[m.dbName] == nil {
		m.schemaOperations[m.dbName] = make([]string, 0)
	}
	m.schemaOperations[m.dbName] = append(m.schemaOperations[m.dbName], "CONNECT")
	return nil
}

func (m *isolationMockDBClient) Begin() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.schemaOperations[m.dbName] == nil {
		m.schemaOperations[m.dbName] = make([]string, 0)
	}
	m.schemaOperations[m.dbName] = append(m.schemaOperations[m.dbName], "BEGIN")
	return nil
}

func (m *isolationMockDBClient) Commit() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.schemaOperations[m.dbName] == nil {
		m.schemaOperations[m.dbName] = make([]string, 0)
	}
	m.schemaOperations[m.dbName] = append(m.schemaOperations[m.dbName], "COMMIT")
	return nil
}

func (m *isolationMockDBClient) Rollback() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.schemaOperations[m.dbName] == nil {
		m.schemaOperations[m.dbName] = make([]string, 0)
	}
	m.schemaOperations[m.dbName] = append(m.schemaOperations[m.dbName], "ROLLBACK")
	return nil
}

func (m *isolationMockDBClient) Close() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.schemaOperations[m.dbName] == nil {
		m.schemaOperations[m.dbName] = make([]string, 0)
	}
	m.schemaOperations[m.dbName] = append(m.schemaOperations[m.dbName], "CLOSE")
}

func (m *isolationMockDBClient) IsClosed() bool {
	return false
}

func (m *isolationMockDBClient) ExecuteFetch(query string, maxrows int) (*sqltypes.Result, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.schemaOperations[m.dbName] == nil {
		m.schemaOperations[m.dbName] = make([]string, 0)
	}
	m.schemaOperations[m.dbName] = append(m.schemaOperations[m.dbName], query)
	return &sqltypes.Result{}, nil
}

func (m *isolationMockDBClient) ExecuteFetchMulti(query string, maxrows int) ([]*sqltypes.Result, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.schemaOperations[m.dbName] == nil {
		m.schemaOperations[m.dbName] = make([]string, 0)
	}
	m.schemaOperations[m.dbName] = append(m.schemaOperations[m.dbName], query)
	return []*sqltypes.Result{{}}, nil
}

func (m *isolationMockDBClient) SupportsCapability(capability capabilities.FlavorCapability) (bool, error) {
	return false, nil
}

func (m *isolationMockDBClient) SetDBName(dbName string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.schemaOperations[m.dbName] == nil {
		m.schemaOperations[m.dbName] = make([]string, 0)
	}
	m.schemaOperations[m.dbName] = append(m.schemaOperations[m.dbName], fmt.Sprintf("SET_DB_NAME:%s", dbName))
	m.dbName = dbName
}
