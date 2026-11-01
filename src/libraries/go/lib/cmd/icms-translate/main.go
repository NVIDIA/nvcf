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

package main

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/cmd"
)

func main() {
	cmd := cmd.NewTranslateCommand()
	app := &cli.App{
		Name:   cmd.Name,
		Usage:  cmd.Usage,
		Flags:  cmd.Flags,
		Action: cmd.Action,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(app.ErrWriter, err)
		os.Exit(1)
	}
}
