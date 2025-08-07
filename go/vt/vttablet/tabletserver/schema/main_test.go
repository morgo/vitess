/*
Copyright 2020 The Vitess Authors.

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

package schema

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/fakesqldb"
	"vitess.io/vitess/go/sqltypes"
)

func getTestSchemaEngine(t *testing.T, schemaMaxAgeSeconds int64) (*Engine, *fakesqldb.DB, string, func()) {
	db := fakesqldb.New(t)
	db.AddQuery("select unix_timestamp()", sqltypes.MakeTestResult(sqltypes.MakeTestFields(
		"t",
		"int64"),
		"1427325876",
	))
	db.AddQueryPattern(baseInnoDBTableSizesPattern, &sqltypes.Result{})
	// Add dual table to the show tables result to prevent it from being dropped
	db.AddQuery(mysql.BaseShowTables, sqltypes.MakeTestResult(mysql.BaseShowTablesFields,
		"testdb|dual|BASE TABLE|1427325875",
	))
	// Mock the columns query for dual table
	db.AddQuery("SELECT COLUMN_NAME as column_name\n\t\tFROM INFORMATION_SCHEMA.COLUMNS\n\t\tWHERE TABLE_SCHEMA = 'testdb' AND TABLE_NAME = 'dual'\n\t\tORDER BY ORDINAL_POSITION", sqltypes.MakeTestResult(sqltypes.MakeTestFields("column_name", "varchar"), "dummy"))
	// Mock the show create table query for dual
	db.AddQuery("SELECT `dummy` FROM `testdb`.`dual` WHERE 1 != 1", sqltypes.MakeTestResult(sqltypes.MakeTestFields("dummy", "varchar")))
	// TODO: this query now returns the schema_name and table_name
	// and will need fixing.
	db.AddQuery(mysql.BaseShowPrimary, &sqltypes.Result{})
	// Add the "show schemas" query that initTables() needs
	db.AddQuery("show schemas", sqltypes.MakeTestResult(sqltypes.MakeTestFields(
		"Database",
		"varchar"),
		"testdb",
	))
	AddFakeInnoDBReadRowsResult(db, 1)
	se := newEngine(10*time.Second, 10*time.Second, schemaMaxAgeSeconds, db, nil)
	require.NoError(t, se.Open())
	dbName := "testdb"
	cancel := func() {
		defer db.Close()
		defer se.Close()
	}
	return se, db, dbName, cancel
}
