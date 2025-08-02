package registry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	querypb "vitess.io/vitess/go/vt/proto/query"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/topo"
	"vitess.io/vitess/go/vt/topo/memorytopo"
	"vitess.io/vitess/go/vt/vterrors"
)

func TestNewTopoRegistry(t *testing.T) {
	ts := memorytopo.NewServer(context.Background(), "cell1")
	defer ts.Close()

	reg := NewTopoRegistry(ts)
	assert.NotNil(t, reg)
	assert.Equal(t, ts, reg.ts)
	assert.NotNil(t, reg.r)
	assert.NotNil(t, reg.targetTablets)
	assert.NotNil(t, reg.dbNameTablets)
}

func TestTopoRegistry_Init(t *testing.T) {
	tests := []struct {
		name          string
		setupRegistry func() *TopoRegistry
		target        *querypb.Target
		wantErr       bool
		errCode       vtrpcpb.Code
		errMessage    string
	}{
		{
			name: "successful init",
			setupRegistry: func() *TopoRegistry {
				ts := memorytopo.NewServer(context.Background(), "cell1")
				return NewTopoRegistry(ts)
			},
			target: &querypb.Target{
				Cell:     "cell1",
				Keyspace: "ks1",
				Shard:    "0",
				DbName:   "vt_ks1_0",
			},
			wantErr: false,
		},
		{
			name: "nil target",
			setupRegistry: func() *TopoRegistry {
				ts := memorytopo.NewServer(context.Background(), "cell1")
				return NewTopoRegistry(ts)
			},
			target:     nil,
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "target cannot be nil",
		},
		{
			name: "nil topo server",
			setupRegistry: func() *TopoRegistry {
				return &TopoRegistry{
					ts:            nil,
					targetTablets: make(map[targetKey]*topo.TabletInfo),
					dbNameTablets: make(map[string]*topo.TabletInfo),
				}
			},
			target: &querypb.Target{
				Cell:     "cell1",
				Keyspace: "ks1",
				Shard:    "0",
			},
			wantErr:    true,
			errCode:    vtrpcpb.Code_INTERNAL,
			errMessage: "registry not properly constructed: topo server or physical tablet alias is nil",
		},
		{
			name: "nil target tablets map",
			setupRegistry: func() *TopoRegistry {
				ts := memorytopo.NewServer(context.Background(), "cell1")
				return &TopoRegistry{
					ts:            ts,
					r:             topo.NewDefaultTabletResolver(ts),
					targetTablets: nil,
					dbNameTablets: make(map[string]*topo.TabletInfo),
				}
			},
			target: &querypb.Target{
				Cell:     "cell1",
				Keyspace: "ks1",
				Shard:    "0",
			},
			wantErr:    true,
			errCode:    vtrpcpb.Code_INTERNAL,
			errMessage: "registry not properly constructed: topo server or physical tablet alias is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := tt.setupRegistry()
			err := reg.Init(context.Background(), tt.target)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.errCode, vterrors.Code(err))
				assert.Contains(t, err.Error(), tt.errMessage)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.target, reg.physicalTarget)
			}
		})
	}
}

