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
	"fmt"

	"github.com/hashicorp/errwrap"
	log "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault/sdk/helper/consts"
	"github.com/hashicorp/vault/sdk/helper/pluginutil"
)

// PluginFactory is used to build plugin database types. It wraps the database
// object in a logging and metrics middleware.
func PluginFactory(ctx context.Context, pluginName string, sys pluginutil.LookRunnerUtil, logger log.Logger) (Database, error) {
	return PluginFactoryVersion(ctx, pluginName, "", sys, logger)
}

// PluginFactoryVersion is used to build plugin database types with a version specified.
// It wraps the database object in a logging and metrics middleware.
func PluginFactoryVersion(ctx context.Context, pluginName string, pluginVersion string, sys pluginutil.LookRunnerUtil, logger log.Logger) (Database, error) {
	// Look for plugin in the plugin catalog
	pluginRunner, err := sys.LookupPluginVersion(ctx, pluginName, consts.PluginTypeDatabase, pluginVersion)
	if err != nil {
		return nil, err
	}

	namedLogger := logger.Named(pluginName)

	var transport string
	var db Database
	if pluginRunner.Builtin {
		// Plugin is builtin so we can retrieve an instance of the interface
		// from the pluginRunner. Then cast it to a Database.
		dbRaw, err := pluginRunner.BuiltinFactory()
		if err != nil {
			return nil, errwrap.Wrapf("error initializing plugin: {{err}}", err)
		}

		var ok bool
		db, ok = dbRaw.(Database)
		if !ok {
			return nil, fmt.Errorf("unsupported database type: %q", pluginName)
		}

		transport = "builtin"

	} else {
		config := pluginutil.PluginClientConfig{
			Name:            pluginName,
			PluginType:      consts.PluginTypeDatabase,
			Version:         pluginVersion,
			PluginSets:      PluginSets,
			HandshakeConfig: HandshakeConfig,
			Logger:          namedLogger,
			IsMetadataMode:  false,
			AutoMTLS:        true,
			Wrapper:         sys,
		}
		// create a DatabasePluginClient instance
		db, err = NewPluginClient(ctx, sys, config)
		if err != nil {
			return nil, err
		}

		// Switch on the underlying database client type to get the transport
		// method.
		switch db.(*DatabasePluginClient).Database.(type) {
		case *gRPCClient:
			transport = "gRPC"
		}

	}

	typeStr, err := db.Type()
	if err != nil {
		return nil, errwrap.Wrapf("error getting plugin type: {{err}}", err)
	}
	logger.Debug("got database plugin instance", "type", typeStr)

	// Wrap with metrics middleware
	db = &databaseMetricsMiddleware{
		next:    db,
		typeStr: typeStr,
	}

	// Wrap with tracing middleware
	if namedLogger.IsTrace() {
		db = &databaseTracingMiddleware{
			next:   db,
			logger: namedLogger.With("transport", transport),
		}
	}

	return db, nil
}
