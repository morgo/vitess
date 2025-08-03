package tabletmanager

import (
	"context"
	"fmt"

	"vitess.io/vitess/go/sqlescape"
	"vitess.io/vitess/go/vt/log"
	tabletmanagerdatapb "vitess.io/vitess/go/vt/proto/tabletmanagerdata"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/topo"
	"vitess.io/vitess/go/vt/vterrors"
	"vitess.io/vitess/go/vt/vttablet/tabletserver"
)

// AddVirtualShard adds a new virtual shard to the existing set of shards this tablet hosts.
// Currently this requires two things:
// 1. Create a schema for the virtual shard.
// 2. Add a subscription in the VReplication engine.
func (tm *TabletManager) AddVirtualShard(ctx context.Context, req *tabletmanagerdatapb.AddVirtualShardRequest) (*tabletmanagerdatapb.AddVirtualShardResponse, error) {
	if req == nil {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "invalid request, no request provided")
	}
	if req.VirtualKeyspace == "" {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "invalid request, no virtual keyspace provided")
	}
	if req.VirtualShard == "" {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "invalid request, no virtual shard provided")
	}
	if req.PhysicalKeyspace == "" {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "invalid request, no physical keyspace provided")
	}
	if req.PhysicalShard == "" {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "invalid request, no physical shard provided")
	}
	if req.SchemaName == "" {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "invalid request, no schema name provided")
	}
	// Create the schema name req.SchemaName
	sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", sqlescape.EscapeID(req.SchemaName))
	if err := tm.MysqlDaemon.ExecuteSuperQuery(ctx, sql); err != nil {
		return nil, vterrors.Wrapf(err, "failed to create schema %s for virtual shard %s/%s", req.SchemaName, req.VirtualKeyspace, req.VirtualShard)
	}
	log.Infof("AddVirtualShard: created schema %s for virtual shard %s/%s", req.SchemaName, req.VirtualKeyspace, req.VirtualShard)

	// Add the virtual shard to the VReplication engine
	err := tm.VREngine.AddVirtualShard(req.VirtualKeyspace, req.VirtualShard, req.SchemaName)
	if err != nil {
		return nil, vterrors.Wrapf(err, "failed to add virtual shard %s/%s to VReplication engine", req.VirtualKeyspace, req.VirtualShard)
	}

	// Add the virtual tablet to the registry
	// Create a virtual tablet info for the registry
	virtualTabletInfo := &topo.TabletInfo{
		Tablet: &topodatapb.Tablet{
			Alias:          tm.Tablet().Alias,
			Hostname:       tm.Tablet().Hostname,
			PortMap:        tm.Tablet().PortMap,
			Keyspace:       req.VirtualKeyspace,
			Shard:          req.VirtualShard,
			Type:           tm.Tablet().Type,
			MysqlHostname:  tm.Tablet().MysqlHostname,
			MysqlPort:      tm.Tablet().MysqlPort,
			DbNameOverride: req.SchemaName,
			Tags: map[string]string{
				topo.PhysicalKeyspaceTag: req.PhysicalKeyspace,
				topo.PhysicalShardTag:    req.PhysicalShard,
			},
		},
	}

	// Add the virtual tablet to the registry through the query service
	if tm.QueryServiceControl != nil {
		if tabletServer, ok := tm.QueryServiceControl.(*tabletserver.TabletServer); ok {
			// Access the registry through the TabletServer
			registry := tabletServer.Registry()
			if registry != nil {
				err = registry.AddTablet(virtualTabletInfo)
				if err != nil {
					return nil, vterrors.Wrapf(err, "failed to add virtual tablet %s/%s to registry", req.VirtualKeyspace, req.VirtualShard)
				}
				log.Infof("AddVirtualShard: added virtual tablet %s/%s to registry", req.VirtualKeyspace, req.VirtualShard)
			}
		}
	}

	return &tabletmanagerdatapb.AddVirtualShardResponse{}, nil
}
