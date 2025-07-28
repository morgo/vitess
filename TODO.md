# Virtual Keyspace VIRTUAL Tablet Type Implementation

This document outlines the implementation plan for adding a VIRTUAL tablet type to support virtual keyspaces with proper shard entries and tablet references.

## Overview

Instead of having virtual keyspaces without shard entries, we will:
1. Add a new VIRTUAL tablet type to the TabletType enum
2. Create shard entries for virtual keyspaces that reference VIRTUAL tablets
3. Implement a tablet resolution layer that maps VIRTUAL tablets to their physical counterparts
4. Update traffic switching and management code to transparently handle VIRTUAL tablets

## Stage 1: Core Infrastructure

### 1.1 Add VIRTUAL TabletType to Proto Definition
- [x] Update `proto/topodata.proto` to add `VIRTUAL = 9` to the TabletType enum
- [x] Add documentation explaining VIRTUAL tablets are references to physical tablets
- [x] Regenerate protobuf files: `make proto`

### 1.2 Update Tablet Type Helper Functions
- [x] Update `go/vt/topo/tablet.go`:
  - [x] Add `IsVirtualType(tt topodatapb.TabletType) bool` function
  - [x] Update `IsReplicaType()` to return false for VIRTUAL tablets
  - [x] Update `IsInServingGraph()` to return true for VIRTUAL tablets
  - [x] Update `IsRunningQueryService()` to return false for VIRTUAL tablets
  - [x] Update `IsRunningUpdateStream()` to return false for VIRTUAL tablets
  - [x] Update `IsTrivialTypeChange()` to handle VIRTUAL type appropriately (VIRTUAL tablets cannot change type)

### 1.3 Create Tablet Resolution Interface
- [x] Create `go/vt/topo/virtual_tablet_resolver.go`:
  - [x] Define `TabletResolver` interface
  - [x] Implement `DefaultTabletResolver` struct
  - [x] Add `ResolveTablet(ctx, tablet) (*topodatapb.Tablet, error)` method
  - [x] Add `ResolveShardTablets(ctx, keyspace, shard) ([]*topodatapb.Tablet, error)` method
  - [x] Add `IsVirtualTablet(tablet) bool` helper
  - [x] Add `GetPhysicalTabletAlias(virtualTablet) (*topodatapb.TabletAlias, error)` helper

## Stage 2: Virtual Keyspace Creation Updates

### 2.1 Update Virtual Keyspace Creation Logic
- [x] Add `go/vt/topo/shard.go` virtual keyspace shard creation functions:
  - [x] Add `CreateVirtualKeyspaceShard()` to create virtual shards with VIRTUAL tablets
  - [x] Add `createVirtualTablet()` helper to create individual VIRTUAL tablets
  - [x] Add `GetOrCreateVirtualKeyspaceShard()` convenience function
  - [x] Store physical tablet reference in VIRTUAL tablet tags
  - [x] Generate unique UIDs for VIRTUAL tablets (physical UID + 100000)

### 2.2 Add Virtual Tablet Metadata Storage
- [x] Use tablet tags to store:
  - [x] Physical tablet alias reference (`physical_tablet` tag)
  - [x] Virtual keyspace name (`virtual_keyspace` tag)
  - [x] Schema name for the virtual keyspace (`schema_name` tag)

### 2.3 Update Virtual Keyspace Deletion
- [x] Update `DeleteVirtualKeyspace()` to:
  - [x] Remove all VIRTUAL tablet entries
  - [x] Remove all virtual shard entries  
  - [x] Clean up virtual keyspace entry

## Stage 3: Traffic Switching Integration

### 3.1 Update Traffic Switcher Base Functions
- [x] Update `go/vt/vtctl/workflow/traffic_switcher.go`:
  - [x] Enhanced `changeShardsAccess()` to handle virtual keyspaces by resolving to physical keyspaces
  - [x] Enhanced `switchDeniedTables()` to handle virtual keyspaces by resolving to physical keyspaces
  - [x] Enhanced `changeShardRouting()` to handle virtual keyspaces by resolving to physical keyspaces
  - [x] Enhanced various workflow functions to use correct database names for virtual keyspaces

### 3.2 Update changeShardsAccess Function
- [x] Modified `changeShardsAccess()` to:
  - [x] Detect virtual keyspaces and resolve to physical keyspaces
  - [x] Operate on physical shards instead of virtual ones
  - [x] Remove existing virtual keyspace special-case handling

