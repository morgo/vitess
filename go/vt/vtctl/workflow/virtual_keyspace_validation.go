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

package workflow

import (
	"context"

	vtrpcpb "vitess.io/vitess/go/vt/proto/vtrpc"
	"vitess.io/vitess/go/vt/topo"
	"vitess.io/vitess/go/vt/vterrors"
)

// ValidateVirtualKeyspaceConstraints validates that virtual keyspace constraints are met
func (s *Server) ValidateVirtualKeyspaceConstraints(ctx context.Context, virtualKeyspace, physicalKeyspace, schemaName string) error {
	// Validate that the physical keyspace exists
	_, err := s.ts.GetKeyspace(ctx, physicalKeyspace)
	if err != nil {
		return vterrors.Wrapf(err, "physical keyspace %s does not exist", physicalKeyspace)
	}

	// Validate that the physical keyspace is unsharded by checking its VSchema
	vschema, err := s.ts.GetVSchema(ctx, physicalKeyspace)
	if err != nil && !topo.IsErrType(err, topo.NoNode) {
		return vterrors.Wrapf(err, "failed to get vschema for physical keyspace %s", physicalKeyspace)
	}

	// If vschema exists and is sharded, reject the virtual keyspace creation
	if vschema != nil && vschema.Keyspace != nil && vschema.Keyspace.Sharded {
		return vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT,
			"virtual keyspaces can only be created on unsharded physical keyspaces, but %s is sharded", physicalKeyspace)
	}

	// Validate that the virtual keyspace doesn't already exist
	if _, err := s.ts.GetKeyspace(ctx, virtualKeyspace); err == nil {
		return vterrors.Errorf(vtrpcpb.Code_ALREADY_EXISTS, "keyspace %s already exists", virtualKeyspace)
	} else if !topo.IsErrType(err, topo.NoNode) {
		return vterrors.Wrapf(err, "failed to check if virtual keyspace %s exists", virtualKeyspace)
	}

	// Validate schema name doesn't conflict with existing schemas
	if err := s.validateSchemaNameConflict(ctx, physicalKeyspace, schemaName); err != nil {
		return err
	}

	return nil
}

// validateSchemaNameConflict checks if the schema name conflicts with existing schemas
func (s *Server) validateSchemaNameConflict(ctx context.Context, physicalKeyspace, schemaName string) error {
	// Get all existing virtual keyspaces that use the same physical keyspace
	allKeyspaces, err := s.ts.GetKeyspaces(ctx)
	if err != nil {
		return vterrors.Wrapf(err, "failed to get list of keyspaces")
	}

	for _, ksName := range allKeyspaces {
		ki, err := s.ts.GetKeyspace(ctx, ksName)
		if err != nil {
			continue // Skip keyspaces we can't read
		}

		if ki.IsVirtual && ki.VirtualKeyspaceInfo != nil {
			if ki.VirtualKeyspaceInfo.PhysicalKeyspace == physicalKeyspace &&
				ki.VirtualKeyspaceInfo.SchemaName == schemaName {
				return vterrors.Errorf(vtrpcpb.Code_ALREADY_EXISTS,
					"schema name %s is already used by virtual keyspace %s on physical keyspace %s",
					schemaName, ksName, physicalKeyspace)
			}
		}
	}

	return nil
}

// ValidateVirtualKeyspaceWorkflow validates that a workflow can be run with virtual keyspaces
func (s *Server) ValidateVirtualKeyspaceWorkflow(ctx context.Context, sourceKeyspace, targetKeyspace string) error {
	sourceKi, err := s.ts.GetKeyspace(ctx, sourceKeyspace)
	if err != nil {
		return vterrors.Wrapf(err, "failed to get source keyspace info")
	}

	targetKi, err := s.ts.GetKeyspace(ctx, targetKeyspace)
	if err != nil {
		return vterrors.Wrapf(err, "failed to get target keyspace info")
	}

	// Virtual to virtual migrations are not supported
	if sourceKi.IsVirtual && targetKi.IsVirtual {
		return vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT,
			"migrations between virtual keyspaces are not supported")
	}

	// If target is virtual, ensure it's on an unsharded physical keyspace
	if targetKi.IsVirtual && targetKi.VirtualKeyspaceInfo != nil {
		// Check the physical keyspace's VSchema to determine if it's sharded
		physicalVSchema, err := s.ts.GetVSchema(ctx, targetKi.VirtualKeyspaceInfo.PhysicalKeyspace)
		if err != nil && !topo.IsErrType(err, topo.NoNode) {
			return vterrors.Wrapf(err, "failed to get vschema for physical keyspace %s", targetKi.VirtualKeyspaceInfo.PhysicalKeyspace)
		}

		// If vschema exists and is sharded, reject the workflow
		if physicalVSchema != nil && physicalVSchema.Keyspace != nil && physicalVSchema.Keyspace.Sharded {
			return vterrors.Errorf(vtrpcpb.Code_INVALID_ARGUMENT,
				"virtual keyspace target %s is on sharded physical keyspace %s, which is not supported",
				targetKeyspace, targetKi.VirtualKeyspaceInfo.PhysicalKeyspace)
		}
	}

	return nil
}

// GetVirtualKeyspaceMetrics returns metrics about virtual keyspace usage
func (s *Server) GetVirtualKeyspaceMetrics(ctx context.Context) (map[string]interface{}, error) {
	metrics := make(map[string]interface{})

	allKeyspaces, err := s.ts.GetKeyspaces(ctx)
	if err != nil {
		return nil, vterrors.Wrapf(err, "failed to get keyspaces")
	}

	var virtualCount, physicalCount int
	physicalToVirtual := make(map[string][]string)

	for _, ksName := range allKeyspaces {
		ki, err := s.ts.GetKeyspace(ctx, ksName)
		if err != nil {
			continue
		}

		if ki.IsVirtual && ki.VirtualKeyspaceInfo != nil {
			virtualCount++
			physicalKs := ki.VirtualKeyspaceInfo.PhysicalKeyspace
			physicalToVirtual[physicalKs] = append(physicalToVirtual[physicalKs], ksName)
		} else {
			physicalCount++
		}
	}

	metrics["virtual_keyspaces"] = virtualCount
	metrics["physical_keyspaces"] = physicalCount
	metrics["physical_to_virtual_mapping"] = physicalToVirtual

	return metrics, nil
}
