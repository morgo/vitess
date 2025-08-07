package registry

import (
	"context"
	"fmt"
	"sync"

	"vitess.io/vitess/go/vt/log"
	querypb "vitess.io/vitess/go/vt/proto/query"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/topo"
	"vitess.io/vitess/go/vt/vterrors"
	"vitess.io/vitess/go/vt/vtgate/vindexes"
)

type Registry interface {
	// Init initializes the registry with the given target.
	Init(ctx context.Context, target *querypb.Target) error
	// Refresh refreshes the registry for the stored physical tablet
	Refresh(ctx context.Context) error
	// ResolveTarget resolves the given target to a physical target and an optional database name override.
	ResolveTarget(ctx context.Context, target *querypb.Target) (*querypb.Target, string, error)
	// ResolveDbName resolves a database name to a tablet.
	ResolveDbName(dbName string) (*topo.TabletInfo, error)
	// GetKeyspaceShardByDbName returns the keyspace and shard for a given database name.
	GetKeyspaceShardByDbName(dbName string) (string, string, error)
	// GetDBNameByKeyspaceShard returns the database name for a given keyspace and shard.
	GetDBNameByKeyspaceShard(keyspace, shard string) (string, error)
	// GetVSchemaByKeyspace returns the vschema for a given keyspace.
	GetVSchemaByKeyspace(keyspace string) (*vindexes.VSchema, error)
	// SetVSchema updates the vschema for a keyspace.
	SetVSchema(keyspace string, vschema *vindexes.VSchema)
	// GetAllKeyspaces returns all keyspaces known to the registry.
	GetAllKeyspaces() []string
	// GetAllDBNames returns all database names known to the registry.
	GetAllDBNames() []string
	// GetPhysicalKeyspaceShard returns the physical keyspace and shard for this registry instance
	GetPhysicalKeyspaceShard() (string, string)
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
	// vschemaMap maps keyspace to vschema for vstreamer support
	vschemaMap map[string]*vindexes.VSchema
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
		vschemaMap:    make(map[string]*vindexes.VSchema),
	}
}

var notConstructed = vterrors.New(vtrpcpb.Code_INTERNAL, "registry not properly constructed!")

func (reg *TopoRegistry) isConstructed() bool {
	if reg.ts == nil || reg.r == nil || reg.targetTablets == nil || reg.dbNameTablets == nil || reg.physicalTarget == nil {
		return false
	}
	return true
}

func (reg *TopoRegistry) Init(ctx context.Context, target *querypb.Target) error {
	if target == nil {
		return vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "target cannot be nil")
	}
	if target.DbName == "" {
		log.Infof("DBName not specified for target %s/%s, %#v", target.Keyspace, target.Shard, target)
		// Need to come up with a DBName otherwise everything will break.
		// This is temporary. No shard ID here, it's a physical shard.
		target.DbName = "vt_" + target.Keyspace
	}
	reg.physicalTarget = target

	if !reg.isConstructed() {
		return notConstructed
	}

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

func (reg *TopoRegistry) Refresh(ctx context.Context) error {
	if !reg.isConstructed() {
		return notConstructed
	}

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

func (reg *TopoRegistry) GetPhysicalKeyspaceShard() (string, string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.physicalTarget.Keyspace, reg.physicalTarget.Shard
}

// ResolveTarget returns the dbName if it's specified in req.DBNameOverride and it's a permissible DB - if not permissible = error.
// If there is no virtual shards present, return the DB name for the physical shard + no error.
// If there are virtual shards present and DBNameOverride is empty = error.
func (reg *TopoRegistry) ResolveTarget(ctx context.Context, target *querypb.Target) (*querypb.Target, string, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	log.Infof("Resolving target %s/%s", target.Keyspace, target.Shard)
	if target.Keyspace == "" {
		return nil, "", vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "target keyspace cannot be empty")
	}

	if target.Shard == "" {
		return nil, "", vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "target shard cannot be empty")
	}

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
			return nil, "", vterrors.Wrapf(err, "failed to resolve tablets for keyspace %s and shard %s", target.Keyspace, target.Shard)
		}
		if len(tablets) == 0 {
			return nil, "", fmt.Errorf("no tablets found for virtual keyspace %s/%s", target.Keyspace, target.Shard)
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
	log.Errorf("")
	return nil, "", vterrors.New(vtrpcpb.Code_NOT_FOUND, fmt.Sprintf("no specific DB override found for target %s/%s", target.Keyspace, target.Shard))
}

func (reg *TopoRegistry) ResolveDbName(dbName string) (*topo.TabletInfo, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	if dbName == "" {
		return nil, vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "dbName cannot be empty")
	}

	tablet, exists := reg.dbNameTablets[dbName]
	if !exists {
		return nil, vterrors.New(vtrpcpb.Code_NOT_FOUND, fmt.Sprintf("no tablet found for dbName %s", dbName))
	}

	log.Infof("Resolved dbName %s to tablet %s/%s", dbName, tablet.Keyspace, tablet.Shard)
	return tablet, nil
}

func (reg *TopoRegistry) GetKeyspaceShardByDbName(dbName string) (string, string, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	if dbName == "" {
		// Fallback to physical keyspace for empty dbName
		return reg.physicalTarget.Keyspace, reg.physicalTarget.Shard, nil
	}

	tablet, exists := reg.dbNameTablets[dbName]
	if !exists {
		// Fallback to physical keyspace if not found
		log.Infof("No tablet found for dbName %s, falling back to physical keyspace %s", dbName, reg.physicalTarget.Keyspace)
		return reg.physicalTarget.Keyspace, reg.physicalTarget.Shard, nil
	}

	log.Infof("Resolved dbName %s to keyspace %s", dbName, tablet.Keyspace)
	return tablet.Keyspace, tablet.Shard, nil
}

