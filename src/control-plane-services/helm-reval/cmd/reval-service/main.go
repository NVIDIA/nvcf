// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"

	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/cmd/reval/cli"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/authorizers"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry/logging"
)

var (
	Version   = "dev"
	GitCommit = "dev"
)

func main() {
	logger, undoReplace := logging.SetupBootstrapLogger(Version)
	defer func() { _ = logger.Sync() }()
	defer undoReplace()
	defer logging.PanicHandler(logger)()

	rootCmd := cli.NewRootCommand(logger, Version, GitCommit, cli.Options{
		AuthorizerFactory: defaultAuthorizerFactory,
	})
	if err := rootCmd.Execute(); err != nil {
		logger.Fatal("Failed to execute root command", zap.Error(err))
	}
}

// defaultAuthorizerFactory wires the self-hosted JWKS JWT and/or OIDC introspection authorizers.
func defaultAuthorizerFactory(
	ctx context.Context,
	_ *viper.Viper,
	cfg *config.RevalConfig,
	logger *zap.Logger,
) ([]authorizers.Authorizer, error) {
	return authorizers.BuildChain(ctx, &cfg.Auth, logger)
}