func TestTopoRegistry_ResolveTarget(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Setup physical target
	physicalTarget := &querypb.Target{
		Cell:     "cell1",
		Keyspace: "physical_ks",
		Shard:    "0",
		DbName:   "vt_physical_ks_0",
	}

	reg := NewTopoRegistry(ts)
	err := reg.Init(ctx, physicalTarget)
	require.NoError(t, err)

	tests := []struct {
		name           string
		setupTablets   func()
		target         *querypb.Target
		expectedTarget *querypb.Target
		expectedDbName string
		wantErr        bool
		errCode        vtrpcpb.Code
		errMessage     string
	}{
		{
			name:         "empty keyspace",
			setupTablets: func() {},
			target: &querypb.Target{
				Keyspace: "",
				Shard:    "0",
			},
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "target keyspace cannot be empty",
		},
		{
			name:         "empty shard",
			setupTablets: func() {},
			target: &querypb.Target{
				Keyspace: "ks1",
				Shard:    "",
			},
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "target shard cannot be empty",
		},
		{
			name:         "physical target match",
			setupTablets: func() {},
			target: &querypb.Target{
				Keyspace: "physical_ks",
				Shard:    "0",
			},
			expectedTarget: physicalTarget,
			expectedDbName: physicalTarget.DbName,
			wantErr:        false,
		},
		{
			name: "virtual target with registered tablet",
			setupTablets: func() {
				virtualTablet := &topo.TabletInfo{
					Tablet: &topodatapb.Tablet{
						Alias: &topodatapb.TabletAlias{
							Cell: "cell1",
							Uid:  100,
						},
						Keyspace: "virtual_ks",
						Shard:    "0",
						Tags: map[string]string{
							topo.PhysicalKeyspaceTag: "physical_ks",
							topo.PhysicalShardTag:    "0",
							topo.SchemaNameTag:       "vt_virtual_ks_0",
						},
					},
				}
				err := reg.AddTablet(virtualTablet)
				require.NoError(t, err)
			},
			target: &querypb.Target{
				Keyspace: "virtual_ks",
				Shard:    "0",
			},
			expectedTarget: physicalTarget,
			expectedDbName: "vt_virtual_ks_0",
			wantErr:        false,
		},
		{
			name: "virtual target not found - fallback to naming convention",
			setupTablets: func() {
				// Create the keyspace and shard but with no tablets
				err := ts.CreateKeyspace(ctx, "unknown_ks", &topodatapb.Keyspace{})
				require.NoError(t, err)
				err = ts.CreateShard(ctx, "unknown_ks", "0")
				require.NoError(t, err)
			},
			target: &querypb.Target{
				Keyspace: "unknown_ks",
				Shard:    "0",
			},
			expectedTarget: &querypb.Target{
				Keyspace: "unknown_ks",
				Shard:    "0",
			},
			expectedDbName: "vt_unknown_ks_0",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset registry state
			reg.mu.Lock()
			reg.targetTablets = make(map[targetKey]*topo.TabletInfo)
			reg.dbNameTablets = make(map[string]*topo.TabletInfo)
			reg.mu.Unlock()

			tt.setupTablets()

			target, dbName, err := reg.ResolveTarget(ctx, tt.target)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.errCode, vterrors.Code(err))
				assert.Contains(t, err.Error(), tt.errMessage)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedTarget, target)
				assert.Equal(t, tt.expectedDbName, dbName)
			}
		})
	}
}

func TestTopoRegistry_ResolveDbName(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	reg := NewTopoRegistry(ts)
	err := reg.Init(ctx, &querypb.Target{
		Cell:     "cell1",
		Keyspace: "ks1",
		Shard:    "0",
	})
	require.NoError(t, err)

	// Add a test tablet
	testTablet := &topo.TabletInfo{
		Tablet: &topodatapb.Tablet{
			Alias: &topodatapb.TabletAlias{
				Cell: "cell1",
				Uid:  100,
			},
			Keyspace: "virtual_ks",
			Shard:    "0",
			Tags: map[string]string{
				topo.SchemaNameTag: "test_db",
			},
		},
	}
	err = reg.AddTablet(testTablet)
	require.NoError(t, err)

	tests := []struct {
		name           string
		dbName         string
		expectedTablet *topo.TabletInfo
		wantErr        bool
		errCode        vtrpcpb.Code
		errMessage     string
	}{
		{
			name:           "successful resolve",
			dbName:         "test_db",
			expectedTablet: testTablet,
			wantErr:        false,
		},
		{
			name:       "empty dbName",
			dbName:     "",
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "dbName cannot be empty",
		},
		{
			name:       "dbName not found",
			dbName:     "nonexistent_db",
			wantErr:    true,
			errCode:    vtrpcpb.Code_NOT_FOUND,
			errMessage: "no tablet found for dbName nonexistent_db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tablet, err := reg.ResolveDbName(tt.dbName)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.errCode, vterrors.Code(err))
				assert.Contains(t, err.Error(), tt.errMessage)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedTablet, tablet)
			}
		})
	}
}

