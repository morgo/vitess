# Virtual Keyspace VIRTUAL Tablet Type Design

## Overview

This document describes the implementation of the VIRTUAL tablet type in Vitess to support virtual keyspaces with proper shard entries and tablet references. This design eliminates the need for special-case handling of virtual keyspaces in traffic switching and workflow operations.

## Background

Previously, virtual keyspaces in Vitess were implemented without proper shard entries, requiring extensive special-case handling throughout the codebase. This approach had several limitations:

1. **Complex Traffic Switching**: Traffic switching operations required special handling for virtual keyspaces
2. **Inconsistent Topology**: Virtual keyspaces didn't follow the standard keyspace/shard/tablet hierarchy
3. **Limited Tooling Support**: Many tools couldn't handle virtual keyspaces properly
4. **Maintenance Burden**: Special-case code paths increased complexity and maintenance overhead

## Design Goals

1. **Eliminate Special Cases**: Virtual keyspaces should work seamlessly with existing operations
2. **Maintain Compatibility**: Existing virtual keyspace functionality must continue to work
3. **Transparent Resolution**: Most code should not need to distinguish between virtual and physical tablets
4. **Performance**: Virtual tablet resolution should add minimal overhead
5. **Operational Simplicity**: Virtual keyspaces should be easy to create, manage, and troubleshoot

## Architecture

### VIRTUAL Tablet Type

We introduce a new `VIRTUAL` tablet type (enum value 9) that represents a reference to a physical tablet. VIRTUAL tablets:

- Are not actual running tablet processes
- Store references to their corresponding physical tablets
- Enable virtual keyspaces to have proper shard entries
- Are transparently resolved to physical tablets when needed

### Virtual-to-Physical Mapping

VIRTUAL tablets store their physical tablet reference using tablet tags:

```go
type VirtualTabletTags struct {
    PhysicalTablet  string // Physical tablet alias (e.g., "zone1-0000000100")
    VirtualKeyspace string // Virtual keyspace name
    SchemaName      string // Database/schema name for this virtual keyspace
}
```

### Tablet Resolution Layer

A tablet resolution interface provides transparent mapping from VIRTUAL tablets to physical tablets:

```go
type TabletResolver interface {
    ResolveTablet(ctx context.Context, tablet *topodatapb.Tablet) (*topodatapb.Tablet, error)
    ResolveShardTablets(ctx context.Context, keyspace, shard string) ([]*topodatapb.Tablet, error)
    IsVirtualTablet(tablet *topodatapb.Tablet) bool
}
```

## Implementation Details

### 1. Core Infrastructure

#### TabletType Enum Extension
```protobuf
enum TabletType {
  // ... existing types ...
  VIRTUAL = 9;  // Virtual tablet that references a physical tablet
}
```

#### Helper Functions
```go
// Returns true if the tablet type is VIRTUAL
func IsVirtualType(tt topodatapb.TabletType) bool

// Returns false for VIRTUAL tablets (they don't serve queries directly)
func IsRunningQueryService(tt topodatapb.TabletType) bool

// Returns true for VIRTUAL tablets (they appear in serving graph)
func IsInServingGraph(tt topodatapb.TabletType) bool
```

### 2. Virtual Keyspace Creation

When creating a virtual keyspace, the system:

1. Creates shard entries for the virtual keyspace
2. Creates VIRTUAL tablets that reference physical tablets
3. Stores virtual keyspace metadata in tablet tags
4. Generates unique UIDs for VIRTUAL tablets (physical UID + 100000)

```go
func CreateVirtualKeyspaceShard(ctx context.Context, ts *topo.Server, 
    virtualKeyspace, physicalKeyspace, shard string) error {
    // 1. Get physical shard tablets
    // 2. Create corresponding VIRTUAL tablets
    // 3. Create virtual shard entry
    // 4. Store VIRTUAL tablets in topology
}
```

### 3. Tablet Resolution

The resolution layer transparently maps VIRTUAL tablets to physical tablets:

```go
func (r *DefaultTabletResolver) ResolveTablet(ctx context.Context, 
    tablet *topodatapb.Tablet) (*topodatapb.Tablet, error) {
    if !r.IsVirtualTablet(tablet) {
        return tablet, nil
    }
    
    // Extract physical tablet alias from tags
    physicalAlias := tablet.Tags["physical_tablet"]
    
    // Retrieve and return physical tablet
    return r.ts.GetTablet(ctx, physicalAlias)
}
```

