# Virtual Keyspaces

Virtual keyspaces in Vitess allow you to create logical database schemas that map to existing physical keyspaces. This feature enables database name abstraction and multi-tenancy scenarios while leveraging the same underlying physical infrastructure.

## Overview

A virtual keyspace provides:
- **Database Name Abstraction**: Present different database names to applications while using the same physical keyspace
- **Multi-tenancy**: Multiple logical databases sharing the same physical infrastructure
- **Schema Isolation**: Each virtual keyspace can have its own schema while sharing physical tablets
- **Transparent Operations**: Virtual keyspaces work seamlessly with existing Vitess operations like traffic switching and workflows

## How Virtual Keyspaces Work

Virtual keyspaces are implemented using VIRTUAL tablets that reference physical tablets. When you create a virtual keyspace:

1. **Virtual Keyspace Entry**: A new keyspace entry is created in the topology
2. **VIRTUAL Tablets**: Special tablet entries are created that reference physical tablets
3. **Schema Mapping**: The virtual keyspace name maps to a specific database schema on the physical tablets
4. **Transparent Resolution**: Queries to virtual keyspaces are automatically routed to the correct physical tablets

### Architecture

```
Virtual Keyspace "tenant_a"     Physical Keyspace "commerce"
├── Virtual Shard "-"           ├── Physical Shard "-"
│   ├── VIRTUAL Tablet (PRIMARY)│   ├── Physical Tablet (PRIMARY)
│   │   └── → References ────────┘   │   ├── Database: commerce
│   ├── VIRTUAL Tablet (REPLICA)│   │   └── Schema: tenant_a_db
│   │   └── → References ────────────┤   ├── Physical Tablet (REPLICA)
│   └── VIRTUAL Tablet (RDONLY) │   │   ├── Database: commerce  
│       └── → References ────────────┘   └── Schema: tenant_a_db
└── Schema Name: "tenant_a_db"      └── Physical Tablet (RDONLY)
```

## Creating Virtual Keyspaces

### Prerequisites

1. **Physical Keyspace**: You need an existing physical keyspace with running tablets
2. **Schema Creation**: The target schema must exist on the physical tablets
3. **Permissions**: Appropriate permissions to create keyspaces and modify topology

### Basic Creation

```bash
# Create a virtual keyspace that maps to a physical keyspace
vtctldclient CreateVirtualKeyspace \
  --keyspace=tenant_a \
  --physical-keyspace=commerce \
  --schema-name=tenant_a_db
```

### Parameters

- `--keyspace`: Name of the virtual keyspace to create
- `--physical-keyspace`: Name of the existing physical keyspace to map to
- `--schema-name`: Database/schema name to use on the physical tablets

### Verification

```bash
# List all keyspaces (should include your virtual keyspace)
vtctldclient GetKeyspaces

# Get virtual keyspace details
vtctldclient GetKeyspace tenant_a

# List tablets in the virtual keyspace
vtctldclient GetTablets --keyspace=tenant_a
```

## Using Virtual Keyspaces

### Database Connections

Applications connect to virtual keyspaces just like regular keyspaces:

```python
# Python example using mysql-connector
import mysql.connector

# Connect to virtual keyspace
conn = mysql.connector.connect(
    host='vtgate-host',
    port=15306,
    database='tenant_a',  # Virtual keyspace name
    user='app_user',
    password='password'
)

# Queries work normally
cursor = conn.cursor()
cursor.execute("SELECT * FROM products")
results = cursor.fetchall()
```

### Query Routing

Queries to virtual keyspaces are automatically routed to the correct physical tablets:

```sql
-- This query to virtual keyspace "tenant_a"
USE tenant_a;
SELECT * FROM products WHERE id = 123;

-- Gets routed to physical keyspace "commerce" 
-- using schema "tenant_a_db"
```

### VTGate Configuration

No special VTGate configuration is required. VTGate automatically discovers virtual keyspaces from the topology and routes queries appropriately.

## Traffic Switching with Virtual Keyspaces

Virtual keyspaces work seamlessly with Vitess traffic switching operations:

### MoveTables

```bash
# Move tables from source to virtual keyspace target
vtctldclient MoveTables --workflow=move_products \
  --source-keyspace=legacy_db \
  --target-keyspace=tenant_a \
  create

# Switch traffic
vtctldclient MoveTables --workflow=move_products \
  --target-keyspace=tenant_a \
  SwitchTraffic
```

### Reshard

```bash
# Reshard a virtual keyspace (operates on underlying physical keyspace)
vtctldclient Reshard --workflow=reshard_tenant \
  --source-shards="-" \
  --target-shards="-80,80-" \
  --target-keyspace=tenant_a \
  create
```

## Management Operations

### Listing Virtual Keyspaces

```bash
# List all keyspaces (virtual keyspaces are marked with type)
vtctldclient GetKeyspaces

# Get detailed information about a virtual keyspace
vtctldclient GetKeyspace tenant_a
```

### Updating Virtual Keyspaces

```bash
# Update virtual keyspace properties
vtctldclient UpdateKeyspace \
  --keyspace=tenant_a \
  --served-from="primary:commerce"
```