func TestTopoRegistry_AddTablet(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	reg := NewTopoRegistry(ts)
	err := reg.Init(ctx, &querypb.Target{
		Cell:     "cell1",
		Keyspace: "ks1",
		Shard:    "0",
	})
	require.NoError(t, err)

	tests := []struct {
		name       string
		tablet     *topo.TabletInfo
		wantErr    bool
		errCode    vtrpcpb.Code
		errMessage string
	}{
		{
			name: "successful add",
			tablet: &topo.TabletInfo{
				Tablet: &topodatapb.Tablet{
					Alias: &topodatapb.TabletAlias{
						Cell: "cell1",
						Uid:  100,
					},
					Keyspace: "virtual_ks",
					Shard:    "0",
					Tags: map[string]string{
						topo.SchemaNameTag: "test_db",
					},
				},
			},
			wantErr: false,
		},
		{
			name:       "nil tablet",
			tablet:     nil,
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "tablet cannot be nil",
		},
		{
			name: "tablet with nil Tablet field",
			tablet: &topo.TabletInfo{
				Tablet: nil,
			},
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "tablet cannot be nil",
		},
		{
			name: "tablet without schema name tag",
			tablet: &topo.TabletInfo{
				Tablet: &topodatapb.Tablet{
					Alias: &topodatapb.TabletAlias{
						Cell: "cell1",
						Uid:  101,
					},
					Keyspace: "virtual_ks",
					Shard:    "0",
					Tags:     map[string]string{},
				},
			},
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "virtual tablet must have a schema name tag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reg.AddTablet(tt.tablet)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.errCode, vterrors.Code(err))
				assert.Contains(t, err.Error(), tt.errMessage)
			} else {
				require.NoError(t, err)

				// Verify the tablet was added to both maps
				tk := targetKey{
					Keyspace: tt.tablet.Keyspace,
					Shard:    tt.tablet.Shard,
				}
				reg.mu.Lock()
				assert.Equal(t, tt.tablet, reg.targetTablets[tk])
				assert.Equal(t, tt.tablet, reg.dbNameTablets[tt.tablet.Tags[topo.SchemaNameTag]])
				reg.mu.Unlock()
			}
		})
	}
}

func TestTopoRegistry_RemoveTablet(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	reg := NewTopoRegistry(ts)
	err := reg.Init(ctx, &querypb.Target{
		Cell:     "cell1",
		Keyspace: "ks1",
		Shard:    "0",
	})
	require.NoError(t, err)

	// Add a test tablet first
	testTablet := &topo.TabletInfo{
		Tablet: &topodatapb.Tablet{
			Alias: &topodatapb.TabletAlias{
				Cell: "cell1",
				Uid:  100,
			},
			Keyspace: "virtual_ks",
			Shard:    "0",
			Tags: map[string]string{
				topo.SchemaNameTag: "test_db",
			},
		},
	}
	err = reg.AddTablet(testTablet)
	require.NoError(t, err)

	tests := []struct {
		name       string
		keyspace   string
		shard      string
		wantErr    bool
		errCode    vtrpcpb.Code
		errMessage string
	}{
		{
			name:     "successful remove",
			keyspace: "virtual_ks",
			shard:    "0",
			wantErr:  false,
		},
		{
			name:       "empty keyspace",
			keyspace:   "",
			shard:      "0",
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "keyspace and shard cannot be empty",
		},
		{
			name:       "empty shard",
			keyspace:   "virtual_ks",
			shard:      "",
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "keyspace and shard cannot be empty",
		},
		{
			name:       "tablet not found",
			keyspace:   "nonexistent_ks",
			shard:      "0",
			wantErr:    true,
			errCode:    vtrpcpb.Code_NOT_FOUND,
			errMessage: "tablet for keyspace nonexistent_ks and shard 0 not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// For the successful case, we need to re-add the tablet since it gets removed
			if tt.name == "successful remove" {
				err := reg.AddTablet(testTablet)
				require.NoError(t, err)
			}

			err := reg.RemoveTablet(tt.keyspace, tt.shard)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.errCode, vterrors.Code(err))
				assert.Contains(t, err.Error(), tt.errMessage)
			} else {
				require.NoError(t, err)

				// Verify the tablet was removed
				tk := targetKey{
					Keyspace: tt.keyspace,
					Shard:    tt.shard,
				}
				reg.mu.Lock()
				_, exists := reg.targetTablets[tk]
				assert.False(t, exists)
				reg.mu.Unlock()
			}
		})
	}
}

