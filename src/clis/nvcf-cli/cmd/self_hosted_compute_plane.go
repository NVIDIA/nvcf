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
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/controlplaneprofile"
	"nvcf-cli/internal/selfhosted/reachability"
)

var (
	computePlaneInstallValues       string
	computePlaneInstallKubeContext  string
	computePlaneInstallClusterName  string
	computePlaneInstallNCAID        string
	computePlaneRegisterDryRun      bool
	computePlaneRegisterProfile     string
	computePlaneRegisterClusterName string
	computePlaneRegisterKubeContext string
	computePlaneRegisterRegion      string
)

var selfHostedComputePlaneCmd = &cobra.Command{
	Use:          "compute-plane",
	Short:        "Manage self-hosted compute-plane clusters",
	SilenceUsage: true,
}

var selfHostedComputePlaneInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Install the compute-plane NVCA operator from a values file",
	RunE:         runSelfHostedComputePlaneInstall,
	SilenceUsage: true,
}

var selfHostedComputePlaneRegisterCmd = &cobra.Command{
	Use:          "register",
	Short:        "Register a compute-plane cluster with a self-hosted control plane",
	RunE:         runSelfHostedComputePlaneRegister,
	SilenceUsage: true,
}

var computePlaneRegisterReachabilityCheck = func(ctx context.Context, req reachability.CheckRequest) error {
	return reachability.Check(ctx, req)
}

func init() {
	selfHostedCmd.AddCommand(selfHostedComputePlaneCmd)
	selfHostedComputePlaneCmd.AddCommand(selfHostedComputePlaneInstallCmd)
	selfHostedComputePlaneCmd.AddCommand(selfHostedComputePlaneRegisterCmd)
	selfHostedComputePlaneInstallCmd.Flags().StringVar(&computePlaneInstallValues, "values", "", "nvca-operator values file to install")
	selfHostedComputePlaneInstallCmd.Flags().StringVar(&computePlaneInstallKubeContext, "kube-context", "", "kubeconfig context for the compute-plane cluster")
	selfHostedComputePlaneInstallCmd.Flags().StringVar(&computePlaneInstallClusterName, "cluster-name", "", "Cluster name override when the values file does not set clusterName")
	selfHostedComputePlaneInstallCmd.Flags().StringVar(&computePlaneInstallNCAID, "nca-id", "nvcf-default", "NCA ID override when the values file does not set ncaID")

	selfHostedComputePlaneRegisterCmd.Flags().BoolVar(&computePlaneRegisterDryRun, "dry-run", false, "Validate registration inputs without mutating SIS or writing values")
	selfHostedComputePlaneRegisterCmd.Flags().StringVar(&computePlaneRegisterProfile, "control-plane-profile", "", "Control-plane profile file")
	selfHostedComputePlaneRegisterCmd.Flags().StringVar(&computePlaneRegisterClusterName, "cluster-name", "", "Compute-plane cluster name")
	selfHostedComputePlaneRegisterCmd.Flags().StringVar(&computePlaneRegisterKubeContext, "kube-context", "", "kubeconfig context for the compute-plane cluster")
	selfHostedComputePlaneRegisterCmd.Flags().StringVar(&computePlaneRegisterRegion, "region", "us-west-1", "Compute-plane cluster region")
}