### Deleting Virtual Keyspaces

```bash
# Delete a virtual keyspace (does not affect physical keyspace)
vtctldclient DeleteVirtualKeyspace --keyspace=tenant_a
```

**⚠️ Warning**: Deleting a virtual keyspace removes all VIRTUAL tablet entries and the virtual keyspace topology entry. The physical keyspace and its data remain unchanged.

## Best Practices

### Schema Management

1. **Consistent Schema Names**: Use consistent naming conventions for virtual keyspace schemas
2. **Schema Isolation**: Ensure each virtual keyspace uses a unique schema name
3. **Migration Planning**: Plan schema migrations across all virtual keyspaces sharing a physical keyspace

### Monitoring

1. **Virtual Keyspace Health**: Monitor virtual keyspace availability separately from physical keyspaces
2. **Query Performance**: Track query performance for virtual keyspaces
3. **Resource Usage**: Monitor resource usage on physical tablets serving multiple virtual keyspaces

### Security

1. **Access Control**: Implement proper access controls for each virtual keyspace
2. **Data Isolation**: Ensure proper data isolation between virtual keyspaces
3. **Audit Logging**: Maintain audit logs for virtual keyspace operations

## Troubleshooting

### Common Issues

#### Virtual Keyspace Not Found

```bash
# Check if virtual keyspace exists in topology
vtctldclient GetKeyspace tenant_a

# Verify virtual tablets are created
vtctldclient GetTablets --keyspace=tenant_a
```

#### Connection Failures

```bash
# Check VTGate discovery of virtual keyspace
vtctldclient GetSrvKeyspace --cell=zone1 tenant_a

# Verify physical tablets are healthy
vtctldclient GetTablets --keyspace=commerce
```

#### Query Routing Issues

```bash
# Check virtual-to-physical tablet mapping
vtctldclient GetTablet zone1-0000000100

# Verify schema exists on physical tablets
mysql -h tablet-host -P 3306 -e "SHOW DATABASES LIKE 'tenant_a_db'"
```

### Debug Commands

```bash
# Get virtual tablet information
vtctldclient GetTablet --tablet-alias=zone1-0000000100

# Check physical tablet reference
vtctldclient GetTablet --tablet-alias=zone1-0000000100 | grep physical_tablet

# Verify virtual keyspace topology
vtctldclient GetKeyspace tenant_a --show-topology
```

## Limitations

### Current Limitations

1. **Single Physical Keyspace**: Each virtual keyspace maps to exactly one physical keyspace
2. **Schema Dependencies**: Virtual keyspaces depend on schemas existing on physical tablets
3. **Backup/Restore**: Backup and restore operations work at the physical keyspace level

### Planned Enhancements

1. **Cross-Physical Support**: Support for virtual keyspaces spanning multiple physical keyspaces
2. **Dynamic Schema Management**: Automatic schema creation and management
3. **Enhanced Monitoring**: Dedicated monitoring and metrics for virtual keyspaces

## Migration Guide

### From Legacy Virtual Keyspaces

If you have existing virtual keyspaces created before the VIRTUAL tablet implementation:

1. **Assessment**: Identify existing virtual keyspaces and their configurations
2. **Migration Planning**: Plan migration to new VIRTUAL tablet approach
3. **Testing**: Test migration in non-production environments
4. **Gradual Migration**: Migrate virtual keyspaces one at a time

### Migration Tool

```bash
# Check if migration is needed
vtctldclient CheckVirtualKeyspaceMigration --keyspace=tenant_a

# Perform migration (with dry-run first)
vtctldclient MigrateVirtualKeyspace --keyspace=tenant_a --dry-run
vtctldclient MigrateVirtualKeyspace --keyspace=tenant_a
```

## Examples

### Multi-Tenant SaaS Application

```bash
# Create virtual keyspaces for different tenants
vtctldclient CreateVirtualKeyspace \
  --keyspace=tenant_a \
  --physical-keyspace=saas_db \
  --schema-name=tenant_a_schema

vtctldclient CreateVirtualKeyspace \
  --keyspace=tenant_b \
  --physical-keyspace=saas_db \
  --schema-name=tenant_b_schema
```

### Database Name Abstraction

```bash
# Create virtual keyspace with application-friendly name
vtctldclient CreateVirtualKeyspace \
  --keyspace=ecommerce \
  --physical-keyspace=commerce_v2 \
  --schema-name=ecommerce_prod
```

### Development/Testing Environments

```bash
# Create virtual keyspaces for different environments
vtctldclient CreateVirtualKeyspace \
  --keyspace=app_dev \
  --physical-keyspace=shared_dev \
  --schema-name=app_dev_db

vtctldclient CreateVirtualKeyspace \
  --keyspace=app_staging \
  --physical-keyspace=shared_staging \
  --schema-name=app_staging_db
```

## See Also

- [Keyspaces](keyspaces.md) - General information about Vitess keyspaces
- [VTGate](vtgate.md) - Query routing and connection management
- [Traffic Switching](traffic-switching.md) - Moving data between keyspaces
- [Topology](topology.md) - Understanding Vitess topology management
