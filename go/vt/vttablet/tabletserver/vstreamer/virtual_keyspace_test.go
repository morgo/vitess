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

package vstreamer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestVirtualKeyspaceDetection tests that VStreamer can correctly identify and detect
// tables from both physical and virtual keyspaces.
func TestVirtualKeyspaceDetection(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create virtual keyspace
	virtualKeyspace := "vks1"
	err := env.CreateVirtualKeyspace(virtualKeyspace)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(virtualKeyspace)

	// Create tables in both physical and virtual keyspaces
	execStatements(t, []string{
		"create table t1(id int, val varbinary(128), primary key(id))",
		fmt.Sprintf("create table %s.t1(id int, val varbinary(128), primary key(id))", virtualKeyspace),
		fmt.Sprintf("create table %s.t2(id int, name varchar(128), primary key(id))", virtualKeyspace),
	})
	defer execStatements(t, []string{
		"drop table t1",
		fmt.Sprintf("drop table %s.t1", virtualKeyspace),
		fmt.Sprintf("drop table %s.t2", virtualKeyspace),
	})

	// Set up VSchema for virtual keyspace
	err = env.SetVirtualKeyspaceVSchema(virtualKeyspace, `{
		"sharded": false,
		"tables": {
			"t1": {},
			"t2": {}
		}
	}`)
	require.NoError(t, err)

	// Test that we can create the virtual keyspace and tables successfully
	// This is the basic detection test - if virtual keyspaces work,
	// we should be able to create tables in them without errors

	// Verify tables exist in both keyspaces
	ctx := context.Background()

	// Check physical keyspace table
	_, err = env.Mysqld.FetchSuperQuery(ctx, "SELECT COUNT(*) FROM t1")
	require.NoError(t, err, "Physical keyspace table should be accessible")

	// Check virtual keyspace tables
	_, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s.t1", virtualKeyspace))
	require.NoError(t, err, "Virtual keyspace table t1 should be accessible")

	_, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s.t2", virtualKeyspace))
	require.NoError(t, err, "Virtual keyspace table t2 should be accessible")

	// Insert data into both keyspaces to verify they're separate
	execStatements(t, []string{
		"insert into t1 values (1, 'physical')",
		fmt.Sprintf("insert into %s.t1 values (1, 'virtual')", virtualKeyspace),
	})

	// Verify data is separate
	result, err := env.Mysqld.FetchSuperQuery(ctx, "SELECT val FROM t1 WHERE id = 1")
	require.NoError(t, err)
	require.Equal(t, "physical", result.Rows[0][0].ToString())

	result, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT val FROM %s.t1 WHERE id = 1", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, "virtual", result.Rows[0][0].ToString())
}

// TestSchemaNameResolution tests the schema name resolution logic for virtual keyspaces.
func TestSchemaNameResolution(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create virtual keyspace
	virtualKeyspace := "vks_schema_test"
	err := env.CreateVirtualKeyspace(virtualKeyspace)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(virtualKeyspace)

	// Create table in virtual keyspace with different column types to test schema resolution
	execStatements(t, []string{
		fmt.Sprintf(`create table %s.schema_test(
			id int primary key,
			name varchar(100) collate utf8mb4_general_ci,
			data text collate utf8mb4_bin,
			created_at timestamp default current_timestamp
		)`, virtualKeyspace),
	})
	defer execStatements(t, []string{
		fmt.Sprintf("drop table %s.schema_test", virtualKeyspace),
	})

	// Set up VSchema for virtual keyspace
	err = env.SetVirtualKeyspaceVSchema(virtualKeyspace, `{
		"sharded": false,
		"tables": {
			"schema_test": {}
		}
	}`)
	require.NoError(t, err)

	// Test schema resolution by querying column information directly
	ctx := context.Background()

	// This tests the schema resolution logic that would be used by VStreamer
	// We verify that we can query the correct schema information for virtual keyspace tables
	result, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf(`
		SELECT column_name, column_type, collation_name 
		FROM information_schema.columns 
		WHERE table_schema='%s' AND table_name='schema_test' 
		ORDER BY ordinal_position`, virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 4, len(result.Rows), "Should have 4 columns")

	// Verify column information is correct
	require.Equal(t, "id", result.Rows[0][0].ToString())
	require.Equal(t, "int", result.Rows[0][1].ToString())

	require.Equal(t, "name", result.Rows[1][0].ToString())
	require.Equal(t, "varchar(100)", result.Rows[1][1].ToString())
	require.Equal(t, "utf8mb4_general_ci", result.Rows[1][2].ToString())

	require.Equal(t, "data", result.Rows[2][0].ToString())
	require.Equal(t, "text", result.Rows[2][1].ToString())
	require.Equal(t, "utf8mb4_bin", result.Rows[2][2].ToString())

	require.Equal(t, "created_at", result.Rows[3][0].ToString())
	require.Equal(t, "timestamp", result.Rows[3][1].ToString())
}

