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

	// Get the physical tablet alias from the VIRTUAL tablet's tags
	physicalTabletAlias, err := GetPhysicalTabletAlias(tablet)
	if err != nil {
		return nil, err
	}

	// Get the physical tablet
	physicalTabletInfo, err := r.ts.GetTablet(ctx, physicalTabletAlias)
	if err != nil {
		return nil, vterrors.Wrapf(err, "failed to resolve VIRTUAL tablet %s to physical tablet %s",
			topoproto.TabletAliasString(tablet.Alias), topoproto.TabletAliasString(physicalTabletAlias))
	}

	// The dbname is stored in the VIRTUAL tablet's tags
	physicalTabletInfo.DbNameOverride = tablet.GetDbNameOverride()
	return physicalTabletInfo.Tablet, nil
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

// GetPhysicalTabletAlias extracts the physical tablet alias from a VIRTUAL tablet's tags.
func GetPhysicalTabletAlias(virtualTablet *topodatapb.Tablet) (*topodatapb.TabletAlias, error) {
	if !IsVirtualType(virtualTablet.Type) {
		return nil, vterrors.Errorf(vtrpc.Code_INVALID_ARGUMENT, "tablet %s is not a VIRTUAL tablet",
			topoproto.TabletAliasString(virtualTablet.Alias))
	}

	physicalTabletAliasStr, ok := virtualTablet.Tags[PhysicalTabletTag]
	if !ok {
		return nil, vterrors.Errorf(vtrpc.Code_INVALID_ARGUMENT, "VIRTUAL tablet %s missing physical_tablet tag",
			topoproto.TabletAliasString(virtualTablet.Alias))
	}

	physicalTabletAlias, err := topoproto.ParseTabletAlias(physicalTabletAliasStr)
	if err != nil {
		return nil, vterrors.Wrapf(err, "invalid physical_tablet alias %s in VIRTUAL tablet %s",
			physicalTabletAliasStr, topoproto.TabletAliasString(virtualTablet.Alias))
	}

	return physicalTabletAlias, nil
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

// GetSchemaName extracts the schema name from a VIRTUAL tablet's tags.
// This represents the MySQL schema name used for this virtual shard.
func GetSchemaName(virtualTablet *topodatapb.Tablet) (string, error) {
	if !IsVirtualType(virtualTablet.Type) {
		return "", vterrors.Errorf(vtrpc.Code_INVALID_ARGUMENT, "tablet %s is not a VIRTUAL tablet",
			topoproto.TabletAliasString(virtualTablet.Alias))
	}

	schemaName, ok := virtualTablet.Tags["schema_name"]
	if !ok {
		return "", vterrors.Errorf(vtrpc.Code_INVALID_ARGUMENT, "VIRTUAL tablet %s missing schema_name tag",
			topoproto.TabletAliasString(virtualTablet.Alias))
	}

	return schemaName, nil
}
