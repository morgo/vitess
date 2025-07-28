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
	"path"
	"time"

	"vitess.io/vitess/go/event"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	vttime "vitess.io/vitess/go/vt/proto/vttime"
	"vitess.io/vitess/go/vt/topo/events"
	"vitess.io/vitess/go/vt/vterrors"
)

// This file contains virtual keyspace utility functions.

const (
	// VirtualKeyspacesPath is the path to virtual keyspaces in the topology
	VirtualKeyspacesPath = "virtual_keyspaces"
	// VirtualKeyspaceFile is the filename for virtual keyspace data
	VirtualKeyspaceFile = "VirtualKeyspace"
	// VirtualTabletRegistrationsPath is the path to virtual tablet registrations
	VirtualTabletRegistrationsPath = "virtual_tablet_registrations"
	// VirtualTabletRegistrationFile is the filename for virtual tablet registration data
	VirtualTabletRegistrationFile = "VirtualTabletRegistration"
)

// VirtualKeyspaceInfo is a meta struct that contains metadata to give the
// virtual keyspace data more context and convenience.
type VirtualKeyspaceInfo struct {
	name    string
	version Version
	*topodatapb.VirtualKeyspace
}

// VirtualKeyspaceName returns the virtual keyspace name
func (vki *VirtualKeyspaceInfo) VirtualKeyspaceName() string {
	return vki.name
}

// SetVirtualKeyspaceName sets the virtual keyspace name
func (vki *VirtualKeyspaceInfo) SetVirtualKeyspaceName(name string) {
	vki.name = name
}

// VirtualTabletRegistrationInfo is a meta struct that contains metadata for
// virtual tablet registrations.
type VirtualTabletRegistrationInfo struct {
	alias   string
	version Version
	*topodatapb.VirtualTabletRegistration
}

// CreateVirtualKeyspace creates a new virtual keyspace in the topology.
func (ts *Server) CreateVirtualKeyspace(ctx context.Context, name, physicalKeyspace, schemaName string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if err := ValidateKeyspaceName(name); err != nil {
		return vterrors.Wrapf(err, "CreateVirtualKeyspace: invalid virtual keyspace name %s", name)
	}

	if err := ValidateKeyspaceName(physicalKeyspace); err != nil {
		return vterrors.Wrapf(err, "CreateVirtualKeyspace: invalid physical keyspace name %s", physicalKeyspace)
	}

	// Verify that the physical keyspace exists
	if _, err := ts.GetKeyspace(ctx, physicalKeyspace); err != nil {
		return vterrors.Wrapf(err, "CreateVirtualKeyspace: physical keyspace %s does not exist", physicalKeyspace)
	}

	// Check if a virtual keyspace with this name already exists in the old location
	virtualKeyspacePath := path.Join(VirtualKeyspacesPath, name, VirtualKeyspaceFile)
	if _, _, err := ts.globalCell.Get(ctx, virtualKeyspacePath); err == nil {
		return vterrors.Errorf(vtrpcpb.Code_ALREADY_EXISTS, "virtual keyspace %s already exists in legacy location", name)
	}

	// Create a Keyspace object with virtual keyspace information
	keyspace := &topodatapb.Keyspace{
		KeyspaceType: topodatapb.KeyspaceType_NORMAL,
		IsVirtual:    true,
		VirtualKeyspaceInfo: &topodatapb.VirtualKeyspaceInfo{
			PhysicalKeyspace: physicalKeyspace,
			SchemaName:       schemaName,
			CreatedAt: &vttime.Time{
				Seconds: time.Now().Unix(),
			},
		},
	}

	data, err := keyspace.MarshalVT()
	if err != nil {
		return err
	}

	// Store in the regular keyspaces path, same as physical keyspaces
	keyspacePath := path.Join(KeyspacesPath, name, KeyspaceFile)
	if _, err := ts.globalCell.Create(ctx, keyspacePath, data); err != nil {
		return err
	}

	// Dispatch a keyspace created event
	// TODO: we need to trace this and figure out
	// why it is not triggering a rebuild.
	event.Dispatch(&events.KeyspaceChange{
		KeyspaceName: name,
		Keyspace:     keyspace,
		Status:       "created",
	})
	return nil
}

