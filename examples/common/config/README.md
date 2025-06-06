# Vitess Configuration

This directory contains example configuration files for Vitess components.

## vitess.yaml

The `vitess.yaml` file is the main configuration file for Vitess components. It should be placed at `/etc/vitess.yaml` by default, but you can specify a different location using the `--vitess-config-file` flag.

### Configuration Sections

#### topo

The `topo` section configures the topology server:

```yaml
topo:
  implementation: etcd2  # Valid options: zk2, etcd2, consul, mysql
  global-server-address: "localhost:2379"
  global-root: "/vitess/global"
```

#### vtctldclient

The `vtctldclient` section configures the vtctldclient:

```yaml
vtctldclient:
  server: "localhost:15999"  # Address of the vtctld server to connect to
```

With this configuration, you can run vtctldclient commands without specifying the `--server` flag:

```bash
# Without configuration file:
vtctldclient --server localhost:15999 GetKeyspaces

# With configuration file:
vtctldclient GetKeyspaces
```

#### global

The `global` section contains global configuration settings:

```yaml
global:
  cell: test
  topo_implementation: etcd2
  topo_server: localhost:2379
  topo_root: /vitess/global
```

#### tablets

The `tablets` section defines MySQL and vttablet instances:

```yaml
tablets:
  - uid: 100
    keyspace: commerce
    shard: "0"
    tablet_type: primary
    port: 15100
    grpc_port: 16100
    mysql_port: 17100
    
  - uid: 101
    keyspace: commerce
    shard: "0"
    tablet_type: replica
    port: 15101
    grpc_port: 16101
    mysql_port: 17101
```

This configuration is used by the `mysqlctl-tablet.sh` script to start, stop, and check the status of MySQL and vttablet instances.

#### vtorc

The `vtorc` section configures the vtorc service:

```yaml
vtorc:
  port: 16000
  topo_implementation: etcd2  # If not specified, uses topo.implementation
  topo_server: localhost:2379  # If not specified, uses topo.global-server-address
  topo_root: /vitess/global  # If not specified, uses topo.global-root
  instance_poll_time: 5s
  topo_information_refresh_duration: 15s
  prevent_cross_cell_failover: false
  sqlite_data_file: "file::memory:?mode=memory&cache=shared"
  audit_to_backend: false
  allow_emergency_reparent: true
```

This configuration is used by the `vtorc` init script to start, stop, and check the status of the vtorc service.

## Usage

Copy the example configuration file to `/etc/vitess.yaml` and modify it according to your environment:

```bash
sudo cp vitess.yaml.example /etc/vitess.yaml
sudo vi /etc/vitess.yaml
```

### Managing MySQL and vttablet Instances

Use the `mysqlctl-tablet.sh` script to manage MySQL and vttablet instances:

```bash
# Start all MySQL and vttablet instances
./mysqlctl-tablet.sh start

# Check the status of all instances
./mysqlctl-tablet.sh status

# Stop all instances
./mysqlctl-tablet.sh stop

# Restart all instances
./mysqlctl-tablet.sh restart
```

The script will read the tablet configurations from `/etc/vitess.yaml` and manage all defined instances.

### Managing VTOrc

Use the `vtorc` init script to manage the VTOrc service:

```bash
# Start VTOrc
./vtorc start

# Check the status of VTOrc
./vtorc status

# Stop VTOrc
./vtorc stop

# Restart VTOrc
./vtorc restart
```

The script will read the VTOrc configuration from `/etc/vitess.yaml` and manage the service accordingly.