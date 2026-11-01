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

	"github.com/spf13/cobra"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/progress"
	"nvcf-cli/internal/selfhosted/teardown"
)

var (
	uninstallControlPlane                bool
	uninstallComputePlane                bool
	uninstallClusterName                 string
	uninstallNoApply                     bool
	uninstallRemovePersistent            bool
	uninstallForceWithRegisteredClusters bool
	uninstallKeepNamespaces              bool
	uninstallConfirm                     bool
)

var selfHostedUninstallCmd = &cobra.Command{
	Use:          "uninstall",
	Short:        "Per-plane teardown primitive (mirrors install)",
	SilenceUsage: true,
	RunE:         runSelfHostedUninstall,
}

func init() {
	selfHostedCmd.AddCommand(selfHostedUninstallCmd)
	selfHostedUninstallCmd.Flags().BoolVar(&uninstallControlPlane, "control-plane", false,
		"Tear down the control-plane release set")
	selfHostedUninstallCmd.Flags().BoolVar(&uninstallComputePlane, "compute-plane", false,
		"Tear down the compute-plane release set (requires --cluster-name)")
	selfHostedUninstallCmd.Flags().StringVar(&uninstallClusterName, "cluster-name", "",
		"Cluster name (required for --compute-plane)")
	selfHostedUninstallCmd.Flags().BoolVar(&uninstallNoApply, "no-apply", false,
		"Render current manifests via `helm get manifest` to stdout instead of invoking helmfile destroy")
	selfHostedUninstallCmd.Flags().BoolVar(&uninstallRemovePersistent, "remove-persistent", false,
		"Also delete PVCs (default: preserve)")
	selfHostedUninstallCmd.Flags().BoolVar(&uninstallForceWithRegisteredClusters, "force-with-registered-clusters", false,
		"Allow control-plane teardown while compute planes are still registered")
	selfHostedUninstallCmd.Flags().BoolVar(&uninstallKeepNamespaces, "keep-namespaces", false,
		"Don't delete the helm-managed namespaces")
	selfHostedUninstallCmd.Flags().BoolVar(&uninstallConfirm, "confirm", false,
		"Required when --remove-persistent is set")
}

func runSelfHostedUninstall(c *cobra.Command, _ []string) error {
	if !uninstallControlPlane && !uninstallComputePlane {
		return fmt.Errorf("exactly one of --control-plane or --compute-plane is required")
	}
	if uninstallControlPlane && uninstallComputePlane {
		return fmt.Errorf("--control-plane and --compute-plane are mutually exclusive")
	}
	if uninstallComputePlane && uninstallClusterName == "" {
		return fmt.Errorf("--compute-plane requires --cluster-name")
	}
	if uninstallRemovePersistent && !uninstallConfirm {
		return fmt.Errorf("--remove-persistent requires --confirm")
	}

	if uninstallNoApply {
		return runUninstallNoApply(c)
	}
	return runUninstallDestroy(c)
}

func runUninstallDestroy(c *cobra.Command) error {
	ctx := c.Context()
	helmRuntimeMode, err := resolveSelfHostedHelmRuntimeMode(ctx)
	if err != nil {
		return fmt.Errorf("resolve Helm runtime mode: %w", err)
	}

	plane := "control-plane"
	kubeCtx := selfHostedControlPlaneContext
	if uninstallComputePlane {
		plane = "compute-plane"
		kubeCtx = selfHostedComputePlaneContext
	}

	stackSource := selfHostedControlPlaneStack
	stackOCI := builtInControlPlaneStackOCI()
	if uninstallComputePlane {
		stackSource = selfHostedComputePlaneStack
		stackOCI = builtInComputePlaneStackOCI()
	}
	resolved, err := selfhosted.ResolveStack(ctx, selfhosted.StackOptions{
		Source:        stackSource,
		BuiltInOCIRef: stackOCI,
	})
	if err != nil {
		return fmt.Errorf("resolve stack: %w", err)
	}

	if err := teardown.Destroy(teardown.DestroyOpts{
		Plane:           plane,
		ClusterName:     uninstallClusterName,
		KubeContext:     kubeCtx,
		StackPath:       resolved.Path,
		Env:             selfHostedEnv,
		HelmRuntimeMode: helmRuntimeMode,
		Stdout:          c.OutOrStdout(),
		Stderr:          c.ErrOrStderr(),
		Ctx:             ctx,
	}, &uninstallNoopSink{}); err != nil {
		return fmt.Errorf("helmfile destroy: %w", err)
	}

	return nil
}

func runUninstallNoApply(c *cobra.Command) error {
	ctx := c.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	kubeCtx := selfHostedControlPlaneContext
	if uninstallComputePlane {
		kubeCtx = selfHostedComputePlaneContext
	}

	// M+11.H: release list is hardcoded per plane.
	// TODO(M+11.J): walk helmfile.d/ to enumerate releases dynamically.
	releases := uninstallDefaultReleases()

	return teardown.RenderUninstall(ctx, teardown.RenderUninstallOpts{
		KubeContext: kubeCtx,
		Releases:    releases,
		Stdout:      c.OutOrStdout(),
		Stderr:      c.ErrOrStderr(),
	})
}

// uninstallDefaultReleases returns the hardcoded release list for --no-apply mode.
// Names + namespaces match defaultDownReleases — verified on mcamp-dev-vm
// against the real nvcf-self-managed-stack helmfile-rendered releases.
// TODO(M+11.J): enumerate releases by walking helmfile.d/ in the resolved stack.
func uninstallDefaultReleases() []teardown.ReleaseRef {
	if uninstallControlPlane {
		return []teardown.ReleaseRef{
			{Name: "ingress", Namespace: "envoy-gateway-system"},
			{Name: "invocation-service", Namespace: "nvcf"},
			{Name: "grpc-proxy", Namespace: "nvcf"},
			{Name: "api", Namespace: "nvcf"},
			{Name: "notary-service", Namespace: "nvcf"},
			{Name: "reval", Namespace: "nvcf"},
			{Name: "ess-api", Namespace: "ess"},
			{Name: "admin-issuer-proxy", Namespace: "api-keys"},
			{Name: "api-keys", Namespace: "api-keys"},
			{Name: "nats-auth-callout-service", Namespace: "nats-system"},
			{Name: "openbao-server", Namespace: "vault-system"},
			{Name: "cassandra", Namespace: "cassandra-system"},
			{Name: "nats", Namespace: "nats-system"},
			{Name: "sis", Namespace: "sis"},
			{Name: "eg", Namespace: "envoy-gateway-system"},
		}
	}
	return []teardown.ReleaseRef{
		{Name: "nvca-operator", Namespace: "nvca-operator"},
	}
}

// uninstallNoopSink satisfies progress.EventSink for callers (like
// runUninstallDestroy) that don't emit structured events.
type uninstallNoopSink struct{}

func (uninstallNoopSink) Emit(_ context.Context, _ progress.Event) error { return nil }
func (uninstallNoopSink) Close() error                                   { return nil }
