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

	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	"vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/topo/topoproto"
	"vitess.io/vitess/go/vt/vterrors"
)

// TabletResolver provides methods for resolving VIRTUAL tablets to their
// physical counterparts.
type TabletResolver interface {
	// ResolveTablet resolves a VIRTUAL tablet to its physical tablet.
	// If the tablet is not VIRTUAL, it returns the tablet unchanged.
	ResolveTablet(ctx context.Context, tablet *topodatapb.Tablet) (*topodatapb.Tablet, error)

	// ResolveShardTablets resolves all VIRTUAL tablets in a shard to their
	// physical counterparts.
	ResolveShardTablets(ctx context.Context, keyspace, shard string) ([]*topodatapb.Tablet, error)
}

// DefaultTabletResolver implements the TabletResolver interface using the
// standard topology server operations.
type DefaultTabletResolver struct {
	ts *Server
}

// NewDefaultTabletResolver creates a new DefaultTabletResolver.
func NewDefaultTabletResolver(ts *Server) *DefaultTabletResolver {
	return &DefaultTabletResolver{ts: ts}
}

// ResolveTablet resolves a VIRTUAL tablet to its physical tablet.
// If the tablet is not VIRTUAL, it returns the tablet unchanged.
func (r *DefaultTabletResolver) ResolveTablet(ctx context.Context, tablet *topodatapb.Tablet) (*topodatapb.Tablet, error) {
	if !IsVirtualType(tablet.Type) {
		return tablet, nil
	}

	// Get the physical keyspace and shard from the VIRTUAL tablet's tags
	physicalKeyspace, physicalShard, err := GetPhysicalShardInfo(ctx, r.ts, tablet.Keyspace, tablet.Shard)
	if err != nil {
		return nil, err
	}

	// Find an appropriate physical tablet in the physical shard
	physicalTablet, err := r.findPhysicalTabletForVirtual(ctx, physicalKeyspace, physicalShard, tablet)
	if err != nil {
		return nil, vterrors.Wrapf(err, "failed to resolve VIRTUAL tablet %s to physical tablet in %s/%s",
			topoproto.TabletAliasString(tablet.Alias), physicalKeyspace, physicalShard)
	}

	// The dbname is stored in the VIRTUAL tablet's DbNameOverride
	physicalTablet.DbNameOverride = tablet.GetDbNameOverride()
	return physicalTablet, nil
}

// ResolveShardTablets resolves all VIRTUAL tablets in a shard to their
// physical counterparts.
func (r *DefaultTabletResolver) ResolveShardTablets(ctx context.Context, keyspace, shard string) ([]*topodatapb.Tablet, error) {
	// Get all tablets in the shard
	tabletMap, err := r.ts.GetTabletMapForShard(ctx, keyspace, shard)
	if err != nil {
		return nil, err
	}

	var resolvedTablets []*topodatapb.Tablet
	for _, tabletInfo := range tabletMap {
		resolvedTablet, err := r.ResolveTablet(ctx, tabletInfo.Tablet)
		if err != nil {
			return nil, err
		}
		resolvedTablets = append(resolvedTablets, resolvedTablet)
	}

	return resolvedTablets, nil
}

// IsVirtualTablet returns true if the tablet is a VIRTUAL tablet.
func IsVirtualTablet(tablet *topodatapb.Tablet) bool {
	return IsVirtualType(tablet.Type)
}

// findPhysicalTabletForVirtual finds an appropriate physical tablet for a virtual tablet.
// It looks for a tablet of the same type as the virtual tablet in the physical shard.
func (r *DefaultTabletResolver) findPhysicalTabletForVirtual(ctx context.Context, physicalKeyspace, physicalShard string, virtualTablet *topodatapb.Tablet) (*topodatapb.Tablet, error) {
	// Get all tablets in the physical shard
	physicalTablets, err := r.ts.GetTabletMapForShard(ctx, physicalKeyspace, physicalShard)
	if err != nil {
		return nil, err
	}

	// Find a tablet of the same type as the virtual tablet would represent
	// For now, we'll look for a PRIMARY tablet as the default choice
	// TODO: This logic may need to be more sophisticated based on requirements
	for _, tabletInfo := range physicalTablets {
		if tabletInfo.Type == topodatapb.TabletType_PRIMARY {
			return tabletInfo.Tablet, nil
		}
	}
	// If no suitable tablet found, return an error
	return nil, vterrors.Errorf(vtrpc.Code_NOT_FOUND, "no suitable physical tablet found in shard %s/%s",
		physicalKeyspace, physicalShard)
}

// GetVirtualKeyspaceName extracts the virtual keyspace name from a VIRTUAL tablet's keyspace field.
// For virtual shards, the virtual keyspace is just the tablet's keyspace.
func GetVirtualKeyspaceName(virtualTablet *topodatapb.Tablet) (string, error) {
	if !IsVirtualType(virtualTablet.Type) {
		return "", vterrors.Errorf(vtrpc.Code_INVALID_ARGUMENT, "tablet %s is not a VIRTUAL tablet",
			topoproto.TabletAliasString(virtualTablet.Alias))
	}

	return virtualTablet.Keyspace, nil
}
