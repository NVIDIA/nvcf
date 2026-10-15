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
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/controlplaneprofile"
)

var (
	controlPlaneProfileValidateFile    string
	controlPlaneProfileValidateRequire string
	controlPlaneProfileExportCluster   string
	controlPlaneProfileExportNCAID     string
	controlPlaneProfileExportRegion    string
)

var selfHostedControlPlaneCmd = &cobra.Command{
	Use:   "control-plane",
	Short: "Manage self-hosted control-plane artifacts",
}

var selfHostedControlPlaneProfileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage self-hosted control-plane profile files",
}

var selfHostedControlPlaneProfileValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate a self-hosted control-plane profile file",
	SilenceUsage: true,
	RunE:         runControlPlaneProfileValidate,
}

var selfHostedControlPlaneProfileExportCmd = &cobra.Command{
	Use:          "export",
	Short:        "Export or recreate a self-hosted control-plane profile file",
	SilenceUsage: true,
	RunE:         runControlPlaneProfileExport,
}

func init() {
	selfHostedCmd.AddCommand(selfHostedControlPlaneCmd)
	selfHostedControlPlaneCmd.AddCommand(selfHostedControlPlaneProfileCmd)
	selfHostedControlPlaneProfileCmd.AddCommand(selfHostedControlPlaneProfileValidateCmd)
	selfHostedControlPlaneProfileCmd.AddCommand(selfHostedControlPlaneProfileExportCmd)

	selfHostedControlPlaneProfileValidateCmd.Flags().StringVar(&controlPlaneProfileValidateFile, "file", "", "Path to control-plane profile YAML")
	_ = selfHostedControlPlaneProfileValidateCmd.MarkFlagRequired("file")
	selfHostedControlPlaneProfileValidateCmd.Flags().StringVar(&controlPlaneProfileValidateRequire, "require", string(controlplaneprofile.RequireAny),
		"Endpoint scope to require: any, in-cluster, compute-reachable, or both")

	selfHostedControlPlaneProfileExportCmd.Flags().StringVar(&controlPlaneProfileExportCluster, "cluster-name", "", "Control-plane cluster name for generated profile metadata")
	selfHostedControlPlaneProfileExportCmd.Flags().StringVar(&controlPlaneProfileExportNCAID, "nca-id", "nvcf-default", "NCA ID (account) for generated profile metadata")
	selfHostedControlPlaneProfileExportCmd.Flags().StringVar(&controlPlaneProfileExportRegion, "region", "us-west-1", "Cluster region for generated profile metadata")
}

func runControlPlaneProfileValidate(c *cobra.Command, _ []string) error {
	require, err := parseControlPlaneProfileRequireMode(controlPlaneProfileValidateRequire)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(controlPlaneProfileValidateFile)
	if err != nil {
		return fmt.Errorf("read control-plane profile %q: %w", controlPlaneProfileValidateFile, err)
	}
	result, err := controlplaneprofile.ParseAndValidate(data, controlplaneprofile.ValidateOptions{Require: require})
	if err != nil {
		return err
	}
	fmt.Fprintln(c.OutOrStdout(), "control-plane profile is valid")
	fmt.Fprintln(c.OutOrStdout(), result.Summary())
	return nil
}

func runControlPlaneProfileExport(c *cobra.Command, _ []string) error {
	resolved, err := selfhosted.ResolveStack(c.Context(), selfhosted.StackOptions{
		Source:        selfHostedControlPlaneStack,
		BuiltInOCIRef: builtInControlPlaneStackOCI(),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.ErrOrStderr(), ">>> Resolving stack: %s\n", stackDescriptor(resolved))

	path, err := writeControlPlaneProfile(controlPlaneProfileWriteRequest{
		Ctx:                 c.Context(),
		StackPath:           resolved.Path,
		ClusterName:         controlPlaneProfileExportCluster,
		NCAID:               controlPlaneProfileExportNCAID,
		Region:              controlPlaneProfileExportRegion,
		Env:                 selfHostedEnv,
		ControlPlaneContext: selfHostedControlPlaneContext,
		ComputePlaneContext: selfHostedComputePlaneContext,
		ICMSURL:             resolveICMSURL(selfHostedICMSURL),
		NATSURL:             selfHostedNATSURL,
		SourceRootCA:        true,
	})
	if err != nil {
		return fmt.Errorf("writing control-plane profile: %w", err)
	}
	fmt.Fprintf(c.ErrOrStderr(), "Wrote control-plane profile:\n  %s\n", path)
	return nil
}

func parseControlPlaneProfileRequireMode(value string) (controlplaneprofile.RequireMode, error) {
	if value == "" {
		return controlplaneprofile.RequireAny, nil
	}
	switch controlplaneprofile.RequireMode(value) {
	case controlplaneprofile.RequireAny, controlplaneprofile.RequireInCluster, controlplaneprofile.RequireComputeReachable, controlplaneprofile.RequireBoth:
		return controlplaneprofile.RequireMode(value), nil
	default:
		return "", fmt.Errorf("--require must be one of any, in-cluster, compute-reachable, or both")
	}
}
