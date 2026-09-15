/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

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

package dbplugin

import (
	"fmt"

	"github.com/hashicorp/go-plugin"
	"github.com/hashicorp/vault/sdk/helper/pluginutil"
)

// Serve is called from within a plugin and wraps the provided
// Database implementation in a databasePluginRPCServer object and starts a
// RPC server.
func Serve(db Database) {
	plugin.Serve(ServeConfig(db))
}

func ServeConfig(db Database) *plugin.ServeConfig {
	err := pluginutil.OptionallyEnableMlock()
	if err != nil {
		fmt.Println(err)
		return nil
	}

	// pluginSets is the map of plugins we can dispense.
	pluginSets := map[int]plugin.PluginSet{
		5: {
			"database": &GRPCDatabasePlugin{
				Impl: db,
			},
		},
	}

	conf := &plugin.ServeConfig{
		HandshakeConfig:  HandshakeConfig,
		VersionedPlugins: pluginSets,
		GRPCServer:       plugin.DefaultGRPCServer,
	}

	return conf
}

func ServeMultiplex(factory Factory) {
	plugin.Serve(ServeConfigMultiplex(factory))
}

func ServeConfigMultiplex(factory Factory) *plugin.ServeConfig {
	err := pluginutil.OptionallyEnableMlock()
	if err != nil {
		fmt.Println(err)
		return nil
	}

	db, err := factory()
	if err != nil {
		fmt.Println(err)
		return nil
	}

	database := db.(Database)

	// pluginSets is the map of plugins we can dispense.
	pluginSets := map[int]plugin.PluginSet{
		5: {
			"database": &GRPCDatabasePlugin{
				Impl: database,
			},
		},
		6: {
			"database": &GRPCDatabasePlugin{
				FactoryFunc: factory,
			},
		},
	}

	conf := &plugin.ServeConfig{
		HandshakeConfig:  HandshakeConfig,
		VersionedPlugins: pluginSets,
		GRPCServer:       plugin.DefaultGRPCServer,
	}

	return conf
}
