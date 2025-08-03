package registry

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"vitess.io/vitess/go/vt/log"
	querypb "vitess.io/vitess/go/vt/proto/query"
	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/topo"
	"vitess.io/vitess/go/vt/vterrors"
)

type Registry interface {
	// Init initializes the registry with the given target.
	Init(ctx context.Context, target *querypb.Target) error
	// ResolveTarget resolves the given target to a physical target and an optional database name override.
	ResolveTarget(ctx context.Context, target *querypb.Target) (*querypb.Target, string, error)
	// ResolveDbName resolves a database name to a tablet.
	ResolveDbName(dbName string) (*topo.TabletInfo, error)
	// AddTablet adds a tablet to the registry.
	AddTablet(tablet *topo.TabletInfo) error
	// RemoveTablet removes a tablet from the registry.
	RemoveTablet(keyspace, shard string) error
}
type TopoRegistry struct {
	ts             *topo.Server
	r              topo.TabletResolver
	physicalTarget *querypb.Target
	mu             sync.Mutex // protects the maps
	// targetTablets maps a target key (keyspace/shard) to the virtual tablet that serves it.
	targetTablets map[targetKey]*topo.TabletInfo
	// dbNameTablets maps a database name to the virtual tablet that serves it.
	dbNameTablets map[string]*topo.TabletInfo
}

var _ Registry = (*TopoRegistry)(nil)

type targetKey struct {
	Keyspace string
	Shard    string
}

func NewTopoRegistry(topoServer *topo.Server) *TopoRegistry {
	return &TopoRegistry{
		ts:            topoServer,
		r:             topo.NewDefaultTabletResolver(topoServer),
		targetTablets: make(map[targetKey]*topo.TabletInfo),
		dbNameTablets: make(map[string]*topo.TabletInfo),
	}
}

func (reg *TopoRegistry) Init(ctx context.Context, target *querypb.Target) error {
	if reg.ts == nil || reg.r == nil || reg.targetTablets == nil || reg.dbNameTablets == nil {
		return vterrors.New(vtrpcpb.Code_INTERNAL, "registry not properly constructed: topo server or physical tablet alias is nil")
	}

	if target == nil {
		return vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "target cannot be nil")
	}
	reg.physicalTarget = target

	reg.mu.Lock()
	defer reg.mu.Unlock()
	if err := reg.loadTabletsAndShards(ctx); err != nil {
		return vterrors.Wrapf(err, "failed to load tablets and shards for cell %q", reg.physicalTarget.Cell)
	}
	if len(reg.targetTablets) == 0 {
		log.Warningf("no tablets found for physical target %s/%s, this may lead to issues with resolving targets", reg.physicalTarget.Keyspace, reg.physicalTarget.Shard)
	}
	log.Infof("TopoRegistry initialized for cell %s with %d tablets", reg.physicalTarget.Cell, len(reg.targetTablets))
	return nil
}

func (reg *TopoRegistry) ResolveTarget(ctx context.Context, target *querypb.Target) (*querypb.Target, string, error) {
	log.Infof("Resolving target %s/%s", target.Keyspace, target.Shard)
	if target.Keyspace == "" {
		return nil, "", vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "target keyspace cannot be empty")
	}

	if target.Shard == "" {
		return nil, "", vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "target shard cannot be empty")
	}

	// Check if the target matches the physical target directly
	if target.Keyspace == reg.physicalTarget.Keyspace && target.Shard == reg.physicalTarget.Shard {
		log.Infof("Target %s/%s matches physical target, returning as is: %#v", target.Keyspace, target.Shard, target)
		return reg.physicalTarget, reg.physicalTarget.DbName, nil
	}

	reg.mu.Lock()
	defer reg.mu.Unlock()

	tk := targetKey{
		Keyspace: target.Keyspace,
		Shard:    target.Shard,
	}
	var dbNameOverride string
	// If this is already registered, we can return the physical target and the DB override.
	// Otherwise, lazy load it from the topology server.
	if tt, ok := reg.targetTablets[tk]; ok {
		dbNameOverride = tt.DbNameOverride
		if dbNameOverride == "" {
			// TODO: Handle the case where no DB override is found.
			// For now, we will use the physical target's DbName, even though this will probably break everything
			log.Errorf("No DB override found for target %s/%s, using physical target's DbName: %s", target.Keyspace, target.Shard, reg.physicalTarget.DbName)
			dbNameOverride = reg.physicalTarget.DbName
		}
		log.Infof("Found tablet for target %s/%s in registry, returning physical target with DB override: %s", target.Keyspace, target.Shard, dbNameOverride)
	} else {
		// If the target is not found in the registry, we need to look it up in the topology server,
		// in case it's a new target that hasn't been registered yet.

		// Check if this is a virtual keyspace by looking it up in the topology server
		log.Warningf("virtual keyspace %s not found in registry, looking in topo server", target.Keyspace)
		tablets, err := reg.r.ResolveShardTablets(ctx, target.Keyspace, target.Shard)
		if err != nil {
			if vterrors.Code(err) == vtrpcpb.Code_NOT_FOUND {
				log.Warningf("virtual keyspace %s/%s not found in topo server, falling back to naming convention", target.Keyspace, target.Shard)
				return target, formatSafeSchema(target.Keyspace, target.Shard), nil
			} else {
				return nil, "", vterrors.Wrapf(err, "failed to resolve tablets for keyspace %s and shard %s", target.Keyspace, target.Shard)
			}
		}
		if len(tablets) == 0 {
			log.Warningf("no tablets found for virtual keyspace %s/%s, falling back to naming convention", target.Keyspace, target.Shard)
			return target, formatSafeSchema(target.Keyspace, target.Shard), nil
		}
		log.Infof("Found %d tablets for virtual keyspace %s/%s", len(tablets), target.Keyspace, target.Shard)
		for _, tablet := range tablets {
			if tablet.Tags[topo.PhysicalKeyspaceTag] == reg.physicalTarget.Keyspace && tablet.Tags[topo.PhysicalShardTag] == reg.physicalTarget.Shard {
				err := reg.storeTablet(&topo.TabletInfo{Tablet: tablet})
				if err != nil {
					log.Errorf("failed to store tablet %s/%s", tablet.Keyspace, tablet.Shard)
				}
				dbNameOverride = tablet.DbNameOverride
				break // Found a matching tablet, no need to continue
			}
		}
	}

	if dbNameOverride != "" {
		return reg.physicalTarget, dbNameOverride, nil
	}

	// If we reach here, we couldn't find any mapping for the target to a specific database name override.
	// TODO: Consider whether we should return an error or a default value.
	log.Infof("No specific DB override found for target %s/%s, returning physical target with default database name", target.Keyspace, target.Shard)
	return reg.physicalTarget, reg.physicalTarget.DbName, nil
}