func (reg *TopoRegistry) GetDBNameByKeyspaceShard(keyspace, shard string) (string, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	for _, tablet := range reg.targetTablets {
		if tablet.Keyspace == keyspace && tablet.Shard == shard {
			return tablet.DbNameOverride, nil
		}
	}

	return "", vterrors.New(vtrpcpb.Code_NOT_FOUND, fmt.Sprintf("no tablet found for keyspace %s and shard %s", keyspace, shard))
}

func (reg *TopoRegistry) GetVSchemaByKeyspace(keyspace string) (*vindexes.VSchema, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	if vschema, exists := reg.vschemaMap[keyspace]; exists {
		return vschema, nil
	}

	// Return nil if not found - caller should handle creating empty vschema
	return nil, nil
}

func (reg *TopoRegistry) SetVSchema(keyspace string, vschema *vindexes.VSchema) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	reg.vschemaMap[keyspace] = vschema
}

func (reg *TopoRegistry) GetAllKeyspaces() []string {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	// Create a set to avoid duplicates
	keyspaceSet := make(map[string]bool)

	// Add the physical keyspace
	keyspaceSet[reg.physicalTarget.Keyspace] = true

	// Add all virtual keyspaces from the targetTablets
	for tk := range reg.targetTablets {
		keyspaceSet[tk.Keyspace] = true
	}

	// Add any keyspaces from the vschemaMap that might not be in targetTablets
	for keyspace := range reg.vschemaMap {
		keyspaceSet[keyspace] = true
	}

	// Convert set to slice
	keyspaces := make([]string, 0, len(keyspaceSet))
	for keyspace := range keyspaceSet {
		keyspaces = append(keyspaces, keyspace)
	}

	log.Infof("GetAllKeyspaces returning %d keyspaces: %v", len(keyspaces), keyspaces)
	return keyspaces
}

func (reg *TopoRegistry) GetAllDBNames() []string {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	// Create a set to avoid duplicates
	dbNameSet := make(map[string]bool)

	// Always add the physical target's DbName if it exists
	if reg.physicalTarget.DbName != "" {
		dbNameSet[reg.physicalTarget.DbName] = true
	}

	// Add all tablets' DB names
	for _, tablet := range reg.targetTablets {
		dbNameSet[tablet.DbNameOverride] = true
	}

	// Convert set to slice
	dbNames := make([]string, 0, len(dbNameSet))
	for dbName := range dbNameSet {
		dbNames = append(dbNames, dbName)
	}

	log.Infof("GetAllDBNames returning %d DB names: %v", len(dbNames), dbNames)
	return dbNames
}

// loadTabletsAndShards loads the tablets and shards from the topology server.
// It clears the existing maps to prevent stale data, and is always called
// under a mutex.
func (reg *TopoRegistry) loadTabletsAndShards(ctx context.Context) error {
	// Clear existing maps to prevent stale data
	reg.targetTablets = make(map[targetKey]*topo.TabletInfo)
	reg.dbNameTablets = make(map[string]*topo.TabletInfo)

	// First, add the physical tablet information
	physicalTablet := &topo.TabletInfo{
		Tablet: &topodatapb.Tablet{
			Keyspace:       reg.physicalTarget.Keyspace,
			Shard:          reg.physicalTarget.Shard,
			DbNameOverride: reg.physicalTarget.DbName,
		},
	}
	tk := targetKey{
		Keyspace: reg.physicalTarget.Keyspace,
		Shard:    reg.physicalTarget.Shard,
	}
	reg.targetTablets[tk] = physicalTablet
	reg.dbNameTablets[reg.physicalTarget.DbName] = physicalTablet

	// Then load virtual tablets
	tablets, err := reg.ts.GetVirtualTablets(ctx, reg.physicalTarget.Cell, reg.physicalTarget.Keyspace, reg.physicalTarget.Shard)
	if err != nil {
		return vterrors.Wrapf(err, "failed to get virtual tablets for cell %q, keyspace %q, shard %q", reg.physicalTarget.Cell, reg.physicalTarget.Keyspace, reg.physicalTarget.Shard)
	}

	for _, tablet := range tablets {
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

	// Virtual tablets must have a DbNameOverride, but physical tablets might not
	if tablet.Tablet.DbNameOverride == "" {
		// Check if this is the physical tablet (matching keyspace/shard)
		if tablet.Keyspace == reg.physicalTarget.Keyspace && tablet.Shard == reg.physicalTarget.Shard {
			// This is the physical tablet, it's okay to not have a DbNameOverride
			// We'll use the physical target's DbName instead
			tablet.Tablet.DbNameOverride = reg.physicalTarget.DbName
		} else {
			// This is a virtual tablet without DbNameOverride, which is an error
			return vterrors.New(vtrpcpb.Code_INVALID_ARGUMENT, "virtual tablet must have a DbNameOverride")
		}
	}

	tk := targetKey{
		Keyspace: tablet.Keyspace,
		Shard:    tablet.Shard,
	}
	reg.targetTablets[tk] = tablet

	// Only add to dbNameTablets if there's a valid DbNameOverride
	if tablet.Tablet.DbNameOverride != "" {
		reg.dbNameTablets[tablet.DbNameOverride] = tablet
	}

	return nil
}