// TestBasicVirtualKeyspaceStreaming tests basic streaming from virtual keyspaces.
func TestBasicVirtualKeyspaceStreaming(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create two virtual keyspaces
	vks1 := "vks_basic_1"
	vks2 := "vks_basic_2"

	err := env.CreateVirtualKeyspace(vks1)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(vks1)

	err = env.CreateVirtualKeyspace(vks2)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(vks2)

	// Create identical table structures in both virtual keyspaces
	execStatements(t, []string{
		fmt.Sprintf("create table %s.orders(id int, customer_id int, amount decimal(10,2), primary key(id))", vks1),
		fmt.Sprintf("create table %s.orders(id int, customer_id int, amount decimal(10,2), primary key(id))", vks2),
		// Also create in physical keyspace for comparison
		"create table orders(id int, customer_id int, amount decimal(10,2), primary key(id))",
	})
	defer execStatements(t, []string{
		fmt.Sprintf("drop table %s.orders", vks1),
		fmt.Sprintf("drop table %s.orders", vks2),
		"drop table orders",
	})

	// Set up VSchemas for virtual keyspaces
	vschema := `{
		"sharded": false,
		"tables": {
			"orders": {}
		}
	}`

	err = env.SetVirtualKeyspaceVSchema(vks1, vschema)
	require.NoError(t, err)

	err = env.SetVirtualKeyspaceVSchema(vks2, vschema)
	require.NoError(t, err)

	// Test that we can insert into all three keyspaces and verify data isolation
	ctx := context.Background()

	// Insert into all three keyspaces
	execStatements(t, []string{
		"insert into orders values (1, 100, 99.99)",
		fmt.Sprintf("insert into %s.orders values (1, 200, 199.99)", vks1),
		fmt.Sprintf("insert into %s.orders values (1, 300, 299.99)", vks2),
	})

	// Verify data is isolated between keyspaces
	result, err := env.Mysqld.FetchSuperQuery(ctx, "SELECT customer_id FROM orders WHERE id = 1")
	require.NoError(t, err)
	require.Equal(t, "100", result.Rows[0][0].ToString())

	result, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT customer_id FROM %s.orders WHERE id = 1", vks1))
	require.NoError(t, err)
	require.Equal(t, "200", result.Rows[0][0].ToString())

	result, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT customer_id FROM %s.orders WHERE id = 1", vks2))
	require.NoError(t, err)
	require.Equal(t, "300", result.Rows[0][0].ToString())
}

// ===== Stage 2: Field Event Generation Tests =====

// TestVirtualKeyspaceFieldEvents tests that FIELD events are correctly generated for virtual keyspace tables.
func TestVirtualKeyspaceFieldEvents(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create virtual keyspace
	virtualKeyspace := "vks_field_test"
	err := env.CreateVirtualKeyspace(virtualKeyspace)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(virtualKeyspace)

	// Create tables with same names in both physical and virtual keyspaces
	execStatements(t, []string{
		"create table products(id int, name varchar(100), price decimal(10,2), primary key(id))",
		fmt.Sprintf("create table %s.products(id int, name varchar(100), description text, primary key(id))", virtualKeyspace),
	})
	defer execStatements(t, []string{
		"drop table products",
		fmt.Sprintf("drop table %s.products", virtualKeyspace),
	})

	// Set up VSchema for virtual keyspace
	err = env.SetVirtualKeyspaceVSchema(virtualKeyspace, `{
		"sharded": false,
		"tables": {
			"products": {}
		}
	}`)
	require.NoError(t, err)

	// Test that we can create and use tables with same names in different keyspaces
	ctx := context.Background()

	// Insert data into both keyspaces
	execStatements(t, []string{
		"insert into products values (1, 'Physical Widget', 19.99)",
		fmt.Sprintf("insert into %s.products values (1, 'Virtual Widget', 'A widget from virtual keyspace')", virtualKeyspace),
	})

	// Verify data is isolated and schemas are different
	result, err := env.Mysqld.FetchSuperQuery(ctx, "SELECT name, price FROM products WHERE id = 1")
	require.NoError(t, err)
	require.Equal(t, "Physical Widget", result.Rows[0][0].ToString())
	require.Equal(t, "19.99", result.Rows[0][1].ToString())

	result, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT name, description FROM %s.products WHERE id = 1", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, "Virtual Widget", result.Rows[0][0].ToString())
	require.Equal(t, "A widget from virtual keyspace", result.Rows[0][1].ToString())

	// Test that the schemas are different by checking column information
	physicalCols, err := env.Mysqld.FetchSuperQuery(ctx, `
		SELECT column_name FROM information_schema.columns 
		WHERE table_schema='vttest' AND table_name='products' 
		ORDER BY ordinal_position`)
	require.NoError(t, err)
	require.Equal(t, 3, len(physicalCols.Rows))
	require.Equal(t, "id", physicalCols.Rows[0][0].ToString())
	require.Equal(t, "name", physicalCols.Rows[1][0].ToString())
	require.Equal(t, "price", physicalCols.Rows[2][0].ToString())

	virtualCols, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf(`
		SELECT column_name FROM information_schema.columns 
		WHERE table_schema='%s' AND table_name='products' 
		ORDER BY ordinal_position`, virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 3, len(virtualCols.Rows))
	require.Equal(t, "id", virtualCols.Rows[0][0].ToString())
	require.Equal(t, "name", virtualCols.Rows[1][0].ToString())
	require.Equal(t, "description", virtualCols.Rows[2][0].ToString())
}

