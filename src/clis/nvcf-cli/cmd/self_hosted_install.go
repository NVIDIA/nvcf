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

	"github.com/spf13/cobra"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted"
)

// newClusterClientForSelfHosted is a package-level seam so unit tests can
// inject a fake without hitting a real ICMS endpoint. Production callers use the
// default factory; tests assign a closure that returns a fakeClusterClient.
var newClusterClientForSelfHosted = func(icmsURL string) (selfhosted.ClusterClient, error) {
	return selfhosted.NewClusterClient(icmsURL)
}

// fetchClusterIdentity is a package-level seam so unit tests can inject a fake
// JWKS fetcher without invoking kubectl. It delegates to fetchClusterJWKS in
// cmd/cluster_registration.go (same package) using a zero-value kubeconfig.
// kctx selects the kubeconfig context; empty string uses the current-context
// (single-cluster mode). M+9.E will pass non-empty values for split-cluster.
// Returns (oidcIssuer, jwks, identitySource, error).
var fetchClusterIdentity = func(ctx context.Context, kctx string) (issuer string, jwks string, identitySource string, err error) {
	cfg, err := client.LoadConfig()
	if err != nil {
		return "", "", "", fmt.Errorf("loading config for JWKS fetch: %w", err)
	}
	return fetchClusterJWKS(cfg, "")
}

var (
	installControlPlane bool
	installComputePlane bool
	installClusterName  string
	installNCAID        string
	installRegion       string
)

var selfHostedInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Render (and optionally apply) self-hosted NVCF manifests",
	RunE:         runSelfHostedInstall,
	SilenceUsage: true,
}

func init() {
	selfHostedCmd.AddCommand(selfHostedInstallCmd)
	selfHostedInstallCmd.Flags().BoolVar(&installControlPlane, "control-plane", false, "Render the control-plane release set")
	selfHostedInstallCmd.Flags().BoolVar(&installComputePlane, "compute-plane", false, "Render the compute-plane release set (requires --cluster-name)")
	selfHostedInstallCmd.Flags().StringVar(&installClusterName, "cluster-name", "", "Cluster name for compute-plane installs")
	selfHostedInstallCmd.Flags().StringVar(&installNCAID, "nca-id", "nvcf-default", "NCA ID (account) the cluster registers under")
	selfHostedInstallCmd.Flags().StringVar(&installRegion, "region", "us-west-1", "Cluster region (ICMS requires non-empty)")
}

