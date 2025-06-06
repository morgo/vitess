/*
Copyright 2020 The Vitess Authors.

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

package main

import (
	"flag"

	"vitess.io/vitess/go/acl"
	"vitess.io/vitess/go/cmd/vtctldclient/command"
	"vitess.io/vitess/go/exit"
	"vitess.io/vitess/go/vt/config"
	"vitess.io/vitess/go/vt/grpcclient"
	"vitess.io/vitess/go/vt/grpccommon"
	"vitess.io/vitess/go/vt/log"
	"vitess.io/vitess/go/vt/logutil"
	"vitess.io/vitess/go/vt/servenv"
	"vitess.io/vitess/go/vt/vtctl/grpcclientcommon"
	"vitess.io/vitess/go/vt/vtctl/vtctlclient"
	"vitess.io/vitess/go/vt/vttablet/grpctmclient"
	"vitess.io/vitess/go/vt/vttablet/tmclient"

	_flag "vitess.io/vitess/go/internal/flag"
)

func main() {
	defer exit.Recover()

	// Register configuration flags
	config.RegisterFlags(command.Root.PersistentFlags())

	// Grab all those global flags across the codebase and shove 'em on in.
	// (TODO|andrew) remove this line after the migration to pflag is complete.
	command.Root.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	log.RegisterFlags(command.Root.PersistentFlags())
	logutil.RegisterFlags(command.Root.PersistentFlags())
	grpcclient.RegisterFlags(command.Root.PersistentFlags())
	grpccommon.RegisterFlags(command.Root.PersistentFlags())
	grpcclientcommon.RegisterFlags(command.Root.PersistentFlags())
	grpctmclient.RegisterFlags(command.Root.PersistentFlags())
	tmclient.RegisterFlags(command.Root.PersistentFlags())
	servenv.RegisterMySQLServerFlags(command.Root.PersistentFlags())
	vtctlclient.RegisterFlags(command.Root.PersistentFlags())
	acl.RegisterFlags(command.Root.PersistentFlags())

	// hack to get rid of an "ERROR: logging before flag.Parse"
	_flag.TrickGlog()

	// Initialize configuration from vitess.yaml
	// We don't want to fail if the config file can't be read, just log a warning
	if err := config.Init(); err != nil {
		log.Warningf("Failed to initialize configuration: %v", err)
	}

	// If topo implementation is not set via command line, try to get it from config
	if command.Root.PersistentFlags().Lookup("topo-implementation").Changed == false {
		if impl := config.GetString("global", "topo-implementation", ""); impl != "" {
			if err := command.Root.PersistentFlags().Set("topo-implementation", impl); err != nil {
				log.Warningf("Failed to set topo-implementation from config: %v", err)
			} else {
				log.Infof("Using topo implementation from config: %s", impl)
			}
		}
	}

	// If topo global-server-address is not set via command line, try to get it from config
	if command.Root.PersistentFlags().Lookup("topo-global-server-address").Changed == false {
		if addr := config.GetString("global", "topo-global-server-address", ""); addr != "" {
			if err := command.Root.PersistentFlags().Set("topo-global-server-address", addr); err != nil {
				log.Warningf("Failed to set topo-global-server-address from config: %v", err)
			} else {
				log.Infof("Using topo global-server-address from config: %s", addr)
			}
		}
	}

	// If topo global-root is not set via command line, try to get it from config
	if command.Root.PersistentFlags().Lookup("topo-global-root").Changed == false {
		if root := config.GetString("global", "topo-global-root", ""); root != "" {
			if err := command.Root.PersistentFlags().Set("topo-global-root", root); err != nil {
				log.Warningf("Failed to set topo-global-root from config: %v", err)
			} else {
				log.Infof("Using topo global-root from config: %s", root)
			}
		}
	}
	
	// If server is not set via command line, try to get it from config
	if command.Root.PersistentFlags().Lookup("server").Changed == false {
		if server := config.GetString("vtctldclient", "server", ""); server != "" {
			if err := command.Root.PersistentFlags().Set("server", server); err != nil {
				log.Warningf("Failed to set server from config: %v", err)
			} else {
				log.Infof("Using vtctld server from config: %s", server)
			}
		}
	}

	// back to your regularly scheduled cobra programming
	if err := command.Root.Execute(); err != nil {
		log.Error(err)
		exit.Return(1)
	}
}
