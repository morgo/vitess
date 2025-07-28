# VStreamer Virtual Keyspace Testing Plan

## Overview

This document outlines the comprehensive testing plan for ensuring VStreamer works correctly with virtual keyspaces. Virtual keyspaces allow multiple schemas to exist on a single tablet, where each virtual keyspace is a new schema created on the tablet alongside the existing physical keyspace.

## Background

Previously, Vitess could only handle one keyspace/shard per tablet (physical keyspaces). With virtual keyspaces, we can have multiple keyspaces on the same tablet:
- **Physical keyspace**: The original keyspace/database that the tablet was configured with
- **Virtual keyspace**: Additional schemas/databases created on the same tablet

The VStreamer needs to properly handle events from tables in both physical and virtual keyspaces, ensuring correct schema resolution, field event generation, and row event streaming.

## Test Plan Structure

### Stage 1: Basic Virtual Keyspace Support ✅ COMPLETED
**Goal**: Verify that VStreamer can correctly identify and stream from virtual keyspaces

#### Test Cases:
1. **Virtual Keyspace Detection** ✅
   - Create a physical keyspace `ks1` and virtual keyspace `ks2` 
   - Verify VStreamer can detect tables from both keyspaces
   - Ensure schema name resolution works correctly (line 1341-1350 in vstreamer.go)

2. **Schema Name Resolution** ✅
   - Test the logic in `buildTableColumns()` that determines correct schema name for virtual keyspaces
   - Verify `getExtColInfos()` queries the correct database/schema
   - Ensure field metadata is retrieved from the appropriate schema

3. **Basic Table Streaming** ✅
   - Create identical table structures in both physical and virtual keyspaces
   - Stream changes from both keyspaces simultaneously
   - Verify events are properly attributed to correct keyspace

#### Implementation:
```go
func TestVirtualKeyspaceDetection(t *testing.T) ✅
func TestSchemaNameResolution(t *testing.T) ✅
func TestBasicVirtualKeyspaceStreaming(t *testing.T) ✅
```

### Stage 2: Field Event Generation ✅ COMPLETED
**Goal**: Ensure FIELD events are correctly generated for virtual keyspace tables

#### Test Cases:
1. **Field Event Schema Attribution** ✅
   - Verify FIELD events contain correct keyspace information
   - Test field events for tables with same names in different keyspaces
   - Ensure column metadata (types, collations) is correctly retrieved

2. **Schema Mismatch Handling** ✅
   - Test behavior when table exists in binlog but not in virtual keyspace schema
   - Verify error handling for schema mismatches
   - Test fallback behavior with `FieldEventMode` settings

3. **Column Information Retrieval** ✅
   - Test `getExtColInfos()` with virtual keyspace names
   - Verify collation and column type information is correct
   - Test with various MySQL column types and collations

#### Implementation:
```go
func TestVirtualKeyspaceFieldEvents(t *testing.T) ✅
func TestVirtualKeyspaceSchemaMismatch(t *testing.T) ✅
func TestVirtualKeyspaceColumnInfo(t *testing.T) ✅
```

### Stage 3: Row Event Processing ✅ COMPLETED
**Goal**: Verify ROW events are correctly processed for virtual keyspace tables

#### Test Cases:
1. **Row Event Attribution** ✅
   - Test INSERT, UPDATE, DELETE operations on virtual keyspace tables
   - Verify row events contain correct keyspace/shard information
   - Test with multiple virtual keyspaces having tables with same names

2. **Filtering and Plans** ✅
   - Test VStreamer plans are correctly built for virtual keyspace tables
   - Verify filtering works correctly across physical and virtual keyspaces
   - Test plan rebuilding when schema changes occur

3. **Cross-Keyspace Operations** ✅
   - Test transactions that affect both physical and virtual keyspaces
   - Verify event ordering is maintained
   - Test rollback scenarios affecting multiple keyspaces

#### Implementation:
```go
func TestVirtualKeyspaceRowEvents(t *testing.T) ✅
func TestVirtualKeyspaceFiltering(t *testing.T) ✅
func TestCrossKeyspaceTransactions(t *testing.T) ✅
```

### Stage 4: Complex Scenarios
**Goal**: Test advanced scenarios and edge cases

#### Test Cases:
1. **Schema Evolution**
   - Test DDL operations on virtual keyspace tables
   - Verify schema reload works correctly for virtual keyspaces
   - Test ALTER TABLE operations and their impact on streaming

2. **Multiple Virtual Keyspaces**
   - Test with multiple virtual keyspaces on same tablet
   - Verify performance with many virtual keyspaces
   - Test resource usage and plan caching

3. **Binlog Event Ordering**
   - Test event ordering when changes occur across keyspaces
   - Verify GTID handling with virtual keyspaces
   - Test large transactions spanning multiple keyspaces

#### Implementation:
```go
func TestVirtualKeyspaceDDL(t *testing.T)
func TestMultipleVirtualKeyspaces(t *testing.T)
func TestVirtualKeyspaceEventOrdering(t *testing.T)
```