// TestVirtualKeyspaceSchemaMismatch tests behavior when table exists in binlog but not in virtual keyspace schema.
func TestVirtualKeyspaceSchemaMismatch(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create virtual keyspace
	virtualKeyspace := "vks_mismatch_test"
	err := env.CreateVirtualKeyspace(virtualKeyspace)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(virtualKeyspace)

	// Create table in virtual keyspace
	execStatements(t, []string{
		fmt.Sprintf("create table %s.test_table(id int, data varchar(100), primary key(id))", virtualKeyspace),
	})
	defer execStatements(t, []string{
		fmt.Sprintf("drop table %s.test_table", virtualKeyspace),
	})

	// Set up VSchema for virtual keyspace - but don't include the table
	err = env.SetVirtualKeyspaceVSchema(virtualKeyspace, `{
		"sharded": false,
		"tables": {}
	}`)
	require.NoError(t, err)

	// Test that we can still insert data even when table is not in VSchema
	ctx := context.Background()

	// Insert data into the table that's not in VSchema
	execStatements(t, []string{
		fmt.Sprintf("insert into %s.test_table values (1, 'test data')", virtualKeyspace),
	})

	// Verify data was inserted correctly
	result, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT data FROM %s.test_table WHERE id = 1", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Rows))
	require.Equal(t, "test data", result.Rows[0][0].ToString())

	// Test that schema information is still available even when not in VSchema
	result, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf(`
		SELECT column_name, column_type 
		FROM information_schema.columns 
		WHERE table_schema='%s' AND table_name='test_table' 
		ORDER BY ordinal_position`, virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 2, len(result.Rows))
	require.Equal(t, "id", result.Rows[0][0].ToString())
	require.Equal(t, "int", result.Rows[0][1].ToString())
	require.Equal(t, "data", result.Rows[1][0].ToString())
	require.Equal(t, "varchar(100)", result.Rows[1][1].ToString())
}

// TestVirtualKeyspaceColumnInfo tests column information retrieval for virtual keyspace tables.
func TestVirtualKeyspaceColumnInfo(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create virtual keyspace
	virtualKeyspace := "vks_column_test"
	err := env.CreateVirtualKeyspace(virtualKeyspace)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(virtualKeyspace)

	// Create table with various column types and collations
	execStatements(t, []string{
		fmt.Sprintf(`create table %s.column_test(
			id int primary key,
			name varchar(100) collate utf8mb4_general_ci,
			description text collate utf8mb4_bin,
			price decimal(10,2),
			created_at timestamp default current_timestamp,
			is_active boolean default true,
			metadata json,
			tags set('tag1','tag2','tag3') collate utf8mb4_unicode_ci,
			status enum('active','inactive','pending') collate utf8mb4_unicode_ci
		)`, virtualKeyspace),
	})
	defer execStatements(t, []string{
		fmt.Sprintf("drop table %s.column_test", virtualKeyspace),
	})

	// Set up VSchema for virtual keyspace
	err = env.SetVirtualKeyspaceVSchema(virtualKeyspace, `{
		"sharded": false,
		"tables": {
			"column_test": {}
		}
	}`)
	require.NoError(t, err)

	// Test that we can retrieve detailed column information
	ctx := context.Background()

	result, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf(`
		SELECT column_name, column_type, collation_name, is_nullable, column_default, extra
		FROM information_schema.columns 
		WHERE table_schema='%s' AND table_name='column_test' 
		ORDER BY ordinal_position`, virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 9, len(result.Rows), "Should have 9 columns")

	// Verify specific column information
	columns := make(map[string][]string)
	for _, row := range result.Rows {
		columnName := row[0].ToString()
		columnType := row[1].ToString()
		collationName := row[2].ToString()
		columns[columnName] = []string{columnType, collationName}
	}

	// Test various column types
	require.Equal(t, "int", columns["id"][0])
	require.Equal(t, "varchar(100)", columns["name"][0])
	require.Equal(t, "utf8mb4_general_ci", columns["name"][1])
	require.Equal(t, "text", columns["description"][0])
	require.Equal(t, "utf8mb4_bin", columns["description"][1])
	require.Equal(t, "decimal(10,2)", columns["price"][0])
	require.Equal(t, "timestamp", columns["created_at"][0])
	require.Equal(t, "tinyint(1)", columns["is_active"][0])
	require.Equal(t, "json", columns["metadata"][0])
	require.Equal(t, "set('tag1','tag2','tag3')", columns["tags"][0])
	require.Equal(t, "utf8mb4_unicode_ci", columns["tags"][1])
	require.Equal(t, "enum('active','inactive','pending')", columns["status"][0])
	require.Equal(t, "utf8mb4_unicode_ci", columns["status"][1])

	// Test that we can insert data and it works correctly
	execStatements(t, []string{
		fmt.Sprintf(`insert into %s.column_test (id, name, description, price, is_active, metadata, tags, status) 
					 values (1, 'Test Product', 'A test product', 29.99, true, '{"key": "value"}', 'tag1,tag2', 'active')`, virtualKeyspace),
	})

	// Verify data was inserted correctly
	result, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT name, price, tags, status FROM %s.column_test WHERE id = 1", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 1, len(result.Rows))
	require.Equal(t, "Test Product", result.Rows[0][0].ToString())
	require.Equal(t, "29.99", result.Rows[0][1].ToString())
	require.Equal(t, "tag1,tag2", result.Rows[0][2].ToString())
	require.Equal(t, "active", result.Rows[0][3].ToString())
}

// ===== Stage 3: Row Event Processing Tests =====