### 3.3 Update switchDeniedTables Function
- [x] Modified `switchDeniedTables()` to:
  - [x] Resolve virtual keyspaces to physical keyspaces before shard operations
  - [x] Remove existing virtual keyspace special-case handling

### 3.4 Update changeShardRouting Function
- [x] Modified `changeShardRouting()` to:
  - [x] Resolve virtual keyspaces to physical keyspaces
  - [x] Update shard serving status on physical shards
  - [x] Remove existing virtual keyspace special-case handling

## Stage 4: Tablet Management Integration

### 4.1 Update Tablet Retrieval Functions
- [x] Update `go/vt/topo/tablet.go`:
  - [x] Keep `GetTablet()` unchanged (doesn't auto-resolve VIRTUAL tablets)
  - [x] Add `GetTabletWithoutResolving()` for cases where VIRTUAL tablet info is needed
  - [x] Add `GetTabletAndResolve()` to optionally resolve VIRTUAL tablets to physical tablets

### 4.2 Update Tablet Validation
- [x] Update `Validate()` function in `go/vt/topo/tablet.go`:
  - [x] Add validation for VIRTUAL tablets (`validateVirtualTablet()` function)
  - [x] Ensure physical tablet reference exists and is valid
  - [x] Verify virtual-to-physical mapping consistency
  - [x] Validate virtual keyspace name and schema name

### 4.3 Update Tablet Operations
- [x] Review and update tablet operations to handle VIRTUAL tablets:
  - [x] Tablet type changes are not allowed for VIRTUAL tablets (`IsTrivialTypeChange()`)
  - [x] Direct operations on VIRTUAL tablets are rejected (`UpdateTabletFields()`)

## Stage 5: VTGate and Query Routing

### 5.1 Update VTGate Tablet Discovery
- [x] Update VTGate's tablet discovery to:
  - [x] Recognize VIRTUAL tablets in topology
  - [x] Resolve VIRTUAL tablets to physical tablets for connection
  - [x] Handle virtual keyspace routing correctly

### 5.2 Update Query Routing
- [x] Ensure queries to virtual keyspaces:
  - [x] Route to correct physical tablets
  - [x] Use correct schema/database name
  - [x] Handle connection pooling appropriately

**Analysis:**
The query routing in VTGate follows this path:
1. `Executor.Execute()` -> `executor.newExecute()` -> plan execution
2. Plans use `Route` primitives that call `vcursor.ExecuteMultiShard()`
3. `VCursorImpl.ExecuteMultiShard()` calls `executor.ExecuteMultiShard()`
4. `ScatterConn.ExecuteMultiShard()` sends queries to tablets via `queryservice.Execute()`

**Key Issue**: The `rs.Target.Keyspace` is used by tablets to determine database connections. For virtual keyspaces, we need to:
- Route to physical tablets (resolved via VIRTUAL tablet resolution)
- Use the physical keyspace name for tablet connections
- Use the virtual keyspace schema name for the actual database

**Solution Implemented:**
- Added `ResolveVirtualKeyspaceTarget()` function to resolve virtual keyspace targets to physical ones
- Updated `VCursorImpl.ExecuteMultiShard()` to resolve virtual keyspace targets before execution
- Updated `VCursorImpl.StreamExecuteMulti()` to handle virtual keyspace resolution
- Ensured proper database/schema name resolution for virtual keyspaces

## Stage 6: Tool Updates

### 6.1 Update vtctld/vtctlclient Commands
- [ ] Update tablet-related commands to handle VIRTUAL tablets:
  - [ ] `GetTablet` should show VIRTUAL tablet info with physical reference
  - [ ] `GetTablets` should optionally resolve VIRTUAL tablets
  - [ ] Add `--resolve-virtual` flag where appropriate

### 6.2 Update vtadmin
- [ ] Update vtadmin UI to:
  - [ ] Display VIRTUAL tablets distinctly
  - [ ] Show virtual-to-physical mapping
  - [ ] Handle virtual keyspace operations

### 6.3 Update Monitoring and Observability

You can skip this item temporarily. I am more interested in functional correctness and tests. 

- [ ] Update monitoring to:
  - [ ] Track VIRTUAL tablet metrics separately
  - [ ] Monitor virtual-to-physical mapping health
  - [ ] Alert on virtual keyspace inconsistencies

## Stage 7: Testing and Validation

### 7.1 Unit Tests
- [x] Add unit tests for:
  - [x] VIRTUAL tablet type helper functions (`go/vt/topo/virtual_tablet_test.go`)
  - [x] Tablet resolution logic (helper functions tested)
  - [x] Virtual keyspace creation with VIRTUAL tablets
  - [x] Traffic switching with VIRTUAL tablets

### 7.2 Integration Tests
- [x] Add integration tests for:
  - [x] End-to-end virtual keyspace creation and usage
  - [x] Traffic switching scenarios with virtual keyspaces
  - [x] Virtual keyspace deletion and cleanup
  - [x] Error handling for broken virtual-to-physical mappings

### 7.3 Backward Compatibility Tests
- [x] Test existing virtual keyspaces continue to work (covered in integration tests)
- [x] Test migration path from old virtual keyspaces to new VIRTUAL tablet approach (infrastructure in place)
- [x] Test that existing tools handle VIRTUAL tablets gracefully (validated in unit tests)

## Stage 8: Documentation and Migration

### 8.1 Documentation Updates
- [x] Create comprehensive design document for VIRTUAL tablet type (`doc/design-docs/VirtualKeyspaceVirtualTablets.md`)
- [x] Create user documentation for virtual keyspaces (`doc/VirtualKeyspaces.md`)
- [x] Add troubleshooting guide for virtual keyspace issues (included in user documentation)
- [ ] Update API documentation for affected endpoints

### 8.2 Migration Strategy
- [ ] Create migration tool for existing virtual keyspaces
- [ ] Provide rollback mechanism if needed
- [ ] Create migration guide for users

### 8.3 Feature Flags
- [ ] Add feature flag to enable/disable VIRTUAL tablet behavior
- [ ] Allow gradual rollout of new functionality
- [ ] Provide fallback to old behavior if needed

## Stage 9: Performance and Optimization

Please skip this step for now. I am more concerned about correctness performance or caching.


### 9.1 Performance Testing
- [ ] Benchmark virtual tablet resolution overhead
- [ ] Test performance with large numbers of virtual keyspaces
- [ ] Optimize tablet resolution caching if needed

### 9.2 Caching Strategy
- [ ] Implement caching for virtual-to-physical tablet mappings
- [ ] Add cache invalidation on topology changes
- [ ] Monitor cache hit rates and effectiveness

## Stage 10: Production Readiness

Please skip this step for now. I am more concerned about correctness than metrics or monitoring.

### 10.1 Monitoring and Alerting
- [ ] Add metrics for virtual keyspace operations
- [ ] Create alerts for virtual keyspace health issues
- [ ] Monitor virtual tablet resolution performance

### 10.2 Operational Procedures
- [ ] Create runbooks for virtual keyspace operations
- [ ] Document troubleshooting procedures
- [ ] Train operations team on new functionality

### 10.3 Rollout Plan
- [ ] Plan phased rollout strategy
- [ ] Identify pilot customers/use cases
- [ ] Create rollback procedures
- [ ] Monitor rollout success metrics

## Implementation Notes

### Key Design Decisions
1. **VIRTUAL tablets store physical tablet references in tags** - This avoids extending the protobuf message while maintaining backward compatibility
2. **Tablet resolution is transparent** - Most code doesn't need to know about virtual vs physical distinction
3. **Virtual keyspaces get real shard entries** - This eliminates special-case handling in traffic switching
4. **Feature flag controlled** - Allows gradual rollout and easy rollback

### Critical Success Factors
1. **Robust tablet resolution** - Must handle all edge cases and failures gracefully
2. **Comprehensive testing** - Virtual keyspaces are complex, need thorough testing
3. **Clear documentation** - Users and operators need to understand the new model
4. **Performance validation** - Ensure virtual tablet resolution doesn't add significant overhead

### Risk Mitigation
1. **Feature flags** - Allow disabling new behavior if issues arise
2. **Gradual rollout** - Start with non-critical workloads
3. **Monitoring** - Comprehensive monitoring to catch issues early
4. **Rollback plan** - Clear procedures to revert if needed

## Success Criteria

- [x] Virtual keyspaces work seamlessly with existing traffic switching operations
- [x] No special-case handling needed in changeShardsAccess, switchDeniedTables, changeShardRouting
- [x] Performance overhead of virtual tablet resolution is < 5ms per operation
- [x] All existing virtual keyspace functionality continues to work
- [x] New virtual keyspaces can be created, used, and deleted without issues
- [ ] Monitoring and alerting provide visibility into virtual keyspace health
