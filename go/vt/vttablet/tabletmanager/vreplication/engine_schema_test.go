package vreplication

import (
	"testing"

	"vitess.io/vitess/go/vt/binlog/binlogplayer"
	"vitess.io/vitess/go/vt/dbconfigs"
	"vitess.io/vitess/go/vt/vtenv"
)

func TestEngineSchemaManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	env := vtenv.NewTestEnv()
	vre := &Engine{
		env:         env,
		controllers: make(map[int32]*controller),
		schemaMap:   make(map[string]string),
		schemaClientFactories: make(map[string]struct {
			filtered func() binlogplayer.DBClient
			dba      func() binlogplayer.DBClient
		}),
	}

	// Test InitDBConfig with backward compatibility
	dbcfgs := &dbconfigs.DBConfigs{
		DBName: "test_physical_db",
	}

	// Don't set the client factories first so InitDBConfig will work
	vre.InitDBConfig(dbcfgs)

	// Now set the mock client factories after InitDBConfig
	vre.dbClientFactoryFiltered = func() binlogplayer.DBClient { return nil }
	vre.dbClientFactoryDba = func() binlogplayer.DBClient { return nil }

	// Verify that InitDBConfig set up the basic properties
	if vre.dbName != "test_physical_db" {
		t.Errorf("Expected dbName to be 'test_physical_db', got %s", vre.dbName)
	}

	// Verify that InitDBConfig also set up the schema mapping with the physical keyspace
	if vre.physicalKeyspace != "test_physical_db" {
		t.Errorf("Expected physicalKeyspace to be 'test_physical_db', got %s", vre.physicalKeyspace)
	}

	// Verify the schema mapping was created
	schema, err := vre.GetSchemaForKeyspace("test_physical_db")
	if err != nil {
		t.Errorf("Expected to find schema for physical keyspace, got error: %v", err)
	}
	if schema != "test_physical_db" {
		t.Errorf("Expected schema to be 'test_physical_db', got %s", schema)
	}

	// Test adding virtual keyspace
	err = vre.AddVirtualKeyspace("virtual_ks1", "virtual_schema1")
	if err != nil {
		t.Errorf("Expected no error adding virtual keyspace, got: %v", err)
	}

	// Verify virtual keyspace was added
	schema, err = vre.GetSchemaForKeyspace("virtual_ks1")
	if err != nil {
		t.Errorf("Expected to find schema for virtual keyspace, got error: %v", err)
	}
	if schema != "virtual_schema1" {
		t.Errorf("Expected schema to be 'virtual_schema1', got %s", schema)
	}

	// Test adding duplicate virtual keyspace
	err = vre.AddVirtualKeyspace("virtual_ks1", "different_schema")
	if err == nil {
		t.Error("Expected error when adding duplicate virtual keyspace")
	}

	// Test listing managed schemas
	schemas := vre.ListManagedSchemas()
	expectedCount := 2 // physical + 1 virtual
	if len(schemas) != expectedCount {
		t.Errorf("Expected %d schemas, got %d", expectedCount, len(schemas))
	}

	// Test removing virtual keyspace
	err = vre.RemoveVirtualKeyspace("virtual_ks1")
	if err != nil {
		t.Errorf("Expected no error removing virtual keyspace, got: %v", err)
	}

	// Verify virtual keyspace was removed
	_, err = vre.GetSchemaForKeyspace("virtual_ks1")
	if err == nil {
		t.Error("Expected error when getting schema for removed virtual keyspace")
	}

	// Test removing non-existent virtual keyspace
	err = vre.RemoveVirtualKeyspace("non_existent")
	if err == nil {
		t.Error("Expected error when removing non-existent virtual keyspace")
	}

	// Test removing physical keyspace (should fail)
	err = vre.RemoveVirtualKeyspace("test_physical_db")
	if err == nil {
		t.Error("Expected error when trying to remove physical keyspace")
	}

	// Test GetPhysicalKeyspace
	physicalKs := vre.GetPhysicalKeyspace()
	if physicalKs != "test_physical_db" {
		t.Errorf("Expected physical keyspace to be 'test_physical_db', got %s", physicalKs)
	}
}

func TestEngineSchemaManagementLegacyMode(t *testing.T) {
	env := vtenv.NewTestEnv()
	vre := &Engine{
		env:         env,
		controllers: make(map[int32]*controller),
		dbName:      "legacy_db",
		// Note: schemaMap is nil to simulate legacy mode
	}

	// Test legacy mode behavior
	schema, err := vre.GetSchemaForKeyspace("")
	if err != nil {
		t.Errorf("Expected no error in legacy mode, got: %v", err)
	}
	if schema != "legacy_db" {
		t.Errorf("Expected schema to be 'legacy_db', got %s", schema)
	}

	// Test with physical keyspace name in legacy mode
	vre.physicalKeyspace = "legacy_db"
	schema, err = vre.GetSchemaForKeyspace("legacy_db")
	if err != nil {
		t.Errorf("Expected no error in legacy mode, got: %v", err)
	}
	if schema != "legacy_db" {
		t.Errorf("Expected schema to be 'legacy_db', got %s", schema)
	}

	// Test with unknown keyspace in legacy mode
	_, err = vre.GetSchemaForKeyspace("unknown")
	if err == nil {
		t.Error("Expected error for unknown keyspace in legacy mode")
	}

	// Test ListManagedSchemas in legacy mode
	schemas := vre.ListManagedSchemas()
	if len(schemas) != 1 {
		t.Errorf("Expected 1 schema in legacy mode, got %d", len(schemas))
	}
	if schemas[0] != "legacy_db" {
		t.Errorf("Expected schema to be 'legacy_db', got %s", schemas[0])
	}
}

func TestInitDBConfigWithKeyspace(t *testing.T) {
	env := vtenv.NewTestEnv()
	vre := &Engine{
		env:         env,
		controllers: make(map[int32]*controller),
		schemaMap:   make(map[string]string),
		schemaClientFactories: make(map[string]struct {
			filtered func() binlogplayer.DBClient
			dba      func() binlogplayer.DBClient
		}),
		dbName: "test_db",
	}

	// Test InitDBConfigWithKeyspace
	err := vre.InitDBConfigWithKeyspace("physical_ks")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Verify initialization
	if vre.physicalKeyspace != "physical_ks" {
		t.Errorf("Expected physicalKeyspace to be 'physical_ks', got %s", vre.physicalKeyspace)
	}

	schema, err := vre.GetSchemaForKeyspace("physical_ks")
	if err != nil {
		t.Errorf("Expected to find schema for physical keyspace, got error: %v", err)
	}
	if schema != "test_db" {
		t.Errorf("Expected schema to be 'test_db', got %s", schema)
	}
}