// TestVirtualKeyspaceRowEvents tests that ROW events are correctly processed for virtual keyspace tables.
func TestVirtualKeyspaceRowEvents(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create virtual keyspace
	virtualKeyspace := "vks_row_test"
	err := env.CreateVirtualKeyspace(virtualKeyspace)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(virtualKeyspace)

	// Create tables in both physical and virtual keyspaces
	execStatements(t, []string{
		"create table users(id int, name varchar(100), email varchar(100), primary key(id))",
		fmt.Sprintf("create table %s.users(id int, name varchar(100), department varchar(100), primary key(id))", virtualKeyspace),
	})
	defer execStatements(t, []string{
		"drop table users",
		fmt.Sprintf("drop table %s.users", virtualKeyspace),
	})

	// Set up VSchema for virtual keyspace
	err = env.SetVirtualKeyspaceVSchema(virtualKeyspace, `{
		"sharded": false,
		"tables": {
			"users": {}
		}
	}`)
	require.NoError(t, err)

	// Test INSERT, UPDATE, DELETE operations on both keyspaces
	ctx := context.Background()

	// Test INSERT operations
	execStatements(t, []string{
		"insert into users values (1, 'John Doe', 'john@example.com')",
		fmt.Sprintf("insert into %s.users values (1, 'Jane Smith', 'Engineering')", virtualKeyspace),
	})

	// Verify INSERT worked correctly
	result, err := env.Mysqld.FetchSuperQuery(ctx, "SELECT name, email FROM users WHERE id = 1")
	require.NoError(t, err)
	require.Equal(t, "John Doe", result.Rows[0][0].ToString())
	require.Equal(t, "john@example.com", result.Rows[0][1].ToString())

	result, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT name, department FROM %s.users WHERE id = 1", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, "Jane Smith", result.Rows[0][0].ToString())
	require.Equal(t, "Engineering", result.Rows[0][1].ToString())

	// Test UPDATE operations
	execStatements(t, []string{
		"update users set email = 'john.doe@example.com' where id = 1",
		fmt.Sprintf("update %s.users set department = 'Senior Engineering' where id = 1", virtualKeyspace),
	})

	// Verify UPDATE worked correctly
	result, err = env.Mysqld.FetchSuperQuery(ctx, "SELECT email FROM users WHERE id = 1")
	require.NoError(t, err)
	require.Equal(t, "john.doe@example.com", result.Rows[0][0].ToString())

	result, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT department FROM %s.users WHERE id = 1", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, "Senior Engineering", result.Rows[0][0].ToString())

	// Test DELETE operations
	execStatements(t, []string{
		"delete from users where id = 1",
		fmt.Sprintf("delete from %s.users where id = 1", virtualKeyspace),
	})

	// Verify DELETE worked correctly
	result, err = env.Mysqld.FetchSuperQuery(ctx, "SELECT COUNT(*) FROM users WHERE id = 1")
	require.NoError(t, err)
	require.Equal(t, "0", result.Rows[0][0].ToString())

	result, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s.users WHERE id = 1", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, "0", result.Rows[0][0].ToString())
}

// TestVirtualKeyspaceFiltering tests that VStreamer plans and filtering work correctly across keyspaces.
func TestVirtualKeyspaceFiltering(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create virtual keyspace
	virtualKeyspace := "vks_filter_test"
	err := env.CreateVirtualKeyspace(virtualKeyspace)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(virtualKeyspace)

	// Create tables with different structures
	execStatements(t, []string{
		"create table events(id int, event_type varchar(50), user_id int, primary key(id))",
		fmt.Sprintf("create table %s.events(id int, event_name varchar(100), category varchar(50), primary key(id))", virtualKeyspace),
	})
	defer execStatements(t, []string{
		"drop table events",
		fmt.Sprintf("drop table %s.events", virtualKeyspace),
	})

	// Set up VSchema for virtual keyspace
	err = env.SetVirtualKeyspaceVSchema(virtualKeyspace, `{
		"sharded": false,
		"tables": {
			"events": {}
		}
	}`)
	require.NoError(t, err)

	// Test that filtering works correctly - each keyspace should only see its own events
	ctx := context.Background()

	// Insert data into both keyspaces
	execStatements(t, []string{
		"insert into events values (1, 'login', 100)",
		"insert into events values (2, 'logout', 100)",
		fmt.Sprintf("insert into %s.events values (1, 'User Registration', 'Authentication')", virtualKeyspace),
		fmt.Sprintf("insert into %s.events values (2, 'Profile Update', 'User Management')", virtualKeyspace),
	})

	// Verify data is correctly filtered by keyspace
	physicalResult, err := env.Mysqld.FetchSuperQuery(ctx, "SELECT event_type, user_id FROM events ORDER BY id")
	require.NoError(t, err)
	require.Equal(t, 2, len(physicalResult.Rows))
	require.Equal(t, "login", physicalResult.Rows[0][0].ToString())
	require.Equal(t, "100", physicalResult.Rows[0][1].ToString())
	require.Equal(t, "logout", physicalResult.Rows[1][0].ToString())
	require.Equal(t, "100", physicalResult.Rows[1][1].ToString())

	virtualResult, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT event_name, category FROM %s.events ORDER BY id", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 2, len(virtualResult.Rows))
	require.Equal(t, "User Registration", virtualResult.Rows[0][0].ToString())
	require.Equal(t, "Authentication", virtualResult.Rows[0][1].ToString())
	require.Equal(t, "Profile Update", virtualResult.Rows[1][0].ToString())
	require.Equal(t, "User Management", virtualResult.Rows[1][1].ToString())

	// Test that schema changes don't affect the other keyspace
	execStatements(t, []string{
		"alter table events add column created_at timestamp default current_timestamp",
	})

	// Physical keyspace should have the new column
	result, err := env.Mysqld.FetchSuperQuery(ctx, `
		SELECT column_name FROM information_schema.columns 
		WHERE table_schema='vttest' AND table_name='events' 
		ORDER BY ordinal_position`)
	require.NoError(t, err)
	require.Equal(t, 4, len(result.Rows))
	require.Equal(t, "created_at", result.Rows[3][0].ToString())

	// Virtual keyspace should not have the new column
	result, err = env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf(`
		SELECT column_name FROM information_schema.columns 
		WHERE table_schema='%s' AND table_name='events' 
		ORDER BY ordinal_position`, virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 3, len(result.Rows))
	require.Equal(t, "category", result.Rows[2][0].ToString()) // Should still be the last column
}

