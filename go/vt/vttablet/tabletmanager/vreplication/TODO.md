# VReplication Virtual Keyspace Support - Refactoring Plan

## Overview

Currently, VReplication is hardcoded to work with a single database schema (the physical keyspace's default schema). With virtual keyspaces, we need VReplication to be able to subscribe to changes from multiple schemas:
1. The default schema for the physical keyspace
2. Additional schemas for each virtual keyspace that gets added over time

This document outlines a staged approach to refactor VReplication to support virtual keyspaces.

## Current State

### Problem Areas

1. **Engine.InitDBConfig()** - Sets `vre.dbName` to a single database name from `dbcfgs.DBName`
2. **Engine.readAllRows()** - Filters vreplication streams by single `db_name` 
3. **Controller/VReplicator** - Assumes single target database schema
4. **VCopier** - Copies tables from single source to single target schema
5. **VPlayer** - Applies binlog events to single target schema

### Key Files to Modify

- `engine.go` - Core engine initialization and stream management
- `controller.go` - Stream controller logic
- `vreplicator.go` - Main replication logic
- `vcopier.go` - Table copying logic
- `vplayer.go` - Binlog event application

## Refactoring Stages

### Stage 1: Multi-Schema Engine Foundation ✅ COMPLETED

**Goal**: Modify the Engine to track multiple target schemas instead of a single `dbName`.

#### Changes Completed:

1. **Engine Structure Changes**: ✅
   - Added `schemaMap map[string]string` for keyspace -> schema_name mapping
   - Added `physicalKeyspace string` to track the physical keyspace
   - Added `schemaClientFactories` for schema-specific database client factories
   - Kept `dbName` for backward compatibility

2. **InitDBConfig() Refactoring**: ✅
   - Enhanced to support both single-schema (backward compatibility) and multi-schema modes
   - Added `InitDBConfigWithKeyspace()` for explicit keyspace information
   - Initializes `schemaMap` with physical keyspace -> default schema mapping

3. **Schema Management Methods**: ✅
   ```go
   func (vre *Engine) AddVirtualKeyspace(virtualKeyspace, schemaName string) error
   func (vre *Engine) RemoveVirtualKeyspace(virtualKeyspace string) error
   func (vre *Engine) GetSchemaForKeyspace(keyspace string) (string, error)
   func (vre *Engine) ListManagedSchemas() []string
   func (vre *Engine) GetPhysicalKeyspace() string
   ```

4. **Test Engine Updates**: ✅
   - Updated `NewTestEngine()` and `NewSimpleTestEngine()` to initialize new fields

#### Testing Status:
- ⏳ Unit tests for new schema management methods - **IN PROGRESS**
- ⏳ Integration tests ensuring backward compatibility - **PENDING**
- ⏳ Tests for adding/removing virtual keyspaces - **PENDING**

### Stage 2: Controller Schema Awareness ✅ COMPLETED

**Goal**: Make controllers aware of which target schema they should operate on.

#### Changes Completed:

1. **Controller Structure Changes**: ✅
   ```go
   type controller struct {
       // Add schema context
       targetKeyspace string
       targetSchema   string
       // ... existing fields
   }
   ```

2. **newController() Updates**: ✅
   - Accept target keyspace/schema parameters from vreplication stream params
   - Validate target schema exists via engine's GetSchemaForKeyspace()
   - Set up schema-specific database connections
   - Fallback to legacy mode for backward compatibility

3. **Database Connection Management**: ✅
   - Added `getSchemaSpecificDBClientFactory()` method to return schema-aware DB client factory
   - Ensures connections use correct target schema
   - Maintains backward compatibility when using default schema

4. **Enhanced Logging**: ✅
   - Added targetKeyspace and targetSchema to controller creation logs
   - Improved debugging visibility for multi-schema operations

#### Testing Status:
- ✅ **Unit tests for controller schema awareness** - **COMPLETED**
- ✅ **Tests for schema-specific DB client factory** - **COMPLETED**  
- ✅ **Integration tests for virtual keyspace controller creation** - **COMPLETED**
- ✅ **Backward compatibility validation** - **COMPLETED**

### Stage 3: VReplicator Multi-Schema Support ✅ COMPLETED

**Goal**: Enable VReplicator to work with schema-specific operations.

#### Changes Completed:

1. **VReplicator Schema Context**: ✅
   ```go
   type vreplicator struct {
       targetKeyspace string
       targetSchema   string
       // ... existing fields
   }
   ```

2. **Schema-Aware Operations**: ✅
   - Updated `buildColInfoMap()` to work with specific target schema
   - Modified DDL operations to target correct schema via `getTargetSchemaName()`
   - Updated table existence checks and schema validation to use target schema
   - Enhanced secondary key operations to use correct schema

3. **Cross-Schema Replication Logic**: ✅
   - Controllers now pass schema context to VReplicators
   - Schema name mapping/translation implemented via helper methods
   - Proper isolation between virtual keyspace streams ensured

4. **Helper Methods**: ✅
   - Added `getTargetSchemaName()` method for consistent schema name resolution
   - Schema-aware operations throughout the VReplicator lifecycle

5. **Database Name Consistency Fixes**: ✅
   - Fixed `readAllRows()` to read from all managed schemas in multi-schema mode
   - Enhanced schema-specific client factories to properly set database names
   - Implemented proper virtual keyspace database name resolution

#### Testing Status:
- ✅ **Multi-schema engine foundation tests** - **COMPLETED**
- ✅ **Controller schema awareness tests** - **COMPLETED**
- ✅ **VReplicator schema-specific operations** - **COMPLETED**
- ✅ **Database name consistency validation** - **COMPLETED**

### Stage 4: VCopier Multi-Schema Enhancement ✅ COMPLETED

**Goal**: Enable table copying to work with multiple target schemas.

#### Changes Completed:

1. **VCopier Schema Targeting**: ✅
   ```go
   type vcopier struct {
       targetKeyspace   string
       targetSchema     string
       // ... existing fields
   }
   ```

2. **Schema-Specific Copy Operations**: ✅
   - Updated `getTargetSchemaName()` to target specific schema for virtual keyspaces
   - Modified table creation/truncation to use correct schema via vreplicator
   - Handle schema-specific constraints and indexes through vreplicator delegation

3. **Copy State Management**: ✅
   - Track copy progress per schema through existing copy_state table
   - Update `copy_state` table includes schema context via vrepl_id
   - Handle concurrent copying to different schemas through worker isolation

#### Testing Status:
- ✅ **Schema-specific copy operations** - **COMPLETED**
- ✅ **Copy state tracking validation** - **COMPLETED**
- ✅ **Schema isolation through vreplicator** - **COMPLETED**

### Stage 5: VPlayer Multi-Schema Application ✅ COMPLETED

**Goal**: Enable VPlayer to apply binlog events to correct target schemas.

#### Changes Completed:

1. **VPlayer Schema Context**: ✅
   ```go
   type vplayer struct {
       targetKeyspace string
       targetSchema   string
       // ... existing fields
   }
   ```

2. **Event Application Logic**: ✅
   - Route events to correct target schema through vreplicator delegation
   - Handle schema-specific SQL generation via existing table plans
   - Ensure transaction isolation between schemas through existing transaction management

3. **Binlog Event Processing**: ✅
   - Parse keyspace information from binlog events (handled by existing replicator plan)
   - Map source keyspace to target schema via `getTargetSchemaName()`
   - Apply events with correct schema context through vreplicator

#### Testing Status:
- ✅ **Schema-aware event application** - **COMPLETED**
- ✅ **Schema routing through vreplicator** - **COMPLETED**
- ✅ **Transaction isolation maintained** - **COMPLETED**

### Stage 6: Virtual Keyspace Lifecycle Integration

**Goal**: Integrate with virtual keyspace creation/deletion lifecycle.

#### Changes Required:

1. **Keyspace Lifecycle Hooks**:
   ```go
   func (vre *Engine) OnVirtualKeyspaceCreated(ctx context.Context, virtualKeyspace, physicalKeyspace, schemaName string) error
   func (vre *Engine) OnVirtualKeyspaceDeleted(ctx context.Context, virtualKeyspace string) error
   ```

2. **Dynamic Schema Management**:
   - Auto-register new virtual keyspaces
   - Clean up streams when virtual keyspaces are deleted
   - Handle schema name conflicts

3. **Stream Auto-Creation**:
   - Automatically create replication streams for new virtual keyspaces
   - Inherit configuration from physical keyspace streams
   - Handle initial data synchronization

#### Testing:
- Virtual keyspace creation/deletion scenarios
- Stream lifecycle management
- Configuration inheritance validation

### Stage 7: Monitoring and Observability

**Goal**: Add monitoring and debugging capabilities for multi-schema replication.

#### Changes Required:

1. **Enhanced Metrics**:
   - Per-schema replication lag metrics
   - Schema-specific error rates
   - Virtual keyspace stream health

2. **Debugging Tools**:
   - Schema-aware status reporting
   - Stream inspection by target schema
   - Multi-schema replication diagnostics

3. **Logging Enhancements**:
   - Include schema context in log messages
   - Schema-specific error reporting
   - Virtual keyspace operation logging

#### Testing:
- Metrics collection validation
- Debugging tool functionality
- Log message verification

## Implementation Progress

### Completed ✅
- **Stage 1**: Multi-Schema Engine Foundation
  - Engine structure modifications
  - Schema management methods
  - Backward compatibility preservation
  - Test engine updates
- **Stage 2**: Controller Schema Awareness
  - Controller structure modifications
  - Schema-aware controller creation
  - Schema-specific database connection management
  - Comprehensive unit testing
- **Stage 3**: VReplicator Multi-Schema Support
  - VReplicator structure modifications
  - Schema-aware operations (buildColInfoMap, DDL operations)
  - Cross-schema replication logic
  - Helper methods for consistent schema resolution
  - Database name consistency fixes
- **Stage 4**: VCopier Multi-Schema Enhancement
  - VCopier structure modifications
  - Schema-specific copy operations
  - Copy state management with schema isolation
- **Stage 5**: VPlayer Multi-Schema Application
  - VPlayer structure modifications
  - Schema-aware event application
  - Binlog event processing with schema context

### In Progress ⏳
- **Stage 6**: Virtual Keyspace Lifecycle Integration

### Pending ⏳
- **Stage 7**: Monitoring and Observability

## Implementation Considerations

### Backward Compatibility

- All changes must maintain backward compatibility with existing single-schema deployments
- Existing vreplication streams should continue to work without modification
- Configuration changes should be opt-in where possible

### Performance Considerations

- Schema mapping lookups should be optimized for performance
- Connection pooling may need adjustment for multiple schemas
- Consider caching schema metadata to reduce lookup overhead

### Error Handling

- Schema-specific error reporting and recovery
- Graceful handling of schema deletion scenarios
- Proper cleanup when virtual keyspaces are removed

### Security Considerations

- Ensure proper access controls for multi-schema operations
- Validate schema permissions for each virtual keyspace
- Prevent cross-schema data leakage

## Migration Strategy

### Phase 1: Internal Refactoring ✅ COMPLETED
- Implement stages 1-3 without exposing new functionality
- Ensure all existing tests pass
- Add comprehensive unit tests for new components

### Phase 2: Feature Enablement
- Implement stages 4-6 with feature flags
- Add integration tests for virtual keyspace scenarios
- Document new configuration options

### Phase 3: Production Rollout
- Enable virtual keyspace support in controlled environments
- Implement monitoring and alerting (stage 7)
- Provide migration tools and documentation

## Testing Strategy

### Unit Tests
- Schema management operations ✅ **COMPLETED**
- Controller and VReplicator schema awareness ✅ **COMPLETED**
- Event routing and application logic ✅ **COMPLETED**

### Integration Tests
- End-to-end virtual keyspace replication ⏳ **IN PROGRESS**
- Multi-schema concurrent operations ⏳ **PENDING**
- Virtual keyspace lifecycle scenarios ⏳ **PENDING**

### Performance Tests
- Multi-schema replication performance ⏳ **PENDING**
- Schema lookup performance ⏳ **PENDING**
- Connection pool efficiency ⏳ **PENDING**

## Comprehensive Test Plan for Virtual Keyspace VReplication

### Phase 1: Core Functionality Tests ✅ **COMPLETED**
These tests verify the basic virtual keyspace functionality is working correctly.

#### 1.1 Engine Schema Management Tests ✅ **COMPLETED**
- **File**: `workflow_virtual_keyspace_test.go`
- **Test**: `TestVirtualKeyspaceSchemaMapping`
- **Coverage**:
  - Engine initialization with physical keyspace
  - Adding/removing virtual keyspaces
  - Schema name mapping and resolution
  - Error handling for invalid operations
  - Listing managed schemas

#### 1.2 Controller Schema Awareness Tests ✅ **COMPLETED**
- **File**: `workflow_virtual_keyspace_test.go`
- **Test**: `TestWorkflowVirtualKeyspaceIntegration`
- **Coverage**:
  - Controller creation with different target keyspaces
  - Schema-specific DB client factory generation
  - Backward compatibility with legacy workflows
  - Fallback behavior for unknown keyspaces

#### 1.3 MoveTables Workflow Tests ✅ **COMPLETED**
- **File**: `movetables_simple_test.go`
- **Test**: `TestMoveTablesWorkflowSchemaSelection`
- **Coverage**:
  - MoveTables targeting physical keyspace
  - MoveTables targeting virtual keyspaces
  - Correct database name resolution
  - Schema-specific factory differentiation

### Phase 2: Bug Reproduction and Regression Tests ✅ **COMPLETED**
These tests specifically target the reported bug scenarios.

#### 2.1 MoveTables Bug Scenario Tests ✅ **COMPLETED**
- **File**: `movetables_simple_test.go`
- **Test**: `TestMoveTablesWorkflowBugScenario`
- **Coverage**:
  - Exact reproduction of `vtctldclient MoveTables` command scenario
  - Verification that customer keyspace uses `vt_customer_0` database
  - Prevention of incorrect `vt_main` database usage
  - Source-target keyspace mapping validation

#### 2.2 Virtual Keyspace Bug Reproduction ✅ **COMPLETED**
- **File**: `movetables_simple_test.go`
- **Test**: `TestMoveTablesVirtualKeyspaceBugReproduction`
- **Coverage**:
  - Full workflow creation simulation
  - Database client configuration verification
  - Schema mapping correctness
  - Virtual keyspace isolation

### Phase 3: End-to-End Integration Tests ✅ **COMPLETED**
These tests verify complete workflows work correctly with virtual keyspaces.

#### 3.1 Complete VReplication Workflow Tests ✅ **COMPLETED**
- **File**: `vreplication_virtual_keyspace_e2e_test.go`
- **Tests**:
  - `TestVirtualKeyspaceVReplicationE2E`
  - `TestVirtualKeyspaceVCopierE2E`
  - `TestVirtualKeyspaceVPlayerE2E`
- **Coverage**:
  - Full replication workflow targeting virtual keyspaces
  - Controller schema awareness and targeting
  - Database client factory isolation
  - Schema-specific operations verification
  - Physical vs virtual keyspace differentiation

#### 3.2 Multi-Schema Concurrent Operations ✅ **COMPLETED**
- **File**: `vreplication_concurrent_test.go`
- **Tests**:
  - `TestConcurrentVirtualKeyspaceReplication`
  - `TestVirtualKeyspaceIsolation`
- **Coverage**:
  - Multiple workflows running simultaneously to different virtual keyspaces
  - Concurrent workflow creation and database operations
  - Schema isolation verification
  - Transaction isolation between virtual keyspaces
  - Cross-schema contamination prevention

#### 3.3 Virtual Keyspace Lifecycle Tests ⏳ **PENDING**
- **File**: `vreplication_lifecycle_test.go` (to be created)
- **Tests**:
  - `TestVirtualKeyspaceCreationDuringReplication`
  - `TestVirtualKeyspaceDeletionDuringReplication`
  - `TestVirtualKeyspaceStreamManagement`
- **Coverage**:
  - Adding virtual keyspaces while replication is running
  - Removing virtual keyspaces and stream cleanup
  - Stream lifecycle management
  - Configuration inheritance

### Phase 4: Data Consistency and Correctness Tests ✅ **COMPLETED**
These tests verify data integrity across virtual keyspaces.

#### 4.1 Data Consistency Tests ✅ **COMPLETED**
- **File**: `vreplication_consistency_test.go`
- **Tests**:
  - `TestVirtualKeyspaceDataConsistency`
  - `TestCrossSchemaDataIsolation`
- **Coverage**:
  - Data consistency within virtual keyspaces
  - Cross-virtual keyspace data isolation
  - Transaction boundary respect and verification
  - Rollback behavior validation
  - Schema-specific table access patterns
  - Concurrent schema access isolation

#### 4.2 Schema Evolution Tests ✅ **COMPLETED**
- **File**: `vreplication_schema_evolution_test.go`
- **Tests**:
  - `TestVirtualKeyspaceDDLHandling`
  - `TestSchemaChangeReplication`
- **Coverage**:
  - DDL operations on virtual keyspace schemas
  - DDL isolation between virtual keyspaces
  - Concurrent DDL operations
  - Schema change propagation
  - Schema synchronization across virtual keyspaces
  - DDL error handling and recovery

### Phase 5: Performance and Scalability Tests ⏳ **PENDING**
These tests verify performance characteristics of virtual keyspace replication.

#### 5.1 Performance Baseline Tests ⏳ **PENDING**
- **File**: `vreplication_performance_test.go` (to be created)
- **Tests**:
  - `TestVirtualKeyspaceReplicationPerformance`
  - `TestSchemaLookupPerformance`
  - `TestConnectionPoolEfficiency`
- **Coverage**:
  - Replication throughput with virtual keyspaces
  - Schema lookup latency
  - Connection pool utilization
  - Memory usage patterns

#### 5.2 Scalability Tests ⏳ **PENDING**
- **File**: `vreplication_scalability_test.go` (to be created)
- **Tests**:
  - `TestManyVirtualKeyspacesScalability`
  - `TestHighVolumeVirtualKeyspaceReplication`
  - `TestResourceLimitHandling`
- **Coverage**:
  - Many virtual keyspaces (100+)
  - High-volume data replication
  - Resource limit handling
  - Graceful degradation

### Phase 6: Error Handling and Recovery Tests ⏳ **PENDING**
These tests verify error scenarios are handled correctly.

#### 6.1 Error Handling Tests ⏳ **PENDING**
- **File**: `vreplication_error_handling_test.go` (to be created)
- **Tests**:
  - `TestVirtualKeyspaceErrorRecovery`
  - `TestSchemaNotFoundHandling`
  - `TestVirtualKeyspaceConnectionErrors`
- **Coverage**:
  - Recovery from virtual keyspace errors
  - Schema not found error handling
  - Connection failure recovery
  - Partial failure scenarios

#### 6.2 Failure Injection Tests ⏳ **PENDING**
- **File**: `vreplication_failure_injection_test.go` (to be created)
- **Tests**:
  - `TestVirtualKeyspaceNetworkFailures`
  - `TestVirtualKeyspaceDatabaseFailures`
  - `TestVirtualKeyspaceResourceExhaustion`
- **Coverage**:
  - Network failure simulation
  - Database failure simulation
  - Resource exhaustion handling
  - Chaos engineering scenarios

### Phase 7: Monitoring and Observability Tests ⏳ **PENDING**
These tests verify monitoring and debugging capabilities.

#### 7.1 Metrics and Monitoring Tests ⏳ **PENDING**
- **File**: `vreplication_monitoring_test.go` (to be created)
- **Tests**:
  - `TestVirtualKeyspaceMetrics`
  - `TestSchemaSpecificStats`
  - `TestVirtualKeyspaceHealthChecks`
- **Coverage**:
  - Per-schema metrics collection
  - Health check functionality
  - Alerting integration
  - Dashboard data accuracy

#### 7.2 Debugging and Diagnostics Tests ⏳ **PENDING**
- **File**: `vreplication_diagnostics_test.go` (to be created)
- **Tests**:
  - `TestVirtualKeyspaceDebugging`
  - `TestSchemaSpecificLogging`
  - `TestVirtualKeyspaceStatusReporting`
- **Coverage**:
  - Debug information accuracy
  - Log message schema context
  - Status reporting completeness
  - Troubleshooting tool functionality

### Phase 8: Backward Compatibility Tests ⏳ **PENDING**
These tests ensure existing functionality continues to work.

#### 8.1 Legacy Compatibility Tests ⏳ **PENDING**
- **File**: `vreplication_backward_compatibility_test.go` (to be created)
- **Tests**:
  - `TestLegacyVReplicationCompatibility`
  - `TestSingleSchemaWorkflows`
  - `TestMigrationFromLegacyToVirtual`
- **Coverage**:
  - Legacy single-schema workflows
  - Migration path validation
  - Configuration compatibility
  - API backward compatibility

### Test Implementation Priority

#### High Priority (Phase 1-3) ✅ **COMPLETED**
- Core functionality verification
- Bug reproduction and regression prevention
- Basic end-to-end workflow validation

#### Medium Priority (Phase 4-5) ⏳ **PENDING**
- Data consistency and correctness
- Performance baseline establishment
- Scalability validation

#### Lower Priority (Phase 6-8) ⏳ **PENDING**
- Advanced error handling
- Comprehensive monitoring
- Backward compatibility edge cases

### Test Execution Strategy

#### Continuous Integration
- All Phase 1-2 tests run on every commit
- Phase 3 tests run on pull requests
- Phase 4-5 tests run nightly
- Phase 6-8 tests run weekly

#### Test Environment Requirements
- Multi-schema MySQL setup
- Virtual keyspace configuration
- Load testing infrastructure
- Monitoring and metrics collection

#### Success Criteria
- All existing tests continue to pass
- New virtual keyspace tests achieve >95% code coverage
- Performance tests show <10% degradation
- Error handling tests demonstrate graceful recovery
- Monitoring tests provide actionable insights

## Documentation Updates

- Update VReplication documentation to cover virtual keyspace support
- Provide configuration examples for multi-schema setups
- Document troubleshooting procedures for virtual keyspace issues
- Create migration guide for existing deployments

## Success Criteria

1. VReplication can simultaneously replicate to multiple schemas corresponding to different virtual keyspaces
2. Virtual keyspaces can be added/removed dynamically without disrupting existing replication
3. All existing single-schema functionality continues to work unchanged
4. Performance impact is minimal for single-schema deployments
5. Comprehensive monitoring and debugging capabilities are available

## Timeline Estimate

- **Stage 1**: ✅ **COMPLETED** (2-3 weeks originally estimated)
- **Stage 2**: ✅ **COMPLETED** (2-3 weeks originally estimated)
- **Stage 3**: ✅ **COMPLETED** (2-3 weeks originally estimated)
- **Stage 4**: 2-3 weeks (VCopier updates)
- **Stage 5**: 2-3 weeks (VPlayer and lifecycle integration)
- **Stage 6**: 1-2 weeks (Lifecycle integration)
- **Stage 7**: 1-2 weeks (Monitoring and observability)
- **Testing and Documentation**: 2-3 weeks

**Total Estimated Timeline**: 10-15 weeks (Stages 1-3 completed)

## Risks and Mitigation

### Risk: Breaking Existing Functionality
**Mitigation**: Comprehensive backward compatibility testing and feature flags

### Risk: Performance Degradation
**Mitigation**: Performance testing at each stage and optimization of critical paths

### Risk: Complex Schema Management
**Mitigation**: Clear abstractions and extensive unit testing of schema management logic

### Risk: Data Consistency Issues
**Mitigation**: Thorough testing of cross-schema scenarios and transaction isolation
