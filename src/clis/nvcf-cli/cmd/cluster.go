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

package cmd

import (
	"context"
	"fmt"

	"nvcf-cli/internal/client"

	"github.com/spf13/cobra"
)

// clusterCmd represents the cluster command group
var clusterCmd = &cobra.Command{
	Use:          "cluster",
	Short:        "Manage cluster resources",
	SilenceUsage: true,
	Long: `Manage and query cluster resources and GPU availability.

Available subcommands:
- list: List available cluster groups`,
}

var clusterListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List cluster groups",
	SilenceUsage: true,
	Long:         `List available cluster groups and their GPU resources.`,
	RunE:         runClusterList,
}

func init() {
	// Cluster commands
	rootCmd.AddCommand(clusterCmd)
	clusterCmd.AddCommand(clusterListCmd)
	initClusterRegistrationCmds()
}

func runClusterList(cmd *cobra.Command, args []string) error {
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	fmt.Println("Listing cluster groups...")
	result, err := c.ListClusterGroups(ctx)
	if err != nil {
		return fmt.Errorf("failed to list cluster groups: %w", err)
	}

	if len(result.ClusterGroups) == 0 {
		fmt.Println("No cluster groups found.")
		return nil
	}

	fmt.Printf("Found %d cluster groups:\n\n", len(result.ClusterGroups))
	for _, group := range result.ClusterGroups {
		fmt.Printf("ID: %s\n", group.ID)
		if group.Name != "" {
			fmt.Printf("Name: %s\n", group.Name)
		}
		if group.NCAID != "" {
			fmt.Printf("NCA ID: %s\n", group.NCAID)
		}
		if len(group.GPUs) > 0 {
			fmt.Printf("Available GPUs: ")
			for i, gpu := range group.GPUs {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Print(gpu.Name)
			}
			fmt.Println()
		}
		if len(group.Clusters) > 0 {
			fmt.Printf("Clusters: %d\n", len(group.Clusters))
		}
		fmt.Println("---")
	}

	return nil
}
