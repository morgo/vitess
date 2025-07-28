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

package command

import (
	"fmt"

	"github.com/spf13/cobra"

	"vitess.io/vitess/go/cmd/vtctldclient/cli"
	vtctldatapb "vitess.io/vitess/go/vt/proto/vtctldata"
)

var (
	// CreateVirtualKeyspace makes a CreateVirtualKeyspace gRPC call to a vtctld.
	CreateVirtualKeyspace = &cobra.Command{
		Use:   "CreateVirtualKeyspace <name> <physical_keyspace>",
		Short: "Creates a virtual keyspace that maps to a physical keyspace.",
		Long: `Creates a virtual keyspace that maps to a physical keyspace.

Virtual keyspaces allow multiple logical keyspaces to share the same physical 
tablet infrastructure, improving resource utilization and reducing operational overhead.

The virtual keyspace will use the specified physical keyspace's tablets and 
create a separate MySQL schema for isolation.

If --schema-name is not specified, it will be auto-generated as "vt_<name>_0".
The "_0" suffix is required because unlike physical keyspaces, there might be 
several shards on the same host, and this encodes the shard ID.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(2),
		RunE:                  commandCreateVirtualKeyspace,
	}

	// DeleteVirtualKeyspace makes a DeleteVirtualKeyspace gRPC call to a vtctld.
	DeleteVirtualKeyspace = &cobra.Command{
		Use:   "DeleteVirtualKeyspace <name>",
		Short: "Deletes the specified virtual keyspace from the topology.",
		Long: `Deletes the specified virtual keyspace from the topology.

This removes the virtual keyspace mapping but does not affect the underlying 
physical keyspace or its tablets. The MySQL schema associated with the virtual 
keyspace should be manually dropped if no longer needed.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		RunE:                  commandDeleteVirtualKeyspace,
	}

	// GetVirtualKeyspace makes a GetVirtualKeyspace gRPC call to a vtctld.
	GetVirtualKeyspace = &cobra.Command{
		Use:   "GetVirtualKeyspace <n>",
		Short: "Gets the specified virtual keyspace from the topology.",
		Long: `Gets the specified virtual keyspace from the topology.

This returns the virtual keyspace configuration including its mapping to the 
physical keyspace and schema name.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		RunE:                  commandGetVirtualKeyspace,
	}

	// ListVirtualKeyspaces makes a ListVirtualKeyspaces gRPC call to a vtctld.
	ListVirtualKeyspaces = &cobra.Command{
		Use:   "ListVirtualKeyspaces",
		Short: "Lists all virtual keyspaces in the topology.",
		Long: `Lists all virtual keyspaces in the topology.

This returns a list of all virtual keyspaces and their configurations.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE:                  commandListVirtualKeyspaces,
	}
)

var schemaNameFlag string

func commandCreateVirtualKeyspace(cmd *cobra.Command, args []string) error {
	cli.FinishedParsing(cmd)

	name := cmd.Flags().Arg(0)
	physicalKeyspace := cmd.Flags().Arg(1)

	req := &vtctldatapb.CreateVirtualKeyspaceRequest{
		Name:             name,
		PhysicalKeyspace: physicalKeyspace,
		SchemaName:       schemaNameFlag,
	}

	resp, err := client.CreateVirtualKeyspace(commandCtx, req)
	if err != nil {
		return err
	}

	data, err := cli.MarshalJSON(resp.VirtualKeyspace)
	if err != nil {
		return err
	}

	fmt.Printf("Successfully created virtual keyspace %s. Result:\n%s\n", name, data)

	return nil
}

func commandDeleteVirtualKeyspace(cmd *cobra.Command, args []string) error {
	cli.FinishedParsing(cmd)

	name := cmd.Flags().Arg(0)
	_, err := client.DeleteVirtualKeyspace(commandCtx, &vtctldatapb.DeleteVirtualKeyspaceRequest{
		Name: name,
	})

	if err != nil {
		return fmt.Errorf("DeleteVirtualKeyspace(%v) error: %w; please check the topo", name, err)
	}

	fmt.Printf("Successfully deleted virtual keyspace %v.\n", name)

	return nil
}

func commandGetVirtualKeyspace(cmd *cobra.Command, args []string) error {
	cli.FinishedParsing(cmd)

	name := cmd.Flags().Arg(0)
	resp, err := client.GetVirtualKeyspace(commandCtx, &vtctldatapb.GetVirtualKeyspaceRequest{
		Name: name,
	})

	if err != nil {
		return fmt.Errorf("GetVirtualKeyspace(%v) error: %w", name, err)
	}

	data, err := cli.MarshalJSON(resp.VirtualKeyspace)
	if err != nil {
		return err
	}

	fmt.Printf("%s\n", data)

	return nil
}

func commandListVirtualKeyspaces(cmd *cobra.Command, args []string) error {
	cli.FinishedParsing(cmd)

	resp, err := client.ListVirtualKeyspaces(commandCtx, &vtctldatapb.ListVirtualKeyspacesRequest{})

	if err != nil {
		return fmt.Errorf("ListVirtualKeyspaces() error: %w", err)
	}

	data, err := cli.MarshalJSON(resp.VirtualKeyspaces)
	if err != nil {
		return err
	}

	fmt.Printf("%s\n", data)

	return nil
}

func init() {
	CreateVirtualKeyspace.Flags().StringVar(&schemaNameFlag, "schema-name", "", "MySQL schema name to use for this virtual keyspace. If empty, defaults to 'vt_<name>_0'")
	Root.AddCommand(CreateVirtualKeyspace)
	Root.AddCommand(DeleteVirtualKeyspace)
	Root.AddCommand(GetVirtualKeyspace)
	Root.AddCommand(ListVirtualKeyspaces)
}