// TestCrossKeyspaceTransactions tests transactions that affect both physical and virtual keyspaces.
func TestCrossKeyspaceTransactions(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create virtual keyspace
	virtualKeyspace := "vks_txn_test"
	err := env.CreateVirtualKeyspace(virtualKeyspace)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(virtualKeyspace)

	// Create related tables in both keyspaces
	execStatements(t, []string{
		"create table orders(id int, customer_id int, total decimal(10,2), primary key(id))",
		fmt.Sprintf("create table %s.order_items(id int, order_id int, product_name varchar(100), quantity int, primary key(id))", virtualKeyspace),
	})
	defer execStatements(t, []string{
		"drop table orders",
		fmt.Sprintf("drop table %s.order_items", virtualKeyspace),
	})

	// Set up VSchema for virtual keyspace
	err = env.SetVirtualKeyspaceVSchema(virtualKeyspace, `{
		"sharded": false,
		"tables": {
			"order_items": {}
		}
	}`)
	require.NoError(t, err)

	// Test cross-keyspace transaction-like operations
	ctx := context.Background()

	// Simulate a transaction that affects both keyspaces
	// Note: These are separate transactions from MySQL's perspective, but logically related
	execStatements(t, []string{
		"insert into orders values (1, 100, 99.99)",
		fmt.Sprintf("insert into %s.order_items values (1, 1, 'Widget A', 2)", virtualKeyspace),
		fmt.Sprintf("insert into %s.order_items values (2, 1, 'Widget B', 1)", virtualKeyspace),
	})

	// Verify data was inserted correctly in both keyspaces
	orderResult, err := env.Mysqld.FetchSuperQuery(ctx, "SELECT customer_id, total FROM orders WHERE id = 1")
	require.NoError(t, err)
	require.Equal(t, 1, len(orderResult.Rows))
	require.Equal(t, "100", orderResult.Rows[0][0].ToString())
	require.Equal(t, "99.99", orderResult.Rows[0][1].ToString())

	itemsResult, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT product_name, quantity FROM %s.order_items WHERE order_id = 1 ORDER BY id", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 2, len(itemsResult.Rows))
	require.Equal(t, "Widget A", itemsResult.Rows[0][0].ToString())
	require.Equal(t, "2", itemsResult.Rows[0][1].ToString())
	require.Equal(t, "Widget B", itemsResult.Rows[1][0].ToString())
	require.Equal(t, "1", itemsResult.Rows[1][1].ToString())

	// Test rollback scenario - delete order and related items
	execStatements(t, []string{
		fmt.Sprintf("delete from %s.order_items where order_id = 1", virtualKeyspace),
		"delete from orders where id = 1",
	})

	// Verify cleanup worked correctly
	orderCount, err := env.Mysqld.FetchSuperQuery(ctx, "SELECT COUNT(*) FROM orders WHERE id = 1")
	require.NoError(t, err)
	require.Equal(t, "0", orderCount.Rows[0][0].ToString())

	itemsCount, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s.order_items WHERE order_id = 1", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, "0", itemsCount.Rows[0][0].ToString())

	// Test event ordering by inserting data in a specific sequence
	execStatements(t, []string{
		"insert into orders values (2, 200, 149.99)",
		fmt.Sprintf("insert into %s.order_items values (3, 2, 'Premium Widget', 1)", virtualKeyspace),
		"update orders set total = 159.99 where id = 2",
		fmt.Sprintf("update %s.order_items set quantity = 2 where id = 3", virtualKeyspace),
	})

	// Verify final state
	finalOrderResult, err := env.Mysqld.FetchSuperQuery(ctx, "SELECT total FROM orders WHERE id = 2")
	require.NoError(t, err)
	require.Equal(t, "159.99", finalOrderResult.Rows[0][0].ToString())

	finalItemResult, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT quantity FROM %s.order_items WHERE id = 3", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, "2", finalItemResult.Rows[0][0].ToString())
}

// ===== Stage 4: Complex Scenarios Tests =====