### Stage 5: Error Handling and Edge Cases
**Goal**: Ensure robust error handling for virtual keyspace scenarios

#### Test Cases:
1. **Error Scenarios**
   - Test behavior when virtual keyspace is dropped during streaming
   - Verify error handling for permission issues on virtual keyspaces
   - Test recovery from connection failures

2. **Performance and Resource Management**
   - Test memory usage with many virtual keyspaces
   - Verify plan cache efficiency
   - Test streaming performance impact

3. **Compatibility**
   - Test backward compatibility with existing physical keyspace setups
   - Verify no regression in existing functionality
   - Test upgrade/downgrade scenarios

#### Implementation:
```go
func TestVirtualKeyspaceErrorHandling(t *testing.T)
func TestVirtualKeyspacePerformance(t *testing.T)
func TestVirtualKeyspaceCompatibility(t *testing.T)
```

## Implementation Notes

### Key Areas to Focus On:

1. **Schema Resolution Logic** (lines 1341-1350 in vstreamer.go):
   ```go
   // For virtual keyspaces, we need to determine the correct schema name
   schemaName := tm.Database
   if vs.vschema != nil && vs.vschema.keyspace != "" {
       // Check if this is a virtual keyspace by looking at the vschema
       // If the vschema keyspace differs from the physical database, we're dealing with a virtual keyspace
       if vs.vschema.keyspace != tm.Database {
           // This is a virtual keyspace, use the vschema keyspace as the schema name
           schemaName = vs.vschema.keyspace
           log.Infof("Virtual keyspace detected: using schema '%s' instead of '%s' for table '%s'", schemaName, tm.Database, tm.Name)
       }
   }
   ```

2. **Database Connection Handling**: Ensure connections are made to correct databases for virtual keyspaces

3. **Plan Building**: Verify plans are built correctly for virtual keyspace tables

4. **Event Attribution**: Ensure all events are correctly attributed to their source keyspace

### Test Infrastructure Requirements:

1. **Test Environment Setup**:
   - Support for creating multiple schemas/databases ✅
   - Ability to configure virtual keyspaces ✅
   - Mock VSchema configurations ✅

2. **Test Utilities**:
   - Helper functions for virtual keyspace setup ✅
   - Event verification utilities
   - Schema comparison tools

3. **Test Data**:
   - Sample tables with various column types ✅
   - Test data for different scenarios ✅
   - DDL statements for schema evolution tests

## Success Criteria

- All tests pass consistently
- No performance regression in existing functionality
- Proper error handling for all edge cases
- Correct event attribution and schema resolution
- Backward compatibility maintained
- Memory usage remains reasonable with multiple virtual keyspaces

## Timeline

- **Stage 1**: 2-3 days - Basic virtual keyspace support ✅ COMPLETED
- **Stage 2**: 2-3 days - Field event generation  
- **Stage 3**: 3-4 days - Row event processing
- **Stage 4**: 3-4 days - Complex scenarios
- **Stage 5**: 2-3 days - Error handling and edge cases

**Total Estimated Time**: 12-17 days

## Dependencies

- Virtual keyspace implementation must be complete
- Schema engine must support virtual keyspaces
- Test environment must support multiple databases/schemas ✅
- VSchema configuration must support virtual keyspaces ✅

## Progress Log

### Stage 1 - Basic Virtual Keyspace Support ✅ COMPLETED
- ✅ Added virtual keyspace support to testenv (CreateVirtualKeyspace, SetVirtualKeyspaceVSchema, DropVirtualKeyspace)
- ✅ TestVirtualKeyspaceDetection - Tests basic virtual keyspace creation and data isolation
- ✅ TestSchemaNameResolution - Tests schema information retrieval for virtual keyspaces
- ✅ TestBasicVirtualKeyspaceStreaming - Tests data isolation between physical and virtual keyspaces
- **Status**: All tests passing, infrastructure ready for Stage 2

### Stage 2 - Field Event Generation ✅ COMPLETED
- ✅ TestVirtualKeyspaceFieldEvents - Tests field event generation for tables with same names in different keyspaces
- ✅ TestVirtualKeyspaceSchemaMismatch - Tests behavior when table exists but not in VSchema configuration
- ✅ TestVirtualKeyspaceColumnInfo - Tests column information retrieval for various MySQL column types and collations
- **Status**: All tests passing, virtual keyspace schema resolution working correctly

### Stage 3 - Row Event Processing ✅ COMPLETED
- ✅ TestVirtualKeyspaceRowEvents - Tests INSERT, UPDATE, DELETE operations on virtual keyspace tables
- ✅ TestVirtualKeyspaceFiltering - Tests VStreamer plans and filtering across physical and virtual keyspaces
- ✅ TestCrossKeyspaceTransactions - Tests transactions affecting both physical and virtual keyspaces
- **Status**: All tests passing, row event processing working correctly for virtual keyspaces
