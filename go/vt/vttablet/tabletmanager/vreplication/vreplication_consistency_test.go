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

// TestVirtualKeyspaceDataConsistency tests data consistency within virtual keyspaces
func TestVirtualKeyspaceDataConsistency(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Create keyspaces
	err := ts.CreateKeyspace(ctx, "source", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "source", "0")
	require.NoError(t, err)

	err = ts.CreateKeyspace(ctx, "main", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "main", "0")
	require.NoError(t, err)

	// Track data operations for consistency verification
	dataOperations := make(map[string][]DataOperation)
	var dataMutex sync.Mutex

	dbClientFactory := func() binlogplayer.DBClient {
		return &consistencyMockDBClient{
			dbName:         "vt_main",
			dataOperations: dataOperations,
			mutex:          &dataMutex,
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

	// Test Case 1: Data consistency within a single virtual keyspace
	t.Run("SingleVirtualKeyspaceConsistency", func(t *testing.T) {
		dataMutex.Lock()
		dataOperations = make(map[string][]DataOperation)
		dataMutex.Unlock()

		// Create controller for commerce virtual keyspace
		params := map[string]string{
			"id":              "1",
			"workflow":        "consistency_test",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()

		// Simulate a sequence of data operations that should be consistent
		client.Begin()
		client.ExecuteFetch("INSERT INTO products VALUES (1, 'Product A', 100.00)", 1000)
		client.ExecuteFetch("INSERT INTO product_inventory VALUES (1, 50)", 1000)
		client.ExecuteFetch("UPDATE products SET price = 95.00 WHERE id = 1", 1000)
		client.Commit()

		// Verify all operations went to the same schema
		dataMutex.Lock()
		defer dataMutex.Unlock()

		commerceOps := dataOperations["vt_commerce_0"]
		require.NotEmpty(t, commerceOps)

		// Check transaction boundaries
		assert.Equal(t, "BEGIN", commerceOps[0].Operation)
		assert.Equal(t, "COMMIT", commerceOps[len(commerceOps)-1].Operation)

		// Verify data consistency - all operations should reference the same product
		productOperations := 0
		for _, op := range commerceOps {
			if strings.Contains(op.Query, "products") || strings.Contains(op.Query, "product_inventory") {
				productOperations++
				assert.Contains(t, op.Query, "1", "All operations should reference product ID 1")
			}
		}
		assert.Equal(t, 3, productOperations, "Should have 3 product-related operations")
	})

	// Test Case 2: Data isolation between virtual keyspaces
	t.Run("CrossVirtualKeyspaceDataIsolation", func(t *testing.T) {
		dataMutex.Lock()
		dataOperations = make(map[string][]DataOperation)
		dataMutex.Unlock()

		// Create controllers for both virtual keyspaces
		commerceParams := map[string]string{
			"id":              "2",
			"workflow":        "commerce_isolation",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		customerParams := map[string]string{
			"id":              "3",
			"workflow":        "customer_isolation",
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

		// Perform operations on both keyspaces with same IDs to test isolation
		commerceFactory := commerceController.getSchemaSpecificDBClientFactory()
		customerFactory := customerController.getSchemaSpecificDBClientFactory()

		commerceClient := commerceFactory()
		customerClient := customerFactory()

		// Commerce operations
		commerceClient.Begin()
		commerceClient.ExecuteFetch("INSERT INTO products VALUES (100, 'Commerce Product', 50.00)", 1000)
		commerceClient.ExecuteFetch("UPDATE products SET name = 'Updated Commerce Product' WHERE id = 100", 1000)
		commerceClient.Commit()

		// Customer operations (same ID, different data)
		customerClient.Begin()
		customerClient.ExecuteFetch("INSERT INTO customers VALUES (100, 'Customer Name', 'customer@example.com')", 1000)
		customerClient.ExecuteFetch("UPDATE customers SET email = 'updated@example.com' WHERE id = 100", 1000)
		customerClient.Commit()

		// Verify data isolation
		dataMutex.Lock()
		defer dataMutex.Unlock()

		commerceOps := dataOperations["vt_commerce_0"]
		customerOps := dataOperations["vt_customer_0"]

		// Verify commerce operations only contain product data
		for _, op := range commerceOps {
			if op.Operation == "EXECUTE" {
				assert.Contains(t, op.Query, "products", "Commerce operations should only contain product data")
				assert.NotContains(t, op.Query, "customers", "Commerce operations should not contain customer data")
			}
		}

		// Verify customer operations only contain customer data
		for _, op := range customerOps {
			if op.Operation == "EXECUTE" {
				assert.Contains(t, op.Query, "customers", "Customer operations should only contain customer data")
				assert.NotContains(t, op.Query, "products", "Customer operations should not contain product data")
			}
		}
	})

	// Test Case 3: Transaction boundary respect
	t.Run("VirtualKeyspaceTransactionBoundaries", func(t *testing.T) {
		dataMutex.Lock()
		dataOperations = make(map[string][]DataOperation)
		dataMutex.Unlock()

		params := map[string]string{
			"id":              "4",
			"workflow":        "transaction_boundaries",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"orders" filter:"select * from orders"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()

		// Test successful transaction
		client.Begin()
		client.ExecuteFetch("INSERT INTO orders VALUES (1, 100, 'pending')", 1000)
		client.ExecuteFetch("INSERT INTO order_items VALUES (1, 1, 2, 25.00)", 1000)
		client.ExecuteFetch("UPDATE orders SET total = 50.00 WHERE id = 1", 1000)
		client.Commit()

		// Test rolled back transaction
		client.Begin()
		client.ExecuteFetch("INSERT INTO orders VALUES (2, 200, 'pending')", 1000)
		client.ExecuteFetch("INSERT INTO order_items VALUES (2, 2, 1, 75.00)", 1000)
		client.Rollback()

		// Verify transaction boundaries
		dataMutex.Lock()
		defer dataMutex.Unlock()

		commerceOps := dataOperations["vt_commerce_0"]

		// Find transaction boundaries
		transactionStarts := []int{}
		transactionEnds := []int{}

		for i, op := range commerceOps {
			if op.Operation == "BEGIN" {
				transactionStarts = append(transactionStarts, i)
			} else if op.Operation == "COMMIT" || op.Operation == "ROLLBACK" {
				transactionEnds = append(transactionEnds, i)
			}
		}

		// Should have 2 transaction starts and 2 transaction ends
		assert.Len(t, transactionStarts, 2, "Should have 2 transaction starts")
		assert.Len(t, transactionEnds, 2, "Should have 2 transaction ends")

		// Verify first transaction was committed
		firstTxnEnd := transactionEnds[0]
		assert.Equal(t, "COMMIT", commerceOps[firstTxnEnd].Operation, "First transaction should be committed")

		// Verify second transaction was rolled back
		secondTxnEnd := transactionEnds[1]
		assert.Equal(t, "ROLLBACK", commerceOps[secondTxnEnd].Operation, "Second transaction should be rolled back")
	})
}

// TestCrossSchemaDataIsolation tests that data doesn't leak between schemas
func TestCrossSchemaDataIsolation(t *testing.T) {
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

	// Track schema-specific data access patterns
	schemaAccess := make(map[string]map[string]int) // schema -> table -> count
	var accessMutex sync.Mutex

	dbClientFactory := func() binlogplayer.DBClient {
		return &isolationTrackingDBClient{
			dbName:       "vt_main",
			schemaAccess: schemaAccess,
			mutex:        &accessMutex,
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
	err = vre.AddVirtualKeyspace("inventory", "vt_inventory_0")
	require.NoError(t, err)

	vre.Open(ctx)

	// Test Case 1: Schema-specific table access
	t.Run("SchemaSpecificTableAccess", func(t *testing.T) {
		accessMutex.Lock()
		schemaAccess = make(map[string]map[string]int)
		accessMutex.Unlock()

		// Create controllers for different virtual keyspaces
		controllers := []*controller{}

		workflows := []struct {
			id             string
			targetKeyspace string
			targetSchema   string
			table          string
		}{
			{"1", "commerce", "vt_commerce_0", "products"},
			{"2", "customer", "vt_customer_0", "customers"},
			{"3", "inventory", "vt_inventory_0", "items"},
		}

		// Create controllers
		for _, w := range workflows {
			params := map[string]string{
				"id":              w.id,
				"workflow":        fmt.Sprintf("isolation_%s", w.targetKeyspace),
				"source":          fmt.Sprintf(`keyspace:"source" shard:"0" filter:{rules:{match:"%s" filter:"select * from %s"}}`, w.table, w.table),
				"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
				"target_keyspace": w.targetKeyspace,
				"db_name":         w.targetSchema,
				"options":         "{}",
			}

			controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
			require.NoError(t, err)
			controllers = append(controllers, controller)
		}

		// Perform operations on each schema
		for i, w := range workflows {
			factory := controllers[i].getSchemaSpecificDBClientFactory()
			client := factory()

			// Perform table-specific operations
			client.ExecuteFetch(fmt.Sprintf("SELECT * FROM %s", w.table), 1000)
			client.ExecuteFetch(fmt.Sprintf("INSERT INTO %s VALUES (1)", w.table), 1000)
			client.ExecuteFetch(fmt.Sprintf("UPDATE %s SET id = 2 WHERE id = 1", w.table), 1000)
			client.ExecuteFetch(fmt.Sprintf("DELETE FROM %s WHERE id = 2", w.table), 1000)
		}

		// Verify schema isolation
		accessMutex.Lock()
		defer accessMutex.Unlock()

		for _, w := range workflows {
			schemaData, exists := schemaAccess[w.targetSchema]
			require.True(t, exists, "Schema %s should have access data", w.targetSchema)

			tableCount, exists := schemaData[w.table]
			require.True(t, exists, "Table %s should be accessed in schema %s", w.table, w.targetSchema)
			assert.Equal(t, 4, tableCount, "Should have 4 operations on table %s", w.table)

			// Verify no cross-contamination
			for otherSchema, otherData := range schemaAccess {
				if otherSchema != w.targetSchema {
					_, exists := otherData[w.table]
					assert.False(t, exists, "Table %s should not be accessed in schema %s", w.table, otherSchema)
				}
			}
		}

		// Clean up
		for _, controller := range controllers {
			controller.Stop()
		}
	})

	// Test Case 2: Concurrent schema access isolation
	t.Run("ConcurrentSchemaAccessIsolation", func(t *testing.T) {
		accessMutex.Lock()
		schemaAccess = make(map[string]map[string]int)
		accessMutex.Unlock()

		// Create controllers for concurrent access
		commerceParams := map[string]string{
			"id":              "10",
			"workflow":        "concurrent_commerce",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		customerParams := map[string]string{
			"id":              "11",
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

		// Perform concurrent operations
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			factory := commerceController.getSchemaSpecificDBClientFactory()
			client := factory()

			for i := 0; i < 10; i++ {
				client.ExecuteFetch(fmt.Sprintf("INSERT INTO products VALUES (%d, 'Product %d')", i, i), 1000)
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			factory := customerController.getSchemaSpecificDBClientFactory()
			client := factory()

			for i := 0; i < 10; i++ {
				client.ExecuteFetch(fmt.Sprintf("INSERT INTO customers VALUES (%d, 'Customer %d')", i, i), 1000)
			}
		}()

		wg.Wait()

		// Verify concurrent access isolation
		accessMutex.Lock()
		defer accessMutex.Unlock()

		commerceData := schemaAccess["vt_commerce_0"]
		customerData := schemaAccess["vt_customer_0"]

		// Verify each schema accessed only its own tables
		assert.Equal(t, 10, commerceData["products"], "Commerce schema should have 10 product operations")
		assert.Equal(t, 0, commerceData["customers"], "Commerce schema should have 0 customer operations")

		assert.Equal(t, 10, customerData["customers"], "Customer schema should have 10 customer operations")
		assert.Equal(t, 0, customerData["products"], "Customer schema should have 0 product operations")
	})
}

// DataOperation represents a database operation for consistency tracking
type DataOperation struct {
	Operation string
	Query     string
	Schema    string
}

// consistencyMockDBClient implements binlogplayer.DBClient for consistency testing
type consistencyMockDBClient struct {
	dbName         string
	dataOperations map[string][]DataOperation
	mutex          *sync.Mutex
}

func (m *consistencyMockDBClient) DBName() string {
	return m.dbName
}

func (m *consistencyMockDBClient) Connect() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.dataOperations[m.dbName] == nil {
		m.dataOperations[m.dbName] = make([]DataOperation, 0)
	}
	m.dataOperations[m.dbName] = append(m.dataOperations[m.dbName], DataOperation{
		Operation: "CONNECT",
		Schema:    m.dbName,
	})
	return nil
}

func (m *consistencyMockDBClient) Begin() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.dataOperations[m.dbName] == nil {
		m.dataOperations[m.dbName] = make([]DataOperation, 0)
	}
	m.dataOperations[m.dbName] = append(m.dataOperations[m.dbName], DataOperation{
		Operation: "BEGIN",
		Schema:    m.dbName,
	})
	return nil
}

func (m *consistencyMockDBClient) Commit() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.dataOperations[m.dbName] == nil {
		m.dataOperations[m.dbName] = make([]DataOperation, 0)
	}
	m.dataOperations[m.dbName] = append(m.dataOperations[m.dbName], DataOperation{
		Operation: "COMMIT",
		Schema:    m.dbName,
	})
	return nil
}

func (m *consistencyMockDBClient) Rollback() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.dataOperations[m.dbName] == nil {
		m.dataOperations[m.dbName] = make([]DataOperation, 0)
	}
	m.dataOperations[m.dbName] = append(m.dataOperations[m.dbName], DataOperation{
		Operation: "ROLLBACK",
		Schema:    m.dbName,
	})
	return nil
}

func (m *consistencyMockDBClient) Close() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.dataOperations[m.dbName] == nil {
		m.dataOperations[m.dbName] = make([]DataOperation, 0)
	}
	m.dataOperations[m.dbName] = append(m.dataOperations[m.dbName], DataOperation{
		Operation: "CLOSE",
		Schema:    m.dbName,
	})
}

func (m *consistencyMockDBClient) IsClosed() bool {
	return false
}

func (m *consistencyMockDBClient) ExecuteFetch(query string, maxrows int) (*sqltypes.Result, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.dataOperations[m.dbName] == nil {
		m.dataOperations[m.dbName] = make([]DataOperation, 0)
	}
	m.dataOperations[m.dbName] = append(m.dataOperations[m.dbName], DataOperation{
		Operation: "EXECUTE",
		Query:     query,
		Schema:    m.dbName,
	})
	return &sqltypes.Result{}, nil
}

func (m *consistencyMockDBClient) ExecuteFetchMulti(query string, maxrows int) ([]*sqltypes.Result, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.dataOperations[m.dbName] == nil {
		m.dataOperations[m.dbName] = make([]DataOperation, 0)
	}
	m.dataOperations[m.dbName] = append(m.dataOperations[m.dbName], DataOperation{
		Operation: "EXECUTE_MULTI",
		Query:     query,
		Schema:    m.dbName,
	})
	return []*sqltypes.Result{{}}, nil
}

func (m *consistencyMockDBClient) SupportsCapability(capability capabilities.FlavorCapability) (bool, error) {
	return false, nil
}

func (m *consistencyMockDBClient) SetDBName(dbName string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.dataOperations[m.dbName] == nil {
		m.dataOperations[m.dbName] = make([]DataOperation, 0)
	}
	m.dataOperations[m.dbName] = append(m.dataOperations[m.dbName], DataOperation{
		Operation: "SET_DB_NAME",
		Query:     fmt.Sprintf("SET_DB_NAME:%s", dbName),
		Schema:    m.dbName,
	})
	m.dbName = dbName
}

// isolationTrackingDBClient implements binlogplayer.DBClient for isolation testing
type isolationTrackingDBClient struct {
	dbName       string
	schemaAccess map[string]map[string]int // schema -> table -> count
	mutex        *sync.Mutex
}

func (m *isolationTrackingDBClient) DBName() string {
	return m.dbName
}

func (m *isolationTrackingDBClient) Connect() error {
	return nil
}

func (m *isolationTrackingDBClient) Begin() error {
	return nil
}

func (m *isolationTrackingDBClient) Commit() error {
	return nil
}

func (m *isolationTrackingDBClient) Rollback() error {
	return nil
}

func (m *isolationTrackingDBClient) Close() {
}

func (m *isolationTrackingDBClient) IsClosed() bool {
	return false
}

func (m *isolationTrackingDBClient) ExecuteFetch(query string, maxrows int) (*sqltypes.Result, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Initialize schema access map if needed
	if m.schemaAccess[m.dbName] == nil {
		m.schemaAccess[m.dbName] = make(map[string]int)
	}

	// Extract table name from query (simple pattern matching)
	tables := []string{"products", "customers", "items", "orders", "order_items", "product_inventory"}
	for _, table := range tables {
		if strings.Contains(strings.ToLower(query), table) {
			m.schemaAccess[m.dbName][table]++
			break
		}
	}

	return &sqltypes.Result{}, nil
}

func (m *isolationTrackingDBClient) ExecuteFetchMulti(query string, maxrows int) ([]*sqltypes.Result, error) {
	// Delegate to ExecuteFetch for simplicity
	_, err := m.ExecuteFetch(query, maxrows)
	return []*sqltypes.Result{{}}, err
}

func (m *isolationTrackingDBClient) SupportsCapability(capability capabilities.FlavorCapability) (bool, error) {
	return false, nil
}

func (m *isolationTrackingDBClient) SetDBName(dbName string) {
	m.dbName = dbName
}
