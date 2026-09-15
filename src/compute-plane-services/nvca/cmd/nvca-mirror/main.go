/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	cli "github.com/urfave/cli/v2"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/mirror"
)

func main() {
	// Create context with signal handling
	baseCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ctx := core.NewDefaultContext(baseCtx)
	log := core.GetLogger(ctx)

	app := &cli.App{
		Name:    "nvca-mirror",
		Usage:   "NVCA Mirror Sidecar",
		Version: version.String(),
		Commands: []*cli.Command{
			mirror.NewRunCommand(),
		},
		// Default action if no command is specified
		Action: func(c *cli.Context) error {
			// Run the default "run" command
			return mirror.NewRunCommand().Run(c)
		},
	}

	if err := app.RunContext(ctx, os.Args); err != nil {
		log.Fatal(err)
	}
}