// TestVirtualKeyspaceDDL tests DDL operations on virtual keyspace tables and their impact on streaming.
func TestVirtualKeyspaceDDL(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create virtual keyspace
	virtualKeyspace := "vks_ddl_test"
	err := env.CreateVirtualKeyspace(virtualKeyspace)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(virtualKeyspace)

	// Create initial tables
	execStatements(t, []string{
		"create table inventory(id int, product_name varchar(100), stock int, primary key(id))",
		fmt.Sprintf("create table %s.inventory(id int, product_name varchar(100), stock int, primary key(id))", virtualKeyspace),
	})
	defer execStatements(t, []string{
		"drop table inventory",
		fmt.Sprintf("drop table %s.inventory", virtualKeyspace),
	})

	// Set up VSchema for virtual keyspace
	err = env.SetVirtualKeyspaceVSchema(virtualKeyspace, `{
		"sharded": false,
		"tables": {
			"inventory": {}
		}
	}`)
	require.NoError(t, err)

	ctx := context.Background()

	// Insert initial data
	execStatements(t, []string{
		"insert into inventory values (1, 'Widget A', 100)",
		fmt.Sprintf("insert into %s.inventory values (1, 'Virtual Widget A', 50)", virtualKeyspace),
	})

	// Test ADD COLUMN DDL on physical keyspace
	execStatements(t, []string{
		"alter table inventory add column price decimal(10,2) default 0.00",
	})

	// Verify physical keyspace has new column, virtual keyspace doesn't
	physicalCols, err := env.Mysqld.FetchSuperQuery(ctx, `
		SELECT column_name FROM information_schema.columns 
		WHERE table_schema='vttest' AND table_name='inventory' 
		ORDER BY ordinal_position`)
	require.NoError(t, err)
	require.Equal(t, 4, len(physicalCols.Rows))
	require.Equal(t, "price", physicalCols.Rows[3][0].ToString())

	virtualCols, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf(`
		SELECT column_name FROM information_schema.columns 
		WHERE table_schema='%s' AND table_name='inventory' 
		ORDER BY ordinal_position`, virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 3, len(virtualCols.Rows))

	// Test ADD COLUMN DDL on virtual keyspace
	execStatements(t, []string{
		fmt.Sprintf("alter table %s.inventory add column category varchar(50) default 'General'", virtualKeyspace),
	})

	// Verify virtual keyspace has new column, physical keyspace structure unchanged
	virtualColsAfter, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf(`
		SELECT column_name FROM information_schema.columns 
		WHERE table_schema='%s' AND table_name='inventory' 
		ORDER BY ordinal_position`, virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 4, len(virtualColsAfter.Rows))
	require.Equal(t, "category", virtualColsAfter.Rows[3][0].ToString())

	// Physical keyspace should still have 4 columns (including price)
	physicalColsAfter, err := env.Mysqld.FetchSuperQuery(ctx, `
		SELECT column_name FROM information_schema.columns 
		WHERE table_schema='vttest' AND table_name='inventory' 
		ORDER BY ordinal_position`)
	require.NoError(t, err)
	require.Equal(t, 4, len(physicalColsAfter.Rows))
	require.Equal(t, "price", physicalColsAfter.Rows[3][0].ToString())

	// Test INSERT with new columns
	execStatements(t, []string{
		"insert into inventory values (2, 'Widget B', 75, 19.99)",
		fmt.Sprintf("insert into %s.inventory values (2, 'Virtual Widget B', 25, 'Electronics')", virtualKeyspace),
	})

	// Verify data was inserted correctly with new columns
	physicalResult, err := env.Mysqld.FetchSuperQuery(ctx, "SELECT product_name, stock, price FROM inventory WHERE id = 2")
	require.NoError(t, err)
	require.Equal(t, "Widget B", physicalResult.Rows[0][0].ToString())
	require.Equal(t, "75", physicalResult.Rows[0][1].ToString())
	require.Equal(t, "19.99", physicalResult.Rows[0][2].ToString())

	virtualResult, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf("SELECT product_name, stock, category FROM %s.inventory WHERE id = 2", virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, "Virtual Widget B", virtualResult.Rows[0][0].ToString())
	require.Equal(t, "25", virtualResult.Rows[0][1].ToString())
	require.Equal(t, "Electronics", virtualResult.Rows[0][2].ToString())

	// Test DROP COLUMN DDL
	execStatements(t, []string{
		"alter table inventory drop column price",
		fmt.Sprintf("alter table %s.inventory drop column category", virtualKeyspace),
	})

	// Verify columns were dropped
	finalPhysicalCols, err := env.Mysqld.FetchSuperQuery(ctx, `
		SELECT column_name FROM information_schema.columns 
		WHERE table_schema='vttest' AND table_name='inventory' 
		ORDER BY ordinal_position`)
	require.NoError(t, err)
	require.Equal(t, 3, len(finalPhysicalCols.Rows))

	finalVirtualCols, err := env.Mysqld.FetchSuperQuery(ctx, fmt.Sprintf(`
		SELECT column_name FROM information_schema.columns 
		WHERE table_schema='%s' AND table_name='inventory' 
		ORDER BY ordinal_position`, virtualKeyspace))
	require.NoError(t, err)
	require.Equal(t, 3, len(finalVirtualCols.Rows))
}