// GetVirtualKeyspace reads the given virtual keyspace and returns it.
func (ts *Server) GetVirtualKeyspace(ctx context.Context, name string) (*VirtualKeyspaceInfo, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if err := ValidateKeyspaceName(name); err != nil {
		return nil, vterrors.Wrapf(err, "GetVirtualKeyspace: invalid virtual keyspace name %s", name)
	}

	// First try to get the keyspace from the unified location
	ki, err := ts.GetKeyspace(ctx, name)
	if err == nil {
		// Check if it's actually a virtual keyspace
		if !ki.IsVirtual {
			return nil, vterrors.Errorf(vtrpcpb.Code_NOT_FOUND, "keyspace %s is not a virtual keyspace", name)
		}

		// Convert to VirtualKeyspace format for backward compatibility
		vk := &topodatapb.VirtualKeyspace{
			Name:             name,
			PhysicalKeyspace: ki.VirtualKeyspaceInfo.PhysicalKeyspace,
			SchemaName:       ki.VirtualKeyspaceInfo.SchemaName,
			CreatedAt:        ki.VirtualKeyspaceInfo.CreatedAt,
		}

		return &VirtualKeyspaceInfo{
			name:            name,
			version:         ki.version,
			VirtualKeyspace: vk,
		}, nil
	}

	// If not found in unified location, check the old virtual keyspaces path for backward compatibility
	if IsErrType(err, NoNode) {
		virtualKeyspacePath := path.Join(VirtualKeyspacesPath, name, VirtualKeyspaceFile)
		data, version, oldErr := ts.globalCell.Get(ctx, virtualKeyspacePath)
		if oldErr == nil {
			vk := &topodatapb.VirtualKeyspace{}
			if err = vk.UnmarshalVT(data); err != nil {
				return nil, vterrors.Wrap(err, "bad virtual keyspace data")
			}

			return &VirtualKeyspaceInfo{
				name:            name,
				version:         version,
				VirtualKeyspace: vk,
			}, nil
		}
	}

	return nil, err
}

// ListVirtualKeyspaces returns the list of virtual keyspaces in the topology.
func (ts *Server) ListVirtualKeyspaces(ctx context.Context) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Get all keyspaces and filter for virtual ones
	allKeyspaces, err := ts.GetKeyspaces(ctx)
	if err != nil {
		return nil, err
	}

	var virtualKeyspaces []string
	for _, keyspaceName := range allKeyspaces {
		ki, err := ts.GetKeyspace(ctx, keyspaceName)
		if err != nil {
			// Skip keyspaces we can't read
			continue
		}
		if ki.IsVirtual {
			virtualKeyspaces = append(virtualKeyspaces, keyspaceName)
		}
	}

	return virtualKeyspaces, nil
}

// DeleteVirtualKeyspace deletes a virtual keyspace from the topology.
func (ts *Server) DeleteVirtualKeyspace(ctx context.Context, name string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Verify it's a virtual keyspace before deleting
	ki, err := ts.GetKeyspace(ctx, name)
	if err != nil {
		return err
	}

	if !ki.IsVirtual {
		return vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT, "keyspace %s is not a virtual keyspace", name)
	}

	// Delete from the unified keyspaces location
	keyspacePath := path.Join(KeyspacesPath, name, KeyspaceFile)
	if err := ts.globalCell.Delete(ctx, keyspacePath, nil); err != nil {
		return err
	}

	event.Dispatch(&events.KeyspaceChange{
		KeyspaceName: name,
		Keyspace:     ki.Keyspace,
		Status:       "deleted",
	})
	return nil
}
