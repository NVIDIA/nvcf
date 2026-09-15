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
	"context"
	"errors"

	"github.com/hashicorp/go-plugin"
	"github.com/hashicorp/vault/sdk/database/dbplugin/v5/proto"
	"github.com/hashicorp/vault/sdk/helper/pluginutil"
	"github.com/hashicorp/vault/sdk/logical"
)

var _ logical.PluginVersioner = (*DatabasePluginClient)(nil)

type DatabasePluginClient struct {
	client pluginutil.PluginClient
	Database
}

func (dc *DatabasePluginClient) PluginVersion() logical.PluginVersion {
	if versioner, ok := dc.Database.(logical.PluginVersioner); ok {
		return versioner.PluginVersion()
	}
	return logical.EmptyPluginVersion
}

// This wraps the Close call and ensures we both close the database connection
// and kill the plugin.
func (dc *DatabasePluginClient) Close() error {
	err := dc.Database.Close()
	dc.client.Close()

	return err
}

// pluginSets is the map of plugins we can dispense.
var PluginSets = map[int]plugin.PluginSet{
	5: {
		"database": &GRPCDatabasePlugin{},
	},
	6: {
		"database": &GRPCDatabasePlugin{},
	},
}

// NewPluginClient returns a databaseRPCClient with a connection to a running
// plugin.
func NewPluginClient(ctx context.Context, sys pluginutil.RunnerUtil, config pluginutil.PluginClientConfig) (Database, error) {
	pluginClient, err := sys.NewPluginClient(ctx, config)
	if err != nil {
		return nil, err
	}

	// Request the plugin
	raw, err := pluginClient.Dispense("database")
	if err != nil {
		return nil, err
	}

	// We should have a database type now. This feels like a normal interface
	// implementation but is in fact over an RPC connection.
	var db Database
	switch c := raw.(type) {
	case gRPCClient:
		// This is an abstraction leak from go-plugin but it is necessary in
		// order to enable multiplexing on multiplexed plugins
		c.client = proto.NewDatabaseClient(pluginClient.Conn())
		c.versionClient = logical.NewPluginVersionClient(pluginClient.Conn())

		db = c
	default:
		return nil, errors.New("unsupported client type")
	}

	return &DatabasePluginClient{
		client:   pluginClient,
		Database: db,
	}, nil
}
