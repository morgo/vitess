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

// AddVirtualKeyspace adds a new virtual keyspace to the existing set of keyspaces this tablet hosts.
// Currently this requires two things:
// 1. Create a schema for the virtual keyspace.
// 2. Add a subscription in the VReplication engine.
func (tm *TabletManager) AddVirtualKeyspace(ctx context.Context, req *tabletmanagerdatapb.AddVirtualKeyspaceRequest) (*tabletmanagerdatapb.AddVirtualKeyspaceResponse, error) {
	if req == nil {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "invalid request, no request provided")
	}
	if req.VirtualKeyspace == "" {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "invalid request, no virtual keyspace provided")
	}
	if req.PhysicalKeyspace == "" {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "invalid request, no physical keyspace provided")
	}
	if req.SchemaName == "" {
		return nil, vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "invalid request, no schema name provided")
	}
	// Create the schema name req.SchemaName
	sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", sqlescape.EscapeID(req.SchemaName))
	if err := tm.MysqlDaemon.ExecuteSuperQuery(ctx, sql); err != nil {
		return nil, vterrors.Wrapf(err, "failed to create schema %s for virtual keyspace %s", req.SchemaName, req.VirtualKeyspace)
	}
	log.Infof("AddVirtualKeyspace: created schema %s for virtual keyspace %s", req.SchemaName, req.VirtualKeyspace)

	// Add the virtual keyspace to the VReplication engine
	err := tm.VREngine.AddVirtualKeyspace(req.VirtualKeyspace, req.SchemaName)
	if err != nil {
		return nil, vterrors.Wrapf(err, "failed to add virtual keyspace %s to VReplication engine", req.VirtualKeyspace)
	}

	return &tabletmanagerdatapb.AddVirtualKeyspaceResponse{}, nil
}

// TODO:
// Add support for DropVirtualKeyspace to remove a virtual keyspace.
