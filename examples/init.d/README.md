# Readme

This directory provides a series of scripts that start Vitess services (topo, vtctld, vtgate, tablets etc).

They are intended to work like init.d scripts, so for example you can call then in sequence as:
```
/etc/init.d/topo start
/etc/init.d/vtctld start
```

.. and in future sequence shouldn't strictly be required, since they should be a bit smarter at waiting for each-other.

My intention is that they replace `common/scripts/*` but I will give users a chance to migrate over.

They make extensive use of `/etc/vitess.yaml` as a declarative source of configuration. For example, `mysqlctl-vttablet start` will start as many tablets as are defined here. If you add a new tablet in configuration, and then re-execute `start` it will launch any new tablets that hadn't previously started.

## Configuration Structure

The configuration file `/etc/vitess.yaml` uses the following structure:

```yaml
global:
  cell: zone1
  vtdataroot: /path/to/vtdataroot
  hostname: localhost
  topo-implementation: etcd2
  topo-global-server-address: localhost:2379
  topo-global-root: /vitess/global

vtgate:
  web-port: 15001
  grpc-port: 15991
  mysql-server-port: 15306
  tablet-types-to-wait: PRIMARY,REPLICA
  cell: zone1
  cells_to_watch: zone1
  service_map: grpc-vtgateservice
  mysql_auth_server_impl: none

vtorc:
  port: 16000
  instance-poll-time: 1s
  prevent-cross-cell-failover: false
  # other vtorc settings...

vtctldclient:
  server: localhost:15999

tablets:
  - uid: 100
    keyspace: commerce
    shard: "0"
    tablet_type: replica
    port: 15100
    grpc_port: 16100
    mysql_port: 17100
    # For unmanaged tablets, add:
    unmanaged: true
    db_app_user: vt_app
    db_app_password: password
    db_dba_user: vt_dba
    db_dba_password: password
    db_repl_user: vt_repl
    db_repl_password: password
    db_filtered_user: vt_filtered
    db_filtered_password: password
    db_allprivs_user: vt_allprivs
    db_allprivs_password: password
    init_db_name_override: vt_commerce
```

## Unmanaged Tablets

The scripts now support unmanaged tablets through the `/etc/vitess.yaml` configuration file. For unmanaged tablets, set `unmanaged: true` in the tablet configuration section.

When a tablet is configured as unmanaged, the `mysqlctl-vttablet` script will only launch the vttablet process and will not execute mysqlctl commands to manage MySQL.

For unmanaged tablets, you can configure the following database connection parameters:

- `db_app_user`, `db_app_password`: Application user credentials
- `db_dba_user`, `db_dba_password`: Database administrator credentials
- `db_repl_user`, `db_repl_password`: Replication user credentials
- `db_filtered_user`, `db_filtered_password`: Filtered replication user credentials
- `db_allprivs_user`, `db_allprivs_password`: All privileges user credentials
- `init_db_name_override`: Override for the initial database name

For more information on unmanaged tablets, see the [Vitess documentation](https://vitess.io/docs/user-guides/configuration-advanced/unmanaged-tablet/).


## TODO

- Check that the topos for zk and consul actually work.
- Move these scripts into bin/* and move the local scripts into here.