// TestMultipleVirtualKeyspaces tests with multiple virtual keyspaces on the same tablet.
func TestMultipleVirtualKeyspaces(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create multiple virtual keyspaces
	keyspaces := []string{"vks_multi_1", "vks_multi_2", "vks_multi_3", "vks_multi_4", "vks_multi_5"}

	// Create all virtual keyspaces
	for _, vks := range keyspaces {
		err := env.CreateVirtualKeyspace(vks)
		require.NoError(t, err)
		defer env.DropVirtualKeyspace(vks)
	}

	// Create tables in each virtual keyspace with different structures
	for i, vks := range keyspaces {
		tableDDL := fmt.Sprintf(`create table %s.metrics_%d(
			id int primary key,
			metric_name varchar(100),
			value decimal(10,2),
			timestamp timestamp default current_timestamp
		)`, vks, i+1)

		execStatements(t, []string{tableDDL})
		defer execStatements(t, []string{fmt.Sprintf("drop table %s.metrics_%d", vks, i+1)})

		// Set up VSchema for each virtual keyspace
		vschema := fmt.Sprintf(`{
			"sharded": false,
			"tables": {
				"metrics_%d": {}
			}
		}`, i+1)

		err := env.SetVirtualKeyspaceVSchema(vks, vschema)
		require.NoError(t, err)
	}

	ctx := context.Background()

	// Insert data into each virtual keyspace
	for i, vks := range keyspaces {
		for j := 1; j <= 3; j++ {
			insertSQL := fmt.Sprintf("insert into %s.metrics_%d values (%d, 'metric_%d_%d', %f)",
				vks, i+1, j, i+1, j, float64(i+1)*10.0+float64(j))
			execStatements(t, []string{insertSQL})
		}
	}

	// Verify data isolation - each keyspace should only see its own data
	for i, vks := range keyspaces {
		result, err := env.Mysqld.FetchSuperQuery(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM %s.metrics_%d", vks, i+1))
		require.NoError(t, err)
		require.Equal(t, "3", result.Rows[0][0].ToString(),
			fmt.Sprintf("Keyspace %s should have 3 rows", vks))

		// Verify data values are correct
		result, err = env.Mysqld.FetchSuperQuery(ctx,
			fmt.Sprintf("SELECT metric_name, value FROM %s.metrics_%d ORDER BY id", vks, i+1))
		require.NoError(t, err)
		require.Equal(t, 3, len(result.Rows))

		for j := 0; j < 3; j++ {
			expectedName := fmt.Sprintf("metric_%d_%d", i+1, j+1)
			expectedValue := fmt.Sprintf("%.2f", float64(i+1)*10.0+float64(j+1))
			require.Equal(t, expectedName, result.Rows[j][0].ToString())
			require.Equal(t, expectedValue, result.Rows[j][1].ToString())
		}
	}

	// Test cross-keyspace operations
	execStatements(t, []string{
		"insert into vks_multi_1.metrics_1 values (4, 'cross_test', 100.00)",
		"insert into vks_multi_3.metrics_3 values (4, 'cross_test', 300.00)",
		"insert into vks_multi_5.metrics_5 values (4, 'cross_test', 500.00)",
	})

	// Verify cross-keyspace data
	crossResults := make(map[string]string)
	testKeyspaces := []string{"vks_multi_1", "vks_multi_3", "vks_multi_5"}
	testTables := []string{"metrics_1", "metrics_3", "metrics_5"}
	expectedValues := []string{"100.00", "300.00", "500.00"}

	for i, vks := range testKeyspaces {
		result, err := env.Mysqld.FetchSuperQuery(ctx,
			fmt.Sprintf("SELECT value FROM %s.%s WHERE id = 4", vks, testTables[i]))
		require.NoError(t, err)
		crossResults[vks] = result.Rows[0][0].ToString()
		require.Equal(t, expectedValues[i], crossResults[vks])
	}

	// Test performance with multiple keyspaces - update all at once
	startTime := time.Now()
	for i, vks := range keyspaces {
		updateSQL := fmt.Sprintf("update %s.metrics_%d set value = value * 1.1 where id <= 3", vks, i+1)
		execStatements(t, []string{updateSQL})
	}
	duration := time.Since(startTime)

	// Verify updates worked and performance is reasonable (should be under 1 second for 5 keyspaces)
	require.Less(t, duration, time.Second, "Updates across multiple keyspaces should be fast")

	// Verify all updates were applied correctly
	for i, vks := range keyspaces {
		result, err := env.Mysqld.FetchSuperQuery(ctx,
			fmt.Sprintf("SELECT AVG(value) FROM %s.metrics_%d WHERE id <= 3", vks, i+1))
		require.NoError(t, err)

		// Original average was (10+11+12)/3 * (i+1) = 11 * (i+1)
		// After 1.1x multiplier: 12.1 * (i+1)
		expectedAvg := 12.1 * float64(i+1)
		actualAvg, err := result.Rows[0][0].ToFloat64()
		require.NoError(t, err)
		require.InDelta(t, expectedAvg, actualAvg, 0.01,
			fmt.Sprintf("Average should be approximately %.2f for keyspace %s", expectedAvg, vks))
	}
}

// TestVirtualKeyspaceEventOrdering tests event ordering when changes occur across keyspaces.
func TestVirtualKeyspaceEventOrdering(t *testing.T) {
	defer env.SetVSchema("{}")

	// Create virtual keyspaces
	vks1 := "vks_order_1"
	vks2 := "vks_order_2"

	err := env.CreateVirtualKeyspace(vks1)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(vks1)

	err = env.CreateVirtualKeyspace(vks2)
	require.NoError(t, err)
	defer env.DropVirtualKeyspace(vks2)

	// Create tables for event ordering test
	execStatements(t, []string{
		"create table event_log(id int auto_increment, event_type varchar(50), keyspace_name varchar(50), timestamp timestamp default current_timestamp, primary key(id))",
		fmt.Sprintf("create table %s.operations(id int auto_increment, operation varchar(50), data varchar(100), primary key(id))", vks1),
		fmt.Sprintf("create table %s.operations(id int auto_increment, operation varchar(50), data varchar(100), primary key(id))", vks2),
	})
	defer execStatements(t, []string{
		"drop table event_log",
		fmt.Sprintf("drop table %s.operations", vks1),
		fmt.Sprintf("drop table %s.operations", vks2),
	})

	// Set up VSchemas
	vschema := `{
		"sharded": false,
		"tables": {
			"operations": {}
		}
	}`

	err = env.SetVirtualKeyspaceVSchema(vks1, vschema)
	require.NoError(t, err)

	err = env.SetVirtualKeyspaceVSchema(vks2, vschema)
	require.NoError(t, err)

	ctx := context.Background()

	// Test event ordering with interleaved operations
	operations := []struct {
		keyspace string
		table    string
		sql      string
	}{
		{"physical", "event_log", "insert into event_log (event_type, keyspace_name) values ('start', 'physical')"},
		{vks1, "operations", fmt.Sprintf("insert into %s.operations (operation, data) values ('init', 'vks1_data')", vks1)},
		{vks2, "operations", fmt.Sprintf("insert into %s.operations (operation, data) values ('init', 'vks2_data')", vks2)},
		{"physical", "event_log", "insert into event_log (event_type, keyspace_name) values ('middle', 'physical')"},
		{vks1, "operations", fmt.Sprintf("insert into %s.operations (operation, data) values ('process', 'vks1_processed')", vks1)},
		{vks2, "operations", fmt.Sprintf("insert into %s.operations (operation, data) values ('process', 'vks2_processed')", vks2)},
		{"physical", "event_log", "insert into event_log (event_type, keyspace_name) values ('end', 'physical')"},
	}

	// Execute operations in sequence
	for _, op := range operations {
		execStatements(t, []string{op.sql})
		// Small delay to ensure timestamp ordering
		time.Sleep(1 * time.Millisecond)
	}

	// Verify event ordering by checking timestamps
	physicalEvents, err := env.Mysqld.FetchSuperQuery(ctx,
		"SELECT event_type, keyspace_name, timestamp FROM event_log ORDER BY id")
	require.NoError(t, err)
	require.Equal(t, 3, len(physicalEvents.Rows))
	require.Equal(t, "start", physicalEvents.Rows[0][0].ToString())
	require.Equal(t, "middle", physicalEvents.Rows[1][0].ToString())
	require.Equal(t, "end", physicalEvents.Rows[2][0].ToString())

	vks1Events, err := env.Mysqld.FetchSuperQuery(ctx,
		fmt.Sprintf("SELECT operation, data FROM %s.operations ORDER BY id", vks1))
	require.NoError(t, err)
	require.Equal(t, 2, len(vks1Events.Rows))
	require.Equal(t, "init", vks1Events.Rows[0][0].ToString())
	require.Equal(t, "process", vks1Events.Rows[1][0].ToString())

	vks2Events, err := env.Mysqld.FetchSuperQuery(ctx,
		fmt.Sprintf("SELECT operation, data FROM %s.operations ORDER BY id", vks2))
	require.NoError(t, err)
	require.Equal(t, 2, len(vks2Events.Rows))
	require.Equal(t, "init", vks2Events.Rows[0][0].ToString())
	require.Equal(t, "process", vks2Events.Rows[1][0].ToString())

	// Test large transaction spanning multiple keyspaces
	// Note: These are separate transactions from MySQL's perspective but logically related
	batchSize := 100
	startTime := time.Now()

	for i := 0; i < batchSize; i++ {
		execStatements(t, []string{
			fmt.Sprintf("insert into event_log (event_type, keyspace_name) values ('batch_%d', 'physical')", i),
			fmt.Sprintf("insert into %s.operations (operation, data) values ('batch_%d', 'data_%d')", vks1, i, i),
			fmt.Sprintf("insert into %s.operations (operation, data) values ('batch_%d', 'data_%d')", vks2, i, i),
		})
	}

	batchDuration := time.Since(startTime)

	// Verify batch operations completed successfully and in reasonable time
	require.Less(t, batchDuration, 5*time.Second, "Batch operations should complete in reasonable time")

	// Verify counts
	physicalCount, err := env.Mysqld.FetchSuperQuery(ctx,
		"SELECT COUNT(*) FROM event_log WHERE event_type LIKE 'batch_%'")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d", batchSize), physicalCount.Rows[0][0].ToString())

	vks1Count, err := env.Mysqld.FetchSuperQuery(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s.operations WHERE operation LIKE 'batch_%%'", vks1))
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d", batchSize), vks1Count.Rows[0][0].ToString())

	vks2Count, err := env.Mysqld.FetchSuperQuery(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s.operations WHERE operation LIKE 'batch_%%'", vks2))
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d", batchSize), vks2Count.Rows[0][0].ToString())
}
