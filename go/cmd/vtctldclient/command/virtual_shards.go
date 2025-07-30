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
	// CreateVirtualShard makes a CreateVirtualShard gRPC call to a vtctld.
	CreateVirtualShard = &cobra.Command{
		Use:   "CreateVirtualShard <virtual_keyspace> <virtual_shard> <physical_keyspace> <physical_shard>",
		Short: "Creates a virtual shard that maps to a physical shard.",
		Long: `Creates a virtual shard that maps to a physical shard.

Virtual shards allow multiple logical shards to share the same physical 
tablet infrastructure, improving resource utilization and reducing operational overhead.

The virtual shard will use the specified physical shard's tablets and 
create a separate MySQL schema for isolation.

If --schema-name is not specified, it will be auto-generated as "vt_<virtual_keyspace>_<virtual_shard>".`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(4),
		RunE:                  commandCreateVirtualShard,
	}
)

var virtualShardSchemaNameFlag string

func commandCreateVirtualShard(cmd *cobra.Command, args []string) error {
	cli.FinishedParsing(cmd)

	virtualKeyspace := cmd.Flags().Arg(0)
	virtualShard := cmd.Flags().Arg(1)
	physicalKeyspace := cmd.Flags().Arg(2)
	physicalShard := cmd.Flags().Arg(3)

	req := &vtctldatapb.CreateVirtualShardRequest{
		VirtualKeyspace:  virtualKeyspace,
		VirtualShard:     virtualShard,
		PhysicalKeyspace: physicalKeyspace,
		PhysicalShard:    physicalShard,
		SchemaName:       virtualShardSchemaNameFlag,
	}

	resp, err := client.CreateVirtualShard(commandCtx, req)
	if err != nil {
		return err
	}

	data, err := cli.MarshalJSON(resp.Shard)
	if err != nil {
		return err
	}

	fmt.Printf("Successfully created virtual shard %s/%s -> %s/%s. Result:\n%s\n", virtualKeyspace, virtualShard, physicalKeyspace, physicalShard, data)

	return nil
}

func init() {
	CreateVirtualShard.Flags().StringVar(&virtualShardSchemaNameFlag, "schema-name", "", "MySQL schema name to use for this virtual shard. If empty, defaults to 'vt_<virtual_keyspace>_<virtual_shard>'")
	Root.AddCommand(CreateVirtualShard)
}
