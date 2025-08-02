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
	"hash/fnv"
	"path"

	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/topo/topoproto"
	"vitess.io/vitess/go/vt/vterrors"
)

// This file contains virtual shard utility functions.

// tabletExists checks if a tablet exists in the topology without logging errors
// for non-existent nodes. This is used to avoid spamming logs when checking
// for available UIDs during virtual tablet creation.
func (ts *Server) tabletExists(ctx context.Context, alias *topodatapb.TabletAlias) (bool, error) {
	conn, err := ts.ConnForCell(ctx, alias.Cell)
	if err != nil {
		return false, err
	}

	tabletPath := path.Join(TabletsPath, topoproto.TabletAliasString(alias), TabletFile)
	_, _, err = conn.Get(ctx, tabletPath)
	if err != nil {
		if IsErrType(err, NoNode) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CreateVirtualTablet creates a VIRTUAL tablet that references a physical tablet.
func (ts *Server) CreateVirtualTablet(ctx context.Context, virtualKeyspace, virtualShard string, physicalTabletInfo *TabletInfo, physicalKeyspace, physicalShard, dbName string) (*topodatapb.TabletAlias, error) {
	// Generate a new tablet alias for the virtual tablet
	// Use the same cell as the physical tablet but with a different UID
	// We need to generate a unique UID for each virtual shard, not just per physical tablet

	// Start with a base UID in the virtual tablet range (100000+)
	// We'll add a hash of the virtual keyspace/shard to make it more unique
	h := fnv.New32a()
	h.Write([]byte(virtualKeyspace + "/" + virtualShard))
	hashOffset := h.Sum32() % 50000 // Use modulo to keep it within a reasonable range

	baseUID := physicalTabletInfo.Alias.Uid + 100000 + hashOffset
	virtualTabletAlias := &topodatapb.TabletAlias{
		Cell: physicalTabletInfo.Alias.Cell,
		Uid:  baseUID,
	}

	// Keep trying UIDs until we find one that doesn't exist
	for {
		// Check if tablet exists without logging "node doesn't exist" errors
		exists, err := ts.tabletExists(ctx, virtualTabletAlias)
		if err != nil {
			// Some other error occurred
			return nil, err
		}
		if !exists {
			// This UID is available
			break
		}
		// This UID is taken, try the next one
		virtualTabletAlias.Uid++
	}

	// Create the virtual tablet with VIRTUAL type
	virtualTablet := &topodatapb.Tablet{
		Alias:    virtualTabletAlias,
		Keyspace: virtualKeyspace,
		Shard:    virtualShard,
		Type:     topodatapb.TabletType_VIRTUAL,
		PortMap:  make(map[string]int32),
		// Set DbNameOverride to the schema name for this virtual shard
		DbNameOverride: dbName,
		// Store metadata in tags
		Tags: map[string]string{
			"physical_keyspace": physicalKeyspace,
			"physical_shard":    physicalShard,
		},
	}

	// Create the virtual tablet in the topology server
	err := ts.CreateTablet(ctx, virtualTablet)
	if err != nil {
		return nil, vterrors.Wrapf(err, "CreateVirtualTablet: failed to create virtual tablet %s in topology", topoproto.TabletAliasString(virtualTabletAlias))
	}

	return virtualTabletAlias, nil
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
