package registry

import (
	"context"
	"fmt"
	"strings"

	"vitess.io/vitess/go/vt/log"
	querypb "vitess.io/vitess/go/vt/proto/query"
	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/topo"
	"vitess.io/vitess/go/vt/vterrors"
)

type Registry struct {
	ts             *topo.Server
	r              topo.TabletResolver
	physicalTarget *querypb.Target
	// targetToDBOverride maps a target key (keyspace/shard) to a specific database name override.
	targetToDBOverride map[targetKey]string
	// shardToTablet maps a target key (keyspace/shard) to the virtual tablet that serves it.
	targetTablets map[targetKey]*topo.TabletInfo
}

type targetKey struct {
	Keyspace string
	Shard    string
}

func NewRegistry(topoServer *topo.Server) *Registry {
	return &Registry{
		ts: topoServer,
		r:  topo.NewDefaultTabletResolver(topoServer),
	}
}

func (reg *Registry) Init(ctx context.Context, target *querypb.Target) error {
	reg.physicalTarget = target
	if reg.ts == nil || reg.physicalTarget == nil {
		return vterrors.New(vtrpcpb.Code_INTERNAL, "registry not properly constructed: topo server or physical tablet alias is nil")
	}
	if err := reg.loadTabletsAndShards(ctx); err != nil {
		return vterrors.Wrapf(err, "failed to load tablets and shards for cell %q", reg.physicalTarget.Cell)
	}
	if len(reg.targetTablets) == 0 {
		log.Warningf("no tablets found for physical target %s/%s, this may lead to issues with resolving targets", reg.physicalTarget.Keyspace, reg.physicalTarget.Shard)
	}
	if len(reg.targetToDBOverride) == 0 {
		log.Warningf("no tablets found for physical target, this may lead to issues with resolving targets")
	}
	log.Infof("Registry initialized for cell %s with %d tablets", reg.physicalTarget.Cell, len(reg.targetTablets))
	return nil
}

func (reg *Registry) loadTabletsAndShards(ctx context.Context) error {
	tablets, err := reg.ts.GetVirtualTablets(ctx, reg.physicalTarget.Cell, reg.physicalTarget.Keyspace, reg.physicalTarget.Shard)
	if err != nil {
		return vterrors.Wrapf(err, "failed to get virtual tablets for cell %q, keyspace %q, shard %q", reg.physicalTarget.Cell, reg.physicalTarget.Keyspace, reg.physicalTarget.Shard)
	}

	reg.targetToDBOverride = make(map[targetKey]string)
	reg.targetTablets = make(map[targetKey]*topo.TabletInfo)

	for _, tablet := range tablets {
		if tablet.Alias == nil || tablet.Alias.Cell != reg.physicalTarget.Cell {
			continue // Skip tablets not in the same cell
		}
		if tablet.Keyspace != reg.physicalTarget.Keyspace || tablet.Shard != reg.physicalTarget.Shard {
			log.Warningf("Tablet %s/%s does not match physical target %s/%s, skipping", tablet.Keyspace, tablet.Shard, reg.physicalTarget.Keyspace, reg.physicalTarget.Shard)
			continue // Skip tablets not in the same keyspace/shard
		}
		if err := reg.storeTablet(ctx, tablet); err != nil {
			return vterrors.Wrapf(err, "failed to store tablet %s/%s", tablet.Keyspace, tablet.Shard)
		}
	}

	return nil
}

func (reg *Registry) storeTablet(ctx context.Context, tablet *topo.TabletInfo) error {
	if tablet == nil || tablet.Tablet == nil {
		return vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "tablet cannot be nil")
	}
	tk := targetKey{
		Keyspace: tablet.Keyspace,
		Shard:    tablet.Shard,
	}
	reg.targetToDBOverride[tk] = tablet.DbNameOverride
	reg.targetTablets[tk] = tablet
	return nil
}

func (reg *Registry) ResolveTarget(ctx context.Context, target *querypb.Target) (*querypb.Target, string, error) {
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

	// Prepare the target key for lookup
	tk := targetKey{
		Keyspace: target.Keyspace,
		Shard:    target.Shard,
	}
	var dbnameOverride string
	var ok bool
	// If this is already registered, we can return the physical target and the DB override.
	// Otherwise, lazy load it from the topology server.
	if dbnameOverride, ok = reg.targetToDBOverride[tk]; !ok {

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
				err := reg.storeTablet(ctx, &topo.TabletInfo{Tablet: tablet})
				if err != nil {
					log.Errorf("failed to store tablet %s/%s", tablet.Keyspace, tablet.Shard)
				}
				dbnameOverride = tablet.Tags[topo.SchemaNameTag]
				break // Found a matching tablet, no need to continue
			}
		}
	}

	if dbnameOverride != "" {
		return reg.physicalTarget, dbnameOverride, nil
	}

	// If we reach here, we couldn't find any mapping for the target to a specific database name override.
	// TODO: Consider whether we should return an error or a default value.
	log.Infof("No specific DB override found for target %s/%s, returning physical target with default database name", target.Keyspace, target.Shard)
	return reg.physicalTarget, reg.physicalTarget.DbName, nil
}

func formatSafeSchema(keyspace, shard string) string {
	safeKeyspace := strings.ReplaceAll(keyspace, "-", "_")
	safeShard := strings.ReplaceAll(shard, "-", "_")
	return fmt.Sprintf("vt_%s_%s", safeKeyspace, safeShard)
}
