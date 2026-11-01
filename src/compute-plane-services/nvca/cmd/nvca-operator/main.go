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

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	cli "github.com/urfave/cli/v2"

	operator "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/reconcile"
)

func main() {
	cmd := operator.NewOperatorCommand()
	app := &cli.App{
		Name:    cmd.Name,
		Usage:   cmd.Usage,
		Version: version.String(),
		Flags:   cmd.Flags,
		Action:  cmd.Action,
	}

	ctx := core.NewDefaultContext(context.Background())
	log := core.GetLogger(ctx)
	if err := app.RunContext(ctx, os.Args); err != nil {
		log.Fatal(err)
	}
}
