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

package cli

import (
	"encoding/json"
	"fmt"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/version"
	"github.com/spf13/cobra"
)

// makeVersionCmd creates the version command
func makeVersionCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Long:  "Display version, build, and runtime information for the application",
		RunE: func(cmd *cobra.Command, args []string) error {
			info := version.Get()

			if outputJSON {
				output, err := json.MarshalIndent(info, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal version info: %w", err)
				}
				fmt.Println(string(output))
			} else {
				fmt.Println(info.String())
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output version information as JSON")

	return cmd
}
