/*
Copyright 2025 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package topo

import (
	"context"
	"fmt"

	"vitess.io/vitess/go/vt/log"

	tabletmanagerdatapb "vitess.io/vitess/go/vt/proto/tabletmanagerdata"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/topo/topoproto"
	"vitess.io/vitess/go/vt/vterrors"
	"vitess.io/vitess/go/vt/vttablet/tmclient"
)

// This file contains virtual shard utility functions.

// CreateVirtualShard creates a virtual shard that maps to a physical shard.
// This creates a shard entry with VIRTUAL tablets that reference the physical tablets.
func (ts *Server) CreateVirtualShard(ctx context.Context, virtualKeyspace, virtualShard, physicalKeyspace, physicalShard string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if err := ValidateKeyspaceName(virtualKeyspace); err != nil {
		return vterrors.Wrapf(err, "CreateVirtualShard: invalid virtual keyspace name %s", virtualKeyspace)
	}

	if err := ValidateKeyspaceName(physicalKeyspace); err != nil {
		return vterrors.Wrapf(err, "CreateVirtualShard: invalid physical keyspace name %s", physicalKeyspace)
	}

	// Validate shard names
	if _, _, err := ValidateShardName(virtualShard); err != nil {
		return vterrors.Wrapf(err, "CreateVirtualShard: invalid virtual shard name %s", virtualShard)
	}

	if _, _, err := ValidateShardName(physicalShard); err != nil {
		return vterrors.Wrapf(err, "CreateVirtualShard: invalid physical shard name %s", physicalShard)
	}

	// Verify that the physical shard exists
	physicalShardInfo, err := ts.GetShard(ctx, physicalKeyspace, physicalShard)
	if err != nil {
		return vterrors.Wrapf(err, "CreateVirtualShard: physical shard %s/%s does not exist", physicalKeyspace, physicalShard)
	}

	// Create the virtual keyspace if it doesn't exist (as a regular keyspace)
	virtualKsi := &topodatapb.Keyspace{
		KeyspaceType: topodatapb.KeyspaceType_NORMAL,
	}
	if err = ts.CreateKeyspace(ctx, virtualKeyspace, virtualKsi); err != nil && !IsErrType(err, NodeExists) {
		return vterrors.Wrapf(err, "CreateVirtualShard: failed to create keyspace %v", virtualKeyspace)
	}

	// Create the virtual shard with metadata pointing to the physical shard
	virtualShardData := &topodatapb.Shard{
		KeyRange:         physicalShardInfo.KeyRange,
		IsPrimaryServing: physicalShardInfo.IsPrimaryServing,
	}

	data, err := virtualShardData.MarshalVT()
	if err != nil {
		return err
	}

	// Create the virtual shard
	virtualShardPath := shardFilePath(virtualKeyspace, virtualShard)
	if _, err := ts.globalCell.Create(ctx, virtualShardPath, data); err != nil {
		return vterrors.Wrapf(err, "CreateVirtualShard: failed to create virtual shard %s/%s", virtualKeyspace, virtualShard)
	}

	// Get all tablets from the physical shard
	physicalTablets, err := ts.GetTabletMapForShard(ctx, physicalKeyspace, physicalShard)
	if err != nil {
		return vterrors.Wrapf(err, "CreateVirtualShard: failed to get tablets for physical shard %s/%s", physicalKeyspace, physicalShard)
	}

	// Generate schema name for the virtual shard
	schemaName := fmt.Sprintf("vt_%s_%s", virtualKeyspace, virtualShard)

	// Create VIRTUAL tablets for each physical tablet and ensure the database schema is created
	var virtualPrimaryAlias *topodatapb.TabletAlias
	for _, physicalTabletInfo := range physicalTablets {
		virtualTabletAlias, err := ts.createVirtualTablet(ctx, virtualKeyspace, virtualShard, physicalTabletInfo, physicalKeyspace, physicalShard, schemaName)
		if err != nil {
			if IsErrType(err, NodeExists) {
				log.Warningf("CreateVirtualShard: virtual tablet node already exists in topology for %s/%s", virtualKeyspace, virtualShard)
			} else {
				return vterrors.Wrapf(err, "CreateVirtualShard: failed to create virtual tablet for %s", physicalTabletInfo.AliasString())
			}
		}

		// If this is the primary tablet, set it as the virtual shard's primary
		if physicalTabletInfo.Type == topodatapb.TabletType_PRIMARY {
			virtualPrimaryAlias = virtualTabletAlias
		}
	}

	// Now ensure the database schema is created on the physical primary tablet
	// We only need to call AddVirtualShard on the primary tablet since the database
	// creation will replicate to the replicas automatically
	var primaryTablet *TabletInfo
	for _, physicalTabletInfo := range physicalTablets {
		if physicalTabletInfo.Type == topodatapb.TabletType_PRIMARY {
			primaryTablet = physicalTabletInfo
			break
		}
	}

	if primaryTablet == nil {
		return vterrors.Errorf(vtrpcpb.Code_FAILED_PRECONDITION, "CreateVirtualShard: no primary tablet found in physical shard %s/%s", physicalKeyspace, physicalShard)
	}

	err = ts.addVirtualShardToPhysicalTablet(ctx, primaryTablet, virtualKeyspace, virtualShard, physicalKeyspace, physicalShard, schemaName)
	if err != nil {
		return vterrors.Wrapf(err, "CreateVirtualShard: failed to add virtual shard to primary tablet %s", primaryTablet.AliasString())
	}

	// Update the virtual shard to set the primary alias
	if virtualPrimaryAlias != nil {
		_, err = ts.UpdateShardFields(ctx, virtualKeyspace, virtualShard, func(si *ShardInfo) error {
			si.PrimaryAlias = virtualPrimaryAlias
			return nil
		})
		if err != nil {
			return vterrors.Wrapf(err, "CreateVirtualShard: failed to set primary alias for virtual shard %s/%s", virtualKeyspace, virtualShard)
		}
	}

	return nil
}

// createVirtualTablet creates a VIRTUAL tablet that references a physical tablet.
func (ts *Server) createVirtualTablet(ctx context.Context, virtualKeyspace, virtualShard string, physicalTabletInfo *TabletInfo, physicalKeyspace, physicalShard, schemaName string) (*topodatapb.TabletAlias, error) {
	// Generate a new tablet alias for the virtual tablet
	// Use the same cell as the physical tablet but with a different UID
	virtualTabletAlias := &topodatapb.TabletAlias{
		Cell: physicalTabletInfo.Alias.Cell,
		// Use a high UID range for virtual tablets to avoid conflicts
		// Add 100000 to the physical tablet UID to create virtual tablet UID
		Uid: physicalTabletInfo.Alias.Uid + 100000,
	}

	// Create the virtual tablet with VIRTUAL type
	virtualTablet := &topodatapb.Tablet{
		Alias:    virtualTabletAlias,
		Keyspace: virtualKeyspace,
		Shard:    virtualShard,
		Type:     topodatapb.TabletType_VIRTUAL,
		// Copy key range from physical tablet
		KeyRange: physicalTabletInfo.KeyRange,
		// Set hostname and ports to empty since VIRTUAL tablets don't run services
		Hostname: "",
		PortMap:  make(map[string]int32),
		// Set DbNameOverride to the schema name for this virtual shard
		DbNameOverride: schemaName,
		// Store metadata in tags
		Tags: map[string]string{
			"physical_tablet":   topoproto.TabletAliasString(physicalTabletInfo.Alias),
			"physical_keyspace": physicalKeyspace,
			"physical_shard":    physicalShard,
			"virtual_shard":     "true",
			"schema_name":       schemaName,
		},
	}

	// Create the virtual tablet
	err := ts.CreateTablet(ctx, virtualTablet)
	if err != nil {
		return nil, err
	}

	return virtualTabletAlias, nil
}

// addVirtualShardToPhysicalTablet creates the database schema on a physical tablet for a virtual shard
func (ts *Server) addVirtualShardToPhysicalTablet(ctx context.Context, physicalTabletInfo *TabletInfo, virtualKeyspace, virtualShard, physicalKeyspace, physicalShard, schemaName string) error {
	// Create a tablet manager client to call AddVirtualShard on the physical tablet
	tmc := tmclient.NewTabletManagerClient()
	defer tmc.Close()

	// Prepare the AddVirtualShard request
	req := &tabletmanagerdatapb.AddVirtualShardRequest{
		VirtualKeyspace:  virtualKeyspace,
		VirtualShard:     virtualShard,
		PhysicalKeyspace: physicalKeyspace,
		PhysicalShard:    physicalShard,
		SchemaName:       schemaName,
	}

	// Call AddVirtualShard on the physical tablet
	_, err := tmc.AddVirtualShard(ctx, physicalTabletInfo.Tablet, req)
	if err != nil {
		return vterrors.Wrapf(err, "failed to add virtual shard %s/%s to physical tablet %s", virtualKeyspace, virtualShard, physicalTabletInfo.AliasString())
	}

	return nil
}

// IsVirtualShard returns true if the shard is a virtual shard.
// We identify virtual shards by checking if there are VIRTUAL tablets in the shard.
func IsVirtualShard(ctx context.Context, ts *Server, keyspace, shard string) (bool, error) {
	// We need to use GetTabletMapForShardWithoutResolving to avoid automatic
	// resolution of VIRTUAL tablets to their physical counterparts
	tablets, err := ts.GetTabletMapForShardWithoutResolving(ctx, keyspace, shard)
	if err != nil {
		return false, err
	}

	for _, tablet := range tablets {
		if tablet.Type == topodatapb.TabletType_VIRTUAL {
			return true, nil
		}
	}
	return false, nil
}

// GetPhysicalShardInfo returns the physical keyspace and shard for a virtual shard.
// It does this by looking at the VIRTUAL tablets in the shard and extracting the
// physical shard information from their tags.
func GetPhysicalShardInfo(ctx context.Context, ts *Server, virtualKeyspace, virtualShard string) (physicalKeyspace, physicalShard string, err error) {
	tablets, err := ts.GetTabletMapForShardWithoutResolving(ctx, virtualKeyspace, virtualShard)
	if err != nil {
		return "", "", err
	}

	// Find a VIRTUAL tablet to get the physical shard info
	for _, tablet := range tablets {
		if tablet.Type == topodatapb.TabletType_VIRTUAL {
			if tablet.Tags == nil {
				return "", "", vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "VIRTUAL tablet %s missing tags", tablet.AliasString())
			}

			physicalKeyspace = tablet.Tags["physical_keyspace"]
			physicalShard = tablet.Tags["physical_shard"]

			if physicalKeyspace == "" || physicalShard == "" {
				return "", "", vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "VIRTUAL tablet %s missing physical shard metadata", tablet.AliasString())
			}

			return physicalKeyspace, physicalShard, nil
		}
	}

	return "", "", vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "shard %s/%s is not a virtual shard", virtualKeyspace, virtualShard)
}