func (reg *TopoRegistry) ResolveDbName(dbName string) (*topo.TabletInfo, error) {
	if dbName == "" {
		return nil, vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "dbName cannot be empty")
	}

	reg.mu.Lock()
	defer reg.mu.Unlock()

	tablet, exists := reg.dbNameTablets[dbName]
	if !exists {
		return nil, vterrors.New(vtrpcpb.Code_NOT_FOUND, fmt.Sprintf("no tablet found for dbName %s", dbName))
	}

	log.Infof("Resolved dbName %s to tablet %s/%s", dbName, tablet.Keyspace, tablet.Shard)
	return tablet, nil
}

func (reg *TopoRegistry) AddTablet(tablet *topo.TabletInfo) error {
	if tablet == nil || tablet.Tablet == nil {
		return vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "tablet cannot be nil")
	}

	reg.mu.Lock()
	defer reg.mu.Unlock()

	if err := reg.storeTablet(tablet); err != nil {
		return vterrors.Wrapf(err, "failed to add tablet %s/%s", tablet.Keyspace, tablet.Shard)
	}
	log.Infof("Added tablet %s/%s to registry", tablet.Keyspace, tablet.Shard)
	return nil
}

func (reg *TopoRegistry) RemoveTablet(keyspace, shard string) error {
	if keyspace == "" || shard == "" {
		return vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "keyspace and shard cannot be empty")
	}

	reg.mu.Lock()
	defer reg.mu.Unlock()

	tk := targetKey{
		Keyspace: keyspace,
		Shard:    shard,
	}
	tablet, exists := reg.targetTablets[tk]
	if !exists {
		return vterrors.New(vtrpcpb.Code_NOT_FOUND, fmt.Sprintf("tablet for keyspace %s and shard %s not found", keyspace, shard))
	}

	// Remove from both maps
	delete(reg.targetTablets, tk)
	delete(reg.dbNameTablets, tablet.DbNameOverride)
	log.Infof("Removed tablet for keyspace %s and shard %s from registry", keyspace, shard)
	return nil
}
func (reg *TopoRegistry) loadTabletsAndShards(ctx context.Context) error {
	tablets, err := reg.ts.GetVirtualTablets(ctx, reg.physicalTarget.Cell, reg.physicalTarget.Keyspace, reg.physicalTarget.Shard)
	if err != nil {
		return vterrors.Wrapf(err, "failed to get virtual tablets for cell %q, keyspace %q, shard %q", reg.physicalTarget.Cell, reg.physicalTarget.Keyspace, reg.physicalTarget.Shard)
	}

	for _, tablet := range tablets {
		if tablet.Alias == nil || tablet.Alias.Cell != reg.physicalTarget.Cell {
			continue // Skip tablets not in the same cell
		}
		if tablet.Keyspace != reg.physicalTarget.Keyspace || tablet.Shard != reg.physicalTarget.Shard {
			log.Warningf("Tablet %s/%s does not match physical target %s/%s, skipping", tablet.Keyspace, tablet.Shard, reg.physicalTarget.Keyspace, reg.physicalTarget.Shard)
			continue // Skip tablets not in the same keyspace/shard
		}
		if err := reg.storeTablet(tablet); err != nil {
			return vterrors.Wrapf(err, "failed to store tablet %s/%s", tablet.Keyspace, tablet.Shard)
		}
	}

	return nil
}

func (reg *TopoRegistry) storeTablet(tablet *topo.TabletInfo) error {
	if tablet == nil || tablet.Tablet == nil {
		return vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "tablet cannot be nil")
	}

	// Check if the tablet has a DbNameOverride
	if tablet.Tablet.DbNameOverride == "" {
		return vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "virtual tablet must have a DbNameOverride")
	}

	tk := targetKey{
		Keyspace: tablet.Keyspace,
		Shard:    tablet.Shard,
	}
	reg.targetTablets[tk] = tablet
	reg.dbNameTablets[tablet.DbNameOverride] = tablet
	return nil
}

func formatSafeSchema(keyspace, shard string) string {
	safeKeyspace := strings.ReplaceAll(keyspace, "-", "_")
	safeShard := strings.ReplaceAll(shard, "-", "_")
	return fmt.Sprintf("vt_%s_%s", safeKeyspace, safeShard)
}
