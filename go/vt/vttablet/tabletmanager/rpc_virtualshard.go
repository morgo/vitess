package tabletmanager

import (
	"context"
	"fmt"

	"vitess.io/vitess/go/sqlescape"
	"vitess.io/vitess/go/vt/log"
	tabletmanagerdatapb "vitess.io/vitess/go/vt/proto/tabletmanagerdata"

	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/vterrors"
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

	return &tabletmanagerdatapb.AddVirtualShardResponse{}, nil
}

// TODO:
// Add support for DropVirtualShard to remove a virtual shard.
