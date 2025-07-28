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

// TestVirtualKeyspaceDDLHandling tests DDL operations on virtual keyspace schemas
func TestVirtualKeyspaceDDLHandling(t *testing.T) {
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

	// Track DDL operations
	ddlOperations := make(map[string][]DDLOperation)
	var ddlMutex sync.Mutex

	dbClientFactory := func() binlogplayer.DBClient {
		return &ddlTrackingDBClient{
			dbName:        "vt_main",
			ddlOperations: ddlOperations,
			mutex:         &ddlMutex,
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

	// Test Case 1: DDL operations on virtual keyspace schemas
	t.Run("VirtualKeyspaceDDLOperations", func(t *testing.T) {
		ddlMutex.Lock()
		ddlOperations = make(map[string][]DDLOperation)
		ddlMutex.Unlock()

		// Create controller for commerce virtual keyspace
		params := map[string]string{
			"id":              "1",
			"workflow":        "ddl_test",
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

		// Perform DDL operations
		ddlStatements := []string{
			"CREATE TABLE products (id INT PRIMARY KEY, name VARCHAR(100), price DECIMAL(10,2))",
			"ALTER TABLE products ADD COLUMN description TEXT",
			"CREATE INDEX idx_products_name ON products(name)",
			"ALTER TABLE products MODIFY COLUMN price DECIMAL(12,2)",
			"DROP INDEX idx_products_name ON products",
			"DROP TABLE products",
		}

		for _, ddl := range ddlStatements {
			_, err := client.ExecuteFetch(ddl, 1000)
			require.NoError(t, err)
		}

		// Verify DDL operations were tracked correctly
		ddlMutex.Lock()
		defer ddlMutex.Unlock()

		commerceOps := ddlOperations["vt_commerce_0"]
		require.Len(t, commerceOps, 6, "Should have 6 DDL operations")

		// Verify each DDL operation
		expectedOps := []struct {
			opType string
			table  string
		}{
			{"CREATE TABLE", "products"},
			{"ALTER TABLE", "products"},
			{"CREATE INDEX", "products"},
			{"ALTER TABLE", "products"},
			{"DROP INDEX", "products"},
			{"DROP TABLE", "products"},
		}

		for i, expected := range expectedOps {
			assert.Equal(t, expected.opType, commerceOps[i].OperationType, "Operation %d should be %s", i, expected.opType)
			assert.Equal(t, expected.table, commerceOps[i].TableName, "Operation %d should target table %s", i, expected.table)
			assert.Equal(t, "vt_commerce_0", commerceOps[i].Schema, "Operation %d should target schema vt_commerce_0", i)
		}
	})

	// Test Case 2: DDL isolation between virtual keyspaces
	t.Run("DDLIsolationBetweenVirtualKeyspaces", func(t *testing.T) {
		ddlMutex.Lock()
		ddlOperations = make(map[string][]DDLOperation)
		ddlMutex.Unlock()

		// Create controllers for both virtual keyspaces
		commerceParams := map[string]string{
			"id":              "2",
			"workflow":        "commerce_ddl",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		customerParams := map[string]string{
			"id":              "3",
			"workflow":        "customer_ddl",
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

		// Perform DDL operations on both schemas
		commerceFactory := commerceController.getSchemaSpecificDBClientFactory()
		customerFactory := customerController.getSchemaSpecificDBClientFactory()

		commerceClient := commerceFactory()
		customerClient := customerFactory()

		// Commerce DDL operations
		commerceClient.ExecuteFetch("CREATE TABLE products (id INT PRIMARY KEY, name VARCHAR(100))", 1000)
		commerceClient.ExecuteFetch("ALTER TABLE products ADD COLUMN category VARCHAR(50)", 1000)

		// Customer DDL operations
		customerClient.ExecuteFetch("CREATE TABLE customers (id INT PRIMARY KEY, email VARCHAR(100))", 1000)
		customerClient.ExecuteFetch("ALTER TABLE customers ADD COLUMN phone VARCHAR(20)", 1000)

		// Verify DDL isolation
		ddlMutex.Lock()
		defer ddlMutex.Unlock()

		commerceOps := ddlOperations["vt_commerce_0"]
		customerOps := ddlOperations["vt_customer_0"]

		// Verify commerce operations
		require.Len(t, commerceOps, 2, "Commerce should have 2 DDL operations")
		for _, op := range commerceOps {
			assert.Equal(t, "products", op.TableName, "Commerce DDL should target products table")
			assert.Equal(t, "vt_commerce_0", op.Schema, "Commerce DDL should target commerce schema")
		}

		// Verify customer operations
		require.Len(t, customerOps, 2, "Customer should have 2 DDL operations")
		for _, op := range customerOps {
			assert.Equal(t, "customers", op.TableName, "Customer DDL should target customers table")
			assert.Equal(t, "vt_customer_0", op.Schema, "Customer DDL should target customer schema")
		}
	})

	// Test Case 3: Concurrent DDL operations
	t.Run("ConcurrentDDLOperations", func(t *testing.T) {
		ddlMutex.Lock()
		ddlOperations = make(map[string][]DDLOperation)
		ddlMutex.Unlock()

		// Create controllers
		commerceParams := map[string]string{
			"id":              "4",
			"workflow":        "concurrent_commerce_ddl",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		customerParams := map[string]string{
			"id":              "5",
			"workflow":        "concurrent_customer_ddl",
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

		// Perform concurrent DDL operations
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			factory := commerceController.getSchemaSpecificDBClientFactory()
			client := factory()

			ddlStatements := []string{
				"CREATE TABLE products (id INT PRIMARY KEY)",
				"CREATE TABLE categories (id INT PRIMARY KEY)",
				"CREATE TABLE product_categories (product_id INT, category_id INT)",
			}

			for _, ddl := range ddlStatements {
				client.ExecuteFetch(ddl, 1000)
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			factory := customerController.getSchemaSpecificDBClientFactory()
			client := factory()

			ddlStatements := []string{
				"CREATE TABLE customers (id INT PRIMARY KEY)",
				"CREATE TABLE addresses (id INT PRIMARY KEY)",
				"CREATE TABLE customer_addresses (customer_id INT, address_id INT)",
			}

			for _, ddl := range ddlStatements {
				client.ExecuteFetch(ddl, 1000)
			}
		}()

		wg.Wait()

		// Verify concurrent DDL operations
		ddlMutex.Lock()
		defer ddlMutex.Unlock()

		commerceOps := ddlOperations["vt_commerce_0"]
		customerOps := ddlOperations["vt_customer_0"]

		// Verify each schema has its expected operations
		assert.Len(t, commerceOps, 3, "Commerce should have 3 DDL operations")
		assert.Len(t, customerOps, 3, "Customer should have 3 DDL operations")

		// Verify table names are schema-specific
		commerceTables := make(map[string]bool)
		for _, op := range commerceOps {
			commerceTables[op.TableName] = true
			assert.Equal(t, "vt_commerce_0", op.Schema, "Commerce DDL should target commerce schema")
		}

		customerTables := make(map[string]bool)
		for _, op := range customerOps {
			customerTables[op.TableName] = true
			assert.Equal(t, "vt_customer_0", op.Schema, "Customer DDL should target customer schema")
		}

		// Verify expected tables were created
		assert.True(t, commerceTables["products"], "Commerce should have products table")
		assert.True(t, commerceTables["categories"], "Commerce should have categories table")
		assert.True(t, commerceTables["product_categories"], "Commerce should have product_categories table")

		assert.True(t, customerTables["customers"], "Customer should have customers table")
		assert.True(t, customerTables["addresses"], "Customer should have addresses table")
		assert.True(t, customerTables["customer_addresses"], "Customer should have customer_addresses table")
	})
}

// TestSchemaChangeReplication tests schema change replication across virtual keyspaces
func TestSchemaChangeReplication(t *testing.T) {
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

	// Track schema changes
	schemaChanges := make(map[string][]SchemaChange)
	var schemaMutex sync.Mutex

	dbClientFactory := func() binlogplayer.DBClient {
		return &schemaChangeTrackingDBClient{
			dbName:        "vt_main",
			schemaChanges: schemaChanges,
			mutex:         &schemaMutex,
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

	// Test Case 1: Schema change propagation
	t.Run("SchemaChangePropagation", func(t *testing.T) {
		schemaMutex.Lock()
		schemaChanges = make(map[string][]SchemaChange)
		schemaMutex.Unlock()

		// Create controller
		params := map[string]string{
			"id":              "1",
			"workflow":        "schema_change_test",
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

		// Simulate schema changes from source
		schemaChangeSequence := []string{
			"CREATE TABLE products (id INT PRIMARY KEY, name VARCHAR(100))",
			"ALTER TABLE products ADD COLUMN price DECIMAL(10,2)",
			"ALTER TABLE products ADD COLUMN created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP",
			"ALTER TABLE products DROP COLUMN created_at",
			"ALTER TABLE products MODIFY COLUMN price DECIMAL(12,2) NOT NULL",
		}

		for _, change := range schemaChangeSequence {
			_, err := client.ExecuteFetch(change, 1000)
			require.NoError(t, err)
		}

		// Verify schema changes were tracked
		schemaMutex.Lock()
		defer schemaMutex.Unlock()

		commerceChanges := schemaChanges["vt_commerce_0"]
		require.Len(t, commerceChanges, 5, "Should have 5 schema changes")

		// Verify schema change sequence
		expectedChanges := []struct {
			changeType string
			table      string
		}{
			{"CREATE", "products"},
			{"ADD_COLUMN", "products"},
			{"ADD_COLUMN", "products"},
			{"DROP_COLUMN", "products"},
			{"MODIFY_COLUMN", "products"},
		}

		for i, expected := range expectedChanges {
			assert.Equal(t, expected.changeType, commerceChanges[i].ChangeType, "Change %d should be %s", i, expected.changeType)
			assert.Equal(t, expected.table, commerceChanges[i].TableName, "Change %d should target table %s", i, expected.table)
		}
	})

	// Test Case 2: Schema synchronization across virtual keyspaces
	t.Run("SchemaSynchronizationAcrossVirtualKeyspaces", func(t *testing.T) {
		schemaMutex.Lock()
		schemaChanges = make(map[string][]SchemaChange)
		schemaMutex.Unlock()

		// Create controllers for both virtual keyspaces
		commerceParams := map[string]string{
			"id":              "2",
			"workflow":        "commerce_schema_sync",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		customerParams := map[string]string{
			"id":              "3",
			"workflow":        "customer_schema_sync",
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

		// Apply schema changes to both keyspaces
		commerceFactory := commerceController.getSchemaSpecificDBClientFactory()
		customerFactory := customerController.getSchemaSpecificDBClientFactory()

		commerceClient := commerceFactory()
		customerClient := customerFactory()

		// Commerce schema changes
		commerceClient.ExecuteFetch("CREATE TABLE products (id INT PRIMARY KEY, name VARCHAR(100))", 1000)
		commerceClient.ExecuteFetch("ALTER TABLE products ADD COLUMN category_id INT", 1000)

		// Customer schema changes
		customerClient.ExecuteFetch("CREATE TABLE customers (id INT PRIMARY KEY, email VARCHAR(100))", 1000)
		customerClient.ExecuteFetch("ALTER TABLE customers ADD COLUMN status VARCHAR(20)", 1000)

		// Verify schema synchronization
		schemaMutex.Lock()
		defer schemaMutex.Unlock()

		commerceChanges := schemaChanges["vt_commerce_0"]
		customerChanges := schemaChanges["vt_customer_0"]

		// Verify each keyspace has its own schema changes
		assert.Len(t, commerceChanges, 2, "Commerce should have 2 schema changes")
		assert.Len(t, customerChanges, 2, "Customer should have 2 schema changes")

		// Verify schema changes are keyspace-specific
		for _, change := range commerceChanges {
			assert.Equal(t, "products", change.TableName, "Commerce changes should target products table")
			assert.Equal(t, "vt_commerce_0", change.Schema, "Commerce changes should target commerce schema")
		}

		for _, change := range customerChanges {
			assert.Equal(t, "customers", change.TableName, "Customer changes should target customers table")
			assert.Equal(t, "vt_customer_0", change.Schema, "Customer changes should target customer schema")
		}
	})
}

// DDLOperation represents a DDL operation for tracking
type DDLOperation struct {
	OperationType string
	TableName     string
	Schema        string
	Statement     string
}

// SchemaChange represents a schema change for tracking
type SchemaChange struct {
	ChangeType string
	TableName  string
	Schema     string
	Statement  string
}

// ddlTrackingDBClient implements binlogplayer.DBClient for DDL tracking
type ddlTrackingDBClient struct {
	dbName        string
	ddlOperations map[string][]DDLOperation
	mutex         *sync.Mutex
}

func (m *ddlTrackingDBClient) DBName() string {
	return m.dbName
}

func (m *ddlTrackingDBClient) Connect() error {
	return nil
}

func (m *ddlTrackingDBClient) Begin() error {
	return nil
}

func (m *ddlTrackingDBClient) Commit() error {
	return nil
}

func (m *ddlTrackingDBClient) Rollback() error {
	return nil
}

func (m *ddlTrackingDBClient) Close() {
}

func (m *ddlTrackingDBClient) IsClosed() bool {
	return false
}

func (m *ddlTrackingDBClient) ExecuteFetch(query string, maxrows int) (*sqltypes.Result, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Initialize DDL operations map if needed
	if m.ddlOperations[m.dbName] == nil {
		m.ddlOperations[m.dbName] = make([]DDLOperation, 0)
	}

	// Parse DDL operation
	queryUpper := strings.ToUpper(strings.TrimSpace(query))
	var opType, tableName string

	if strings.HasPrefix(queryUpper, "CREATE TABLE") {
		opType = "CREATE TABLE"
		tableName = extractTableName(query, "CREATE TABLE")
	} else if strings.HasPrefix(queryUpper, "ALTER TABLE") {
		opType = "ALTER TABLE"
		tableName = extractTableName(query, "ALTER TABLE")
	} else if strings.HasPrefix(queryUpper, "DROP TABLE") {
		opType = "DROP TABLE"
		tableName = extractTableName(query, "DROP TABLE")
	} else if strings.HasPrefix(queryUpper, "CREATE INDEX") {
		opType = "CREATE INDEX"
		tableName = extractTableNameFromIndex(query)
	} else if strings.HasPrefix(queryUpper, "DROP INDEX") {
		opType = "DROP INDEX"
		tableName = extractTableNameFromIndex(query)
	}

	if opType != "" {
		m.ddlOperations[m.dbName] = append(m.ddlOperations[m.dbName], DDLOperation{
			OperationType: opType,
			TableName:     tableName,
			Schema:        m.dbName,
			Statement:     query,
		})
	}

	return &sqltypes.Result{}, nil
}

func (m *ddlTrackingDBClient) ExecuteFetchMulti(query string, maxrows int) ([]*sqltypes.Result, error) {
	_, err := m.ExecuteFetch(query, maxrows)
	return []*sqltypes.Result{{}}, err
}

func (m *ddlTrackingDBClient) SupportsCapability(capability capabilities.FlavorCapability) (bool, error) {
	return false, nil
}

func (m *ddlTrackingDBClient) SetDBName(dbName string) {
	m.dbName = dbName
}

// schemaChangeTrackingDBClient implements binlogplayer.DBClient for schema change tracking
type schemaChangeTrackingDBClient struct {
	dbName        string
	schemaChanges map[string][]SchemaChange
	mutex         *sync.Mutex
}

func (m *schemaChangeTrackingDBClient) DBName() string {
	return m.dbName
}

func (m *schemaChangeTrackingDBClient) Connect() error {
	return nil
}

func (m *schemaChangeTrackingDBClient) Begin() error {
	return nil
}

func (m *schemaChangeTrackingDBClient) Commit() error {
	return nil
}

func (m *schemaChangeTrackingDBClient) Rollback() error {
	return nil
}

func (m *schemaChangeTrackingDBClient) Close() {
}

func (m *schemaChangeTrackingDBClient) IsClosed() bool {
	return false
}

func (m *schemaChangeTrackingDBClient) ExecuteFetch(query string, maxrows int) (*sqltypes.Result, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Initialize schema changes map if needed
	if m.schemaChanges[m.dbName] == nil {
		m.schemaChanges[m.dbName] = make([]SchemaChange, 0)
	}

	// Parse schema change
	queryUpper := strings.ToUpper(strings.TrimSpace(query))
	var changeType, tableName string

	if strings.HasPrefix(queryUpper, "CREATE TABLE") {
		changeType = "CREATE"
		tableName = extractTableName(query, "CREATE TABLE")
	} else if strings.Contains(queryUpper, "ADD COLUMN") {
		changeType = "ADD_COLUMN"
		tableName = extractTableName(query, "ALTER TABLE")
	} else if strings.Contains(queryUpper, "DROP COLUMN") {
		changeType = "DROP_COLUMN"
		tableName = extractTableName(query, "ALTER TABLE")
	} else if strings.Contains(queryUpper, "MODIFY COLUMN") {
		changeType = "MODIFY_COLUMN"
		tableName = extractTableName(query, "ALTER TABLE")
	} else if strings.HasPrefix(queryUpper, "ALTER TABLE") {
		changeType = "ALTER"
		tableName = extractTableName(query, "ALTER TABLE")
	}

	if changeType != "" {
		m.schemaChanges[m.dbName] = append(m.schemaChanges[m.dbName], SchemaChange{
			ChangeType: changeType,
			TableName:  tableName,
			Schema:     m.dbName,
			Statement:  query,
		})
	}

	return &sqltypes.Result{}, nil
}

func (m *schemaChangeTrackingDBClient) ExecuteFetchMulti(query string, maxrows int) ([]*sqltypes.Result, error) {
	_, err := m.ExecuteFetch(query, maxrows)
	return []*sqltypes.Result{{}}, err
}

func (m *schemaChangeTrackingDBClient) SupportsCapability(capability capabilities.FlavorCapability) (bool, error) {
	return false, nil
}

func (m *schemaChangeTrackingDBClient) SetDBName(dbName string) {
	m.dbName = dbName
}

// Helper functions for parsing table names from DDL statements
func extractTableName(query, prefix string) string {
	queryUpper := strings.ToUpper(strings.TrimSpace(query))
	prefixUpper := strings.ToUpper(prefix)

	if !strings.HasPrefix(queryUpper, prefixUpper) {
		return ""
	}

	remaining := strings.TrimSpace(query[len(prefix):])
	parts := strings.Fields(remaining)
	if len(parts) > 0 {
		return strings.Trim(parts[0], "`")
	}

	return ""
}

func extractTableNameFromIndex(query string) string {
	queryUpper := strings.ToUpper(strings.TrimSpace(query))

	// For CREATE INDEX idx_name ON table_name
	// For DROP INDEX idx_name ON table_name
	onIndex := strings.Index(queryUpper, " ON ")
	if onIndex != -1 {
		remaining := strings.TrimSpace(query[onIndex+4:])
		parts := strings.Fields(remaining)
		if len(parts) > 0 {
			tableName := strings.Trim(parts[0], "`")
			// Remove column specification if present (e.g., "products(name)" -> "products")
			if parenIndex := strings.Index(tableName, "("); parenIndex != -1 {
				tableName = tableName[:parenIndex]
			}
			return tableName
		}
	}

	return ""
}