func runSelfHostedComputePlaneInstall(c *cobra.Command, _ []string) error {
	if computePlaneInstallValues == "" {
		return fmt.Errorf("--values is required")
	}

	valuesPath, err := filepath.Abs(computePlaneInstallValues)
	if err != nil {
		return fmt.Errorf("resolving values path: %w", err)
	}
	metadata, err := readNVCAValuesMetadata(valuesPath)
	if err != nil {
		return err
	}
	clusterName := firstNonEmpty(computePlaneInstallClusterName, metadata.ClusterName, inferClusterNameFromValuesPath(valuesPath))
	if clusterName == "" {
		return fmt.Errorf("cluster name is required: set clusterName in the values file or pass --cluster-name")
	}
	ncaID := firstNonEmpty(metadata.NCAID, computePlaneInstallNCAID)

	resolved, err := selfhosted.ResolveStack(c.Context(), selfhosted.StackOptions{
		Source:        selfHostedStack,
		BuiltInOCIRef: builtInStackOCI(),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.ErrOrStderr(), ">>> Resolving stack: %s\n", stackDescriptor(resolved))

	helmRuntimeMode, err := resolveSelfHostedHelmRuntimeMode(c.Context())
	if err != nil {
		return fmt.Errorf("resolving helm runtime: %w", err)
	}

	helmfileFile, selector := computePlaneTarget(resolved.Path)
	return selfhosted.Render(selfhosted.RenderOptions{
		StackPath:       resolved.Path,
		HelmfileFile:    helmfileFile,
		Env:             selfHostedEnv,
		Selector:        selector,
		Apply:           !selfHostedNoApply,
		KubeContext:     computePlaneInstallKubeContext,
		HelmRuntimeMode: helmRuntimeMode,
		Stdout:          c.OutOrStdout(),
		Stderr:          c.ErrOrStderr(),
		Ctx:             c.Context(),
		ExtraEnv:        computePlaneInstallEnv(valuesPath, clusterName, ncaID),
	})
}

type nvcaValuesMetadata struct {
	ClusterName string
	NCAID       string
}

func readNVCAValuesMetadata(path string) (nvcaValuesMetadata, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nvcaValuesMetadata{}, fmt.Errorf("reading values file: %w", err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(body, &values); err != nil {
		return nvcaValuesMetadata{}, fmt.Errorf("parsing values file: %w", err)
	}
	return nvcaValuesMetadata{
		ClusterName: stringMapValue(values, "clusterName"),
		NCAID:       firstNonEmpty(stringMapValue(values, "ncaID"), stringMapValue(values, "ncaId")),
	}, nil
}

func stringMapValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	v, ok := values[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func inferClusterNameFromValuesPath(path string) string {
	base := filepath.Base(path)
	for _, suffix := range []string{"-nvca-values.yaml", "-nvca-values.yml", "-register-values.yaml", "-register-values.yml", ".yaml", ".yml"} {
		if strings.HasSuffix(base, suffix) {
			return strings.TrimSuffix(base, suffix)
		}
	}
	return ""
}

func computePlaneInstallEnv(valuesPath, clusterName, ncaID string) []string {
	return []string{
		"NVCF_NVCA_VALUES_FILE=" + valuesPath,
		"HELMFILE_INCLUDE_WORKER_LAYER=true",
		"CLUSTER_NAME=" + clusterName,
		"NCA_ID=" + ncaID,
	}
}

func runSelfHostedComputePlaneRegister(c *cobra.Command, _ []string) error {
	if computePlaneRegisterProfile == "" {
		return fmt.Errorf("--control-plane-profile is required")
	}
	if computePlaneRegisterClusterName == "" {
		return fmt.Errorf("--cluster-name is required")
	}

	validation, selected, err := loadControlPlaneProfileForComputeRegister(computePlaneRegisterProfile, computePlaneRegisterClusterName)
	if err != nil {
		return err
	}
	registrationICMSURL := computePlaneRegisterICMSURL(selected.Endpoints.ICMSURL)
	if err := computePlaneRegisterReachabilityCheck(c.Context(), reachability.CheckRequest{
		TargetClusterName: computePlaneRegisterClusterName,
		ICMSURL:           registrationICMSURL,
		ReValURL:          selected.Endpoints.ReValURL,
		NATSURL:           selected.Endpoints.NATSURL,
		SISHost:           validation.Profile.ControlPlane.Hosts.SIS,
		ReValHost:         validation.Profile.ControlPlane.Hosts.ReVal,
		ProbeHTTP:         shouldProbeComputeRegisterHTTP(selected.Name),
	}); err != nil {
		return err
	}

	oidcIssuer, jwks, identitySource, err := fetchClusterIdentity(c.Context(), computePlaneRegisterKubeContext)
	if err != nil {
		return fmt.Errorf("discovering target cluster identity: %w", err)
	}
	if strings.TrimSpace(oidcIssuer) == "" {
		return fmt.Errorf("discovering target cluster identity: OIDC issuer is empty")
	}
	if strings.TrimSpace(jwks) == "" {
		return fmt.Errorf("discovering target cluster identity: JWKS is empty")
	}
	if identitySource == "" {
		identitySource = "psat"
	}

	cp := validation.Profile.ControlPlane
	var registration *selfhosted.RegisterResponse
	if !computePlaneRegisterDryRun {
		cc, err := newClusterClientForSelfHosted(registrationICMSURL)
		if err != nil {
			return fmt.Errorf("constructing cluster client: %w", err)
		}
		defer cc.Close()
		registration, err = cc.RegisterCluster(c.Context(), selfhosted.RegisterRequest{
			ClusterName:    computePlaneRegisterClusterName,
			NCAID:          cp.NCAID,
			Region:         computePlaneRegisterRegion,
			JWKS:           jwks,
			OIDCIssuer:     oidcIssuer,
			IdentitySource: identitySource,
		})
		if err != nil {
			return fmt.Errorf("cluster register: %w", err)
		}
	}

	out := c.OutOrStdout()
	fmt.Fprintf(out, "dryRun: %t\n", computePlaneRegisterDryRun)
	fmt.Fprintf(out, "clusterName: %s\n", computePlaneRegisterClusterName)
	fmt.Fprintf(out, "controlPlaneClusterName: %s\n", cp.ClusterName)
	fmt.Fprintf(out, "ncaID: %s\n", cp.NCAID)
	fmt.Fprintf(out, "region: %s\n", computePlaneRegisterRegion)
	fmt.Fprintf(out, "kubeContext: %s\n", computePlaneRegisterKubeContext)
	fmt.Fprintf(out, "endpointScope: %s\n", selected.Name)
	fmt.Fprintf(out, "icmsURL: %s\n", registrationICMSURL)
	fmt.Fprintf(out, "revalURL: %s\n", selected.Endpoints.ReValURL)
	fmt.Fprintf(out, "natsURL: %s\n", selected.Endpoints.NATSURL)
	fmt.Fprintf(out, "oidcIssuer: %s\n", oidcIssuer)
	fmt.Fprintf(out, "identitySource: %s\n", identitySource)
	if registration != nil {
		fmt.Fprintf(out, "clusterID: %s\n", registration.ClusterID)
		fmt.Fprintf(out, "clusterGroupID: %s\n", registration.ClusterGroupID)
		fmt.Fprintln(out, "sisMutation: completed")
	} else {
		fmt.Fprintln(out, "sisMutation: skipped")
	}
	fmt.Fprintln(out, "valuesWrite: skipped")
	return nil
}

func computePlaneRegisterICMSURL(profileICMSURL string) string {
	if strings.TrimSpace(selfHostedICMSURL) != "" {
		return selfHostedICMSURL
	}
	if v := os.Getenv("NVCF_ICMS_URL"); strings.TrimSpace(v) != "" {
		return v
	}
	if v := os.Getenv("NVCF_SIS_URL"); strings.TrimSpace(v) != "" {
		return v
	}
	cfg, err := client.LoadConfigWithoutAuth()
	if err == nil && strings.TrimSpace(cfg.ICMSURL) != "" {
		return cfg.ICMSURL
	}
	return profileICMSURL
}

func shouldProbeComputeRegisterHTTP(endpointScope selfhosted.ControlPlaneProfileEndpointScopeName) bool {
	if endpointScope != selfhosted.EndpointScopeComputeReachable {
		return false
	}
	return !strings.EqualFold(selfHostedEnv, "local")
}

func loadControlPlaneProfileForComputeRegister(path, clusterName string) (*controlplaneprofile.ValidationResult, selfhosted.ControlPlaneProfileEndpointScopeSelection, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, selfhosted.ControlPlaneProfileEndpointScopeSelection{}, fmt.Errorf("reading control-plane profile: %w", err)
	}
	validation, err := controlplaneprofile.ParseAndValidate(body, controlplaneprofile.ValidateOptions{Require: controlplaneprofile.RequireAny})
	if err != nil {
		return nil, selfhosted.ControlPlaneProfileEndpointScopeSelection{}, err
	}
	selected, err := selfhosted.SelectControlPlaneProfileEndpointScope(validation.Profile, clusterName)
	if err != nil {
		return nil, selfhosted.ControlPlaneProfileEndpointScopeSelection{}, err
	}
	validation, err = controlplaneprofile.ParseAndValidate(body, controlplaneprofile.ValidateOptions{
		Require: selfhosted.ControlPlaneProfileRequireModeForEndpointScope(selected.Name),
	})
	if err != nil {
		return nil, selfhosted.ControlPlaneProfileEndpointScopeSelection{}, err
	}
	return validation, selected, nil
}
