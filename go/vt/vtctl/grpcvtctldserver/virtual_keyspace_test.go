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

package grpcvtctldserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vitess.io/vitess/go/vt/topo/memorytopo"
	"vitess.io/vitess/go/vt/vtenv"

	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	vtctldatapb "vitess.io/vitess/go/vt/proto/vtctldata"
)

func TestVirtualKeyspaceOperations(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	vtctld := NewVtctldServer(vtenv.NewTestEnv(), ts)

	// First create a physical keyspace
	err := ts.CreateKeyspace(ctx, "physical_ks", &topodatapb.Keyspace{})
	require.NoError(t, err)

	// Test CreateVirtualKeyspace
	t.Run("CreateVirtualKeyspace", func(t *testing.T) {
		req := &vtctldatapb.CreateVirtualKeyspaceRequest{
			Name:             "virtual_ks",
			PhysicalKeyspace: "physical_ks",
			SchemaName:       "vt_virtual_ks",
		}
		resp, err := vtctld.CreateVirtualKeyspace(ctx, req)
		require.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Equal(t, "virtual_ks", resp.VirtualKeyspace.Name)
		assert.Equal(t, "physical_ks", resp.VirtualKeyspace.PhysicalKeyspace)
		assert.Equal(t, "vt_virtual_ks", resp.VirtualKeyspace.SchemaName)
	})

	// Test GetVirtualKeyspace
	t.Run("GetVirtualKeyspace", func(t *testing.T) {
		req := &vtctldatapb.GetVirtualKeyspaceRequest{
			Name: "virtual_ks",
		}
		resp, err := vtctld.GetVirtualKeyspace(ctx, req)
		require.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Equal(t, "virtual_ks", resp.VirtualKeyspace.Name)
		assert.Equal(t, "physical_ks", resp.VirtualKeyspace.PhysicalKeyspace)
	})

	// Test ListVirtualKeyspaces
	t.Run("ListVirtualKeyspaces", func(t *testing.T) {
		req := &vtctldatapb.ListVirtualKeyspacesRequest{}
		resp, err := vtctld.ListVirtualKeyspaces(ctx, req)
		require.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Len(t, resp.VirtualKeyspaces, 1)
		assert.Equal(t, "virtual_ks", resp.VirtualKeyspaces[0].Name)
	})

	// Test DeleteVirtualKeyspace
	t.Run("DeleteVirtualKeyspace", func(t *testing.T) {
		req := &vtctldatapb.DeleteVirtualKeyspaceRequest{
			Name: "virtual_ks",
		}
		resp, err := vtctld.DeleteVirtualKeyspace(ctx, req)
		require.NoError(t, err)
		assert.NotNil(t, resp)

		// Verify it's deleted
		getReq := &vtctldatapb.GetVirtualKeyspaceRequest{
			Name: "virtual_ks",
		}
		_, err = vtctld.GetVirtualKeyspace(ctx, getReq)
		assert.Error(t, err)
	})
}
