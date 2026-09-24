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
	"encoding/json"
	"fmt"

	"nvcf-cli/internal/client"

	"github.com/spf13/cobra"
)

var clusterListRegisteredCmd = &cobra.Command{
	Use:          "list-registered",
	Short:        "List self-hosted cluster registrations from ICMS",
	SilenceUsage: true,
	RunE:         runClusterListRegistered,
}

func runClusterListRegistered(cmd *cobra.Command, _ []string) error {
	ncaID, _ := cmd.Flags().GetString(clusterFlagNcaID)

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf(errFailedToLoadConfig, err)
	}
	if err := requireAdminToken(config); err != nil {
		return err
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf(errFailedToCreateClient, err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	clusters, err := c.ListClusters(ctx, getICMSURL(cmd, config), ncaID)
	if err != nil {
		return fmt.Errorf("failed to list registered clusters from ICMS: %w", err)
	}

	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]interface{}{"clusters": clusters})
	}

	if len(clusters) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No registered clusters found.")
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Found %d registered cluster(s):\n\n", len(clusters))
	for _, cluster := range clusters {
		fmt.Fprintf(cmd.OutOrStdout(), "Name: %s\n", cluster.ClusterName)
		fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", cluster.ClusterID)
		fmt.Fprintf(cmd.OutOrStdout(), "Cluster Group ID: %s\n", cluster.ClusterGroupID)
		if cluster.ClusterStatus != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", cluster.ClusterStatus)
		}
		if cluster.NVCAVersion != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "NVCA Version: %s\n", cluster.NVCAVersion)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "---")
	}

	return nil
}