func TestTopoRegistry_storeTablet(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	reg := NewTopoRegistry(ts)
	err := reg.Init(ctx, &querypb.Target{
		Cell:     "cell1",
		Keyspace: "ks1",
		Shard:    "0",
	})
	require.NoError(t, err)

	tests := []struct {
		name       string
		tablet     *topo.TabletInfo
		wantErr    bool
		errCode    vtrpcpb.Code
		errMessage string
	}{
		{
			name: "successful store",
			tablet: &topo.TabletInfo{
				Tablet: &topodatapb.Tablet{
					Keyspace: "virtual_ks",
					Shard:    "0",
					Tags: map[string]string{
						topo.SchemaNameTag: "test_db",
					},
				},
			},
			wantErr: false,
		},
		{
			name:       "nil tablet",
			tablet:     nil,
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "tablet cannot be nil",
		},
		{
			name: "tablet with nil Tablet field",
			tablet: &topo.TabletInfo{
				Tablet: nil,
			},
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "tablet cannot be nil",
		},
		{
			name: "tablet without schema name tag",
			tablet: &topo.TabletInfo{
				Tablet: &topodatapb.Tablet{
					Keyspace: "virtual_ks",
					Shard:    "0",
					Tags:     map[string]string{},
				},
			},
			wantErr:    true,
			errCode:    vtrpcpb.Code_INVALID_ARGUMENT,
			errMessage: "virtual tablet must have a schema name tag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reg.storeTablet(tt.tablet)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.errCode, vterrors.Code(err))
				assert.Contains(t, err.Error(), tt.errMessage)
			} else {
				require.NoError(t, err)

				// Verify the tablet was stored in both maps
				tk := targetKey{
					Keyspace: tt.tablet.Keyspace,
					Shard:    tt.tablet.Shard,
				}
				reg.mu.Lock()
				assert.Equal(t, tt.tablet, reg.targetTablets[tk])
				assert.Equal(t, tt.tablet, reg.dbNameTablets[tt.tablet.Tags[topo.SchemaNameTag]])
				reg.mu.Unlock()
			}
		})
	}
}