### 4. Traffic Switching Integration

Traffic switching operations work seamlessly with virtual keyspaces:

- `changeShardsAccess()`: Resolves virtual keyspaces to physical keyspaces for shard operations
- `switchDeniedTables()`: Uses physical keyspace names for denied table operations
- `changeShardRouting()`: Updates serving status on physical shards

The key insight is that shard-level operations must use physical keyspaces, while database-level operations must use virtual keyspace schema names.

### 5. Query Routing

VTGate query routing handles virtual keyspaces by:

1. Recognizing VIRTUAL tablets in the serving graph
2. Resolving VIRTUAL tablets to physical tablets for connections
3. Using virtual keyspace schema names for database selection
4. Routing queries to the correct physical tablets

## Database Name Resolution

A critical aspect of virtual keyspaces is proper database name handling:

- **Physical Operations**: Use physical keyspace database names
- **Virtual Operations**: Use virtual keyspace schema names
- **Shard Operations**: Always use physical keyspace names
- **Query Execution**: Use virtual keyspace schema names

This is implemented through helper methods:

```go
// Returns virtual keyspace schema name if keyspace is virtual
func (s *Server) getDbNameOverride(ctx context.Context, keyspace string) string

// Returns physical keyspace name for shard operations
func (s *Server) getPhysicalKeyspaceForShardOps(ctx context.Context, keyspace string) string
```

## Benefits

### 1. Simplified Codebase
- Eliminates special-case handling in traffic switching
- Reduces complexity in workflow operations
- Standardizes virtual keyspace behavior

### 2. Improved Tooling Support
- Standard tools work with virtual keyspaces
- Consistent topology representation
- Better monitoring and observability

### 3. Enhanced Reliability
- Fewer code paths reduce bugs
- Standard operations are better tested
- Consistent behavior across components

### 4. Better Performance
- Eliminates redundant special-case checks
- Optimized tablet resolution with caching
- Reduced complexity in hot paths

## Migration Strategy

### Existing Virtual Keyspaces
Existing virtual keyspaces continue to work without modification. The system detects legacy virtual keyspaces and handles them appropriately.

### New Virtual Keyspaces
New virtual keyspaces automatically use the VIRTUAL tablet approach, providing improved functionality and performance.

### Migration Tool
A migration tool can convert existing virtual keyspaces to use VIRTUAL tablets:

```bash
vtctldclient MigrateVirtualKeyspace --keyspace=my_virtual_ks --dry-run
vtctldclient MigrateVirtualKeyspace --keyspace=my_virtual_ks
```

## Operational Considerations

### Monitoring
- Monitor virtual-to-physical mapping health
- Track VIRTUAL tablet resolution performance
- Alert on virtual keyspace inconsistencies

### Troubleshooting
- Use `GetTablet` to inspect VIRTUAL tablet metadata
- Verify physical tablet references are valid
- Check virtual keyspace schema name consistency

### Best Practices
- Always validate virtual keyspace creation
- Monitor physical tablet health for virtual keyspaces
- Use consistent naming conventions for virtual keyspaces

## Testing Strategy

### Unit Tests
- VIRTUAL tablet type helper functions
- Tablet resolution logic
- Virtual keyspace creation and deletion
- Error handling for invalid mappings

### Integration Tests
- End-to-end virtual keyspace workflows
- Traffic switching with virtual keyspaces
- Query routing through virtual keyspaces
- Failure scenarios and recovery

### Performance Tests
- Virtual tablet resolution overhead
- Large-scale virtual keyspace operations
- Cache effectiveness and hit rates

## Future Enhancements

### Caching
Implement intelligent caching for virtual-to-physical tablet mappings to minimize topology lookups.

### Advanced Routing
Support more sophisticated routing rules for virtual keyspaces, including cross-shard virtual keyspaces.

### Multi-Physical Support
Allow virtual keyspaces to span multiple physical keyspaces for advanced use cases.

## Conclusion

The VIRTUAL tablet type provides a clean, scalable solution for virtual keyspaces in Vitess. By treating virtual keyspaces as first-class citizens in the topology, we eliminate special-case handling while maintaining full compatibility with existing functionality.

This design significantly simplifies the codebase, improves reliability, and provides a foundation for future virtual keyspace enhancements.