func runSelfHostedInstall(c *cobra.Command, _ []string) error {
	if !installControlPlane && !installComputePlane {
		return fmt.Errorf("exactly one of --control-plane or --compute-plane is required")
	}
	if installControlPlane && installComputePlane {
		return fmt.Errorf("--control-plane and --compute-plane are mutually exclusive")
	}
	if installComputePlane && installClusterName == "" {
		return fmt.Errorf("--compute-plane requires --cluster-name")
	}

	resolved, err := selfhosted.ResolveStack(c.Context(), selfhosted.StackOptions{
		Source:        selfHostedStack,
		BuiltInOCIRef: builtInStackOCI(),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.ErrOrStderr(), ">>> Resolving stack: %s\n", stackDescriptor(resolved))

	if installControlPlane {
		return selfhosted.Render(selfhosted.RenderOptions{
			StackPath:   resolved.Path,
			Env:         selfHostedEnv,
			Apply:       false,
			KubeContext: selfHostedControlPlaneContext, // M+9: empty in single-cluster mode
			Stdout:      c.OutOrStdout(),
			Stderr:      c.ErrOrStderr(),
			Ctx:         c.Context(),
		})
	}
	// --compute-plane: register cluster then render worker-layer manifests.
	icmsURL := resolveICMSURL(selfHostedICMSURL)
	cc, err := newClusterClientForSelfHosted(icmsURL)
	if err != nil {
		return fmt.Errorf("constructing cluster client: %w", err)
	}
	defer cc.Close()

	// M+9: register reads JWKS from the compute-plane K8s API. Empty in single-cluster mode.
	oidcIssuer, jwks, identitySource, err := fetchClusterIdentity(c.Context(), selfHostedComputePlaneContext)
	if err != nil {
		return fmt.Errorf("fetching cluster JWKS: %w", err)
	}

	if identitySource == "" {
		identitySource = "psat"
	}

	ncaID := installNCAID
	if v := os.Getenv("NCA_ID"); v != "" {
		ncaID = v
	}
	region := installRegion
	if v := os.Getenv("CLUSTER_REGION"); v != "" {
		region = v
	}
	resp, err := cc.RegisterCluster(c.Context(), selfhosted.RegisterRequest{
		ClusterName:    installClusterName,
		NCAID:          ncaID,
		Region:         region,
		JWKS:           jwks,
		OIDCIssuer:     oidcIssuer,
		IdentitySource: identitySource,
	})
	if err != nil {
		return fmt.Errorf("cluster register: %w", err)
	}
	fmt.Fprintf(c.ErrOrStderr(), ">>> Cluster registered: clusterId=%s clusterGroupId=%s\n", resp.ClusterID, resp.ClusterGroupID)

	natsURL := resolveNATSURL(selfHostedNATSURL, icmsURL)
	if err := writeRegisterValuesYAML(resolved.Path, installClusterName, ncaID, region, identitySource, resp.ClusterID, resp.ClusterGroupID, icmsURL, natsURL); err != nil {
		return fmt.Errorf("writing register-values: %w", err)
	}

	helmfileFile, selector := computePlaneTarget(resolved.Path)
	return selfhosted.Render(selfhosted.RenderOptions{
		StackPath:    resolved.Path,
		HelmfileFile: helmfileFile,
		Env:          selfHostedEnv,
		Selector:     selector,
		Apply:        false,
		KubeContext:  selfHostedComputePlaneContext, // M+9: empty in single-cluster mode
		Stdout:       c.OutOrStdout(),
		Stderr:       c.ErrOrStderr(),
		Ctx:          c.Context(),
		ExtraEnv: []string{
			"CLUSTER_NAME=" + installClusterName,
			"CLUSTER_ID=" + resp.ClusterID,
			"CLUSTER_GROUP_ID=" + resp.ClusterGroupID,
			"IDENTITY_SOURCE=" + identitySource,
			"NCA_ID=" + ncaID,
			"CLUSTER_REGION=" + region,
		},
	})
}

// computePlaneTarget picks the helmfile target for the worker layer. If the
// stack tree contains a top-level helmfile-nvca-operator.yaml.gotmpl (the
// multi-cluster topology where compute-plane is split out from helmfile.d/),
// use that file directly with no selector. Otherwise default to the bundled
// layout: helmfile.d/ filtered by release-group=workers.
func computePlaneTarget(stackPath string) (helmfileFile, selector string) {
	if _, err := os.Stat(stackPath + "/helmfile-nvca-operator.yaml.gotmpl"); err == nil {
		return "helmfile-nvca-operator.yaml.gotmpl", ""
	}
	return "", "release-group=workers"
}

func stackDescriptor(r *selfhosted.ResolvedStack) string {
	if r.OCIRef != "" {
		return r.OCIRef
	}
	return r.Path
}

// builtInStackOCI returns the digest-pinned default OCI URL baked at CLI build
// time. The string literal is overwritten by ldflags in release builds; CI
// publishes the matching artifact via M1.
func builtInStackOCI() string {
	if v := os.Getenv("NVCF_CLI_DEFAULT_STACK"); v != "" {
		return v
	}
	return defaultStackOCI
}

// defaultStackOCI is set via -ldflags '-X nvcf-cli/cmd.defaultStackOCI=oci://…'.
// Empty in dev builds; the user must pass --stack=.
var defaultStackOCI = ""
