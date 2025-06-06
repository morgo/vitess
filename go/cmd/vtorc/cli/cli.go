/*
Copyright 2023 The Vitess Authors.

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

package cli

import (
	"github.com/spf13/cobra"

	"vitess.io/vitess/go/acl"
	"vitess.io/vitess/go/viperutil/debug"
	vtconfig "vitess.io/vitess/go/vt/config"
	"vitess.io/vitess/go/vt/log"
	"vitess.io/vitess/go/vt/servenv"
	"vitess.io/vitess/go/vt/vtorc/config"
	"vitess.io/vitess/go/vt/vtorc/inst"
	"vitess.io/vitess/go/vt/vtorc/logic"
	"vitess.io/vitess/go/vt/vtorc/server"
)

var (
	Main = &cobra.Command{
		Use:   "vtorc",
		Short: "VTOrc is the automated fault detection and repair tool in Vitess.",
		Example: `vtorc \
	--topo-implementation etcd2 \
	--topo-global-server-address localhost:2379 \
	--topo-global-root /vitess/global \
	--log_dir $VTDATAROOT/tmp \
	--port 15000 \
	--instance-poll-time "1s" \
	--topo-information-refresh-duration "30s" \
	--alsologtostderr`,
		Args:    cobra.NoArgs,
		Version: servenv.AppVersion.String(),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := servenv.CobraPreRunE(cmd, args); err != nil {
				return err
			}

			// Initialize config from vitess.yaml
			if err := vtconfig.Init(); err != nil {
				return err
			}

			// If topo implementation is not set via command line, try to get it from config
			if cmd.Flags().Lookup("topo-implementation").Changed == false {
				if impl := vtconfig.GetString("global", "topo-implementation", ""); impl != "" {
					if err := cmd.Flags().Set("topo-implementation", impl); err != nil {
						return err
					}
				}
			}

			// If topo global-server-address is not set via command line, try to get it from config
			if cmd.Flags().Lookup("topo-global-server-address").Changed == false {
				if addr := vtconfig.GetString("global", "topo-global-server-address", ""); addr != "" {
					if err := cmd.Flags().Set("topo-global-server-address", addr); err != nil {
						return err
					}
				}
			}

			// If topo global-root is not set via command line, try to get it from config
			if cmd.Flags().Lookup("topo-global-root").Changed == false {
				if root := vtconfig.GetString("global", "topo-global-root", ""); root != "" {
					if err := cmd.Flags().Set("topo-global-root", root); err != nil {
						return err
					}
				}
			}

			return nil
		},
		Run: run,
	}
)

func run(cmd *cobra.Command, args []string) {
	servenv.Init()
	inst.RegisterStats()

	log.Info("starting vtorc")
	if config.GetAuditToSyslog() {
		inst.EnableAuditSyslog()
	}
	config.MarkConfigurationLoaded()

	// Log final config values to debug if something goes wrong.
	log.Infof("Running with Configuration - %v", debug.AllSettings())
	server.StartVTOrcDiscovery()

	server.RegisterVTOrcAPIEndpoints()
	servenv.OnRun(func() {
		addStatusParts()
	})

	// For backward compatability, we require that VTOrc functions even when the --port flag is not provided.
	// In this case, it should function like before but without the servenv pages.
	// Therefore, currently we don't check for the --port flag to be necessary, but release 16+ that check
	// can be added to always have the serenv page running in VTOrc.
	servenv.RunDefault()
}

// addStatusParts adds UI parts to the /debug/status page of VTOrc
func addStatusParts() {
	servenv.AddStatusPart("Recent Recoveries", logic.TopologyRecoveriesTemplate, func() any {
		recoveries, _ := logic.ReadRecentRecoveries(0)
		return recoveries
	})
}

func init() {
	servenv.RegisterDefaultFlags()
	servenv.RegisterFlags()

	// Register configuration flags
	vtconfig.RegisterFlags(Main.Flags())

	servenv.MoveFlagsToCobraCommand(Main)

	logic.RegisterFlags(Main.Flags())
	acl.RegisterFlags(Main.Flags())
}