func TestFormatSafeSchema(t *testing.T) {
	tests := []struct {
		name     string
		keyspace string
		shard    string
		expected string
	}{
		{
			name:     "simple keyspace and shard",
			keyspace: "ks1",
			shard:    "0",
			expected: "vt_ks1_0",
		},
		{
			name:     "keyspace with dashes",
			keyspace: "my-keyspace",
			shard:    "0",
			expected: "vt_my_keyspace_0",
		},
		{
			name:     "shard with dashes",
			keyspace: "ks1",
			shard:    "80-c0",
			expected: "vt_ks1_80_c0",
		},
		{
			name:     "both with dashes",
			keyspace: "my-keyspace",
			shard:    "80-c0",
			expected: "vt_my_keyspace_80_c0",
		},
		{
			name:     "complex shard range",
			keyspace: "commerce",
			shard:    "-80",
			expected: "vt_commerce__80",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatSafeSchema(tt.keyspace, tt.shard)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTopoRegistry_IntegrationTest(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Create a registry
	reg := NewTopoRegistry(ts)
	physicalTarget := &querypb.Target{
		Cell:     "cell1",
		Keyspace: "physical_ks",
		Shard:    "0",
		DbName:   "vt_physical_ks_0",
	}

	// Initialize the registry
	err := reg.Init(ctx, physicalTarget)
	require.NoError(t, err)

	// Add some virtual tablets
	virtualTablet1 := &topo.TabletInfo{
		Tablet: &topodatapb.Tablet{
			Alias: &topodatapb.TabletAlias{
				Cell: "cell1",
				Uid:  100,
			},
			Keyspace: "virtual_ks1",
			Shard:    "0",
			Tags: map[string]string{
				topo.PhysicalKeyspaceTag: "physical_ks",
				topo.PhysicalShardTag:    "0",
				topo.SchemaNameTag:       "vt_virtual_ks1_0",
			},
		},
	}

	virtualTablet2 := &topo.TabletInfo{
		Tablet: &topodatapb.Tablet{
			Alias: &topodatapb.TabletAlias{
				Cell: "cell1",
				Uid:  101,
			},
			Keyspace: "virtual_ks2",
			Shard:    "0",
			Tags: map[string]string{
				topo.PhysicalKeyspaceTag: "physical_ks",
				topo.PhysicalShardTag:    "0",
				topo.SchemaNameTag:       "vt_virtual_ks2_0",
			},
		},
	}

	// Add tablets to registry
	err = reg.AddTablet(virtualTablet1)
	require.NoError(t, err)

	err = reg.AddTablet(virtualTablet2)
	require.NoError(t, err)

	// Test resolving virtual targets
	target1, dbName1, err := reg.ResolveTarget(ctx, &querypb.Target{
		Keyspace: "virtual_ks1",
		Shard:    "0",
	})
	require.NoError(t, err)
	assert.Equal(t, physicalTarget, target1)
	assert.Equal(t, "vt_virtual_ks1_0", dbName1)

	target2, dbName2, err := reg.ResolveTarget(ctx, &querypb.Target{
		Keyspace: "virtual_ks2",
		Shard:    "0",
	})
	require.NoError(t, err)
	assert.Equal(t, physicalTarget, target2)
	assert.Equal(t, "vt_virtual_ks2_0", dbName2)

	// Test resolving by database name
	tablet1, err := reg.ResolveDbName("vt_virtual_ks1_0")
	require.NoError(t, err)
	assert.Equal(t, virtualTablet1, tablet1)

	tablet2, err := reg.ResolveDbName("vt_virtual_ks2_0")
	require.NoError(t, err)
	assert.Equal(t, virtualTablet2, tablet2)

	// Test removing a tablet
	err = reg.RemoveTablet("virtual_ks1", "0")
	require.NoError(t, err)

	// Verify the tablet is no longer resolvable by database name
	_, err = reg.ResolveDbName("vt_virtual_ks1_0")
	require.Error(t, err)
	assert.Equal(t, vtrpcpb.Code_NOT_FOUND, vterrors.Code(err))

	// But the other tablet should still be resolvable
	tablet2Again, err := reg.ResolveDbName("vt_virtual_ks2_0")
	require.NoError(t, err)
	assert.Equal(t, virtualTablet2, tablet2Again)
}

func TestTopoRegistry_RemoveTablet_Bug(t *testing.T) {
	// This test verifies that RemoveTablet correctly removes tablets from both maps
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	reg := NewTopoRegistry(ts)
	err := reg.Init(ctx, &querypb.Target{
		Cell:     "cell1",
		Keyspace: "ks1",
		Shard:    "0",
	})
	require.NoError(t, err)

	// Add a test tablet
	testTablet := &topo.TabletInfo{
		Tablet: &topodatapb.Tablet{
			Alias: &topodatapb.TabletAlias{
				Cell: "cell1",
				Uid:  100,
			},
			Keyspace: "virtual_ks",
			Shard:    "0",
			Tags: map[string]string{
				topo.SchemaNameTag: "test_db",
			},
		},
	}
	err = reg.AddTablet(testTablet)
	require.NoError(t, err)

	// Verify tablet is in both maps
	tk := targetKey{
		Keyspace: "virtual_ks",
		Shard:    "0",
	}
	reg.mu.Lock()
	_, existsInTarget := reg.targetTablets[tk]
	_, existsInDbName := reg.dbNameTablets["test_db"]
	reg.mu.Unlock()
	assert.True(t, existsInTarget)
	assert.True(t, existsInDbName)

	// Remove the tablet
	err = reg.RemoveTablet("virtual_ks", "0")
	require.NoError(t, err)

	// FIXED: The tablet should be removed from both maps
	reg.mu.Lock()
	_, existsInTarget = reg.targetTablets[tk]
	_, existsInDbName = reg.dbNameTablets["test_db"]
	reg.mu.Unlock()
	assert.False(t, existsInTarget, "tablet should be removed from targetTablets")
	assert.False(t, existsInDbName, "tablet should be removed from dbNameTablets")

	// ResolveDbName should now fail
	_, err = reg.ResolveDbName("test_db")
	require.Error(t, err)
	assert.Equal(t, vtrpcpb.Code_NOT_FOUND, vterrors.Code(err))
}

func TestTopoRegistry_RemoveTablet_EdgeCases(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	reg := NewTopoRegistry(ts)
	err := reg.Init(ctx, &querypb.Target{
		Cell:     "cell1",
		Keyspace: "ks1",
		Shard:    "0",
	})
	require.NoError(t, err)

	t.Run("remove tablet with nil tags", func(t *testing.T) {
		// This tests the edge case where a tablet somehow has nil tags
		// (shouldn't happen in normal operation, but we should handle it gracefully)
		testTablet := &topo.TabletInfo{
			Tablet: &topodatapb.Tablet{
				Keyspace: "virtual_ks",
				Shard:    "0",
				Tags:     nil, // nil tags
			},
		}

		// Manually add to targetTablets to simulate this edge case
		tk := targetKey{Keyspace: "virtual_ks", Shard: "0"}
		reg.mu.Lock()
		reg.targetTablets[tk] = testTablet
		reg.mu.Unlock()

		// RemoveTablet should not panic even with nil tags
		err := reg.RemoveTablet("virtual_ks", "0")
		require.NoError(t, err)

		// Verify it's removed
		reg.mu.Lock()
		_, exists := reg.targetTablets[tk]
		reg.mu.Unlock()
		assert.False(t, exists)
	})

	t.Run("remove tablet with empty schema name tag", func(t *testing.T) {
		// This tests the edge case where a tablet has tags but empty schema name
		testTablet := &topo.TabletInfo{
			Tablet: &topodatapb.Tablet{
				Keyspace: "virtual_ks2",
				Shard:    "0",
				Tags: map[string]string{
					topo.SchemaNameTag: "", // empty schema name
					"other_tag":        "value",
				},
			},
		}

		// Manually add to targetTablets to simulate this edge case
		tk := targetKey{Keyspace: "virtual_ks2", Shard: "0"}
		reg.mu.Lock()
		reg.targetTablets[tk] = testTablet
		reg.mu.Unlock()

		// RemoveTablet should not panic even with empty schema name
		err := reg.RemoveTablet("virtual_ks2", "0")
		require.NoError(t, err)

		// Verify it's removed
		reg.mu.Lock()
		_, exists := reg.targetTablets[tk]
		reg.mu.Unlock()
		assert.False(t, exists)
	})
}

func TestTopoRegistry_EdgeCases(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	reg := NewTopoRegistry(ts)
	physicalTarget := &querypb.Target{
		Cell:     "cell1",
		Keyspace: "physical_ks",
		Shard:    "0",
		DbName:   "vt_physical_ks_0",
	}
	err := reg.Init(ctx, physicalTarget)
	require.NoError(t, err)

	t.Run("resolve target with empty dbName in physical target", func(t *testing.T) {
		// Test case where physical target has empty DbName
		reg.physicalTarget.DbName = ""

		target, dbName, err := reg.ResolveTarget(ctx, &querypb.Target{
			Keyspace: "physical_ks",
			Shard:    "0",
		})
		require.NoError(t, err)
		assert.Equal(t, reg.physicalTarget, target)
		assert.Equal(t, "", dbName)

		// Reset
		reg.physicalTarget.DbName = "vt_physical_ks_0"
	})

	t.Run("add tablet with empty tags map", func(t *testing.T) {
		tablet := &topo.TabletInfo{
			Tablet: &topodatapb.Tablet{
				Keyspace: "virtual_ks",
				Shard:    "0",
				Tags:     nil, // nil tags map
			},
		}
		err := reg.AddTablet(tablet)
		require.Error(t, err)
		assert.Equal(t, vtrpcpb.Code_INVALID_ARGUMENT, vterrors.Code(err))
		assert.Contains(t, err.Error(), "virtual tablet must have a schema name tag")
	})

	t.Run("concurrent access safety", func(t *testing.T) {
		// This test ensures the mutex protects concurrent access
		tablet := &topo.TabletInfo{
			Tablet: &topodatapb.Tablet{
				Keyspace: "concurrent_ks",
				Shard:    "0",
				Tags: map[string]string{
					topo.SchemaNameTag: "concurrent_db",
				},
			},
		}

		// Run multiple goroutines concurrently
		const numGoroutines = 10
		done := make(chan bool, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer func() { done <- true }()

				// Add tablet
				err := reg.AddTablet(tablet)
				// This might succeed or fail depending on timing, but shouldn't panic
				_ = err

				// Try to resolve
				_, _, err = reg.ResolveTarget(ctx, &querypb.Target{
					Keyspace: "concurrent_ks",
					Shard:    "0",
				})
				// This might succeed or fail depending on timing, but shouldn't panic
				_ = err

				// Try to remove
				err = reg.RemoveTablet("concurrent_ks", "0")
				// This might succeed or fail depending on timing, but shouldn't panic
				_ = err
			}()
		}

		// Wait for all goroutines to complete
		for i := 0; i < numGoroutines; i++ {
			<-done
		}
	})
}
