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
	"crypto/tls"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/progress"
)

// newClusterClientForSelfHosted is a package-level seam so unit tests can
// inject a fake without hitting a real ICMS endpoint. Production callers use the
// default factory; tests assign a closure that returns a fakeClusterClient.
//
// The factory threads selfHostedToken (the --token flag) into the underlying
// *client.Config. When --token is supplied by a CI/non-interactive caller, the
// admin JWT must be used as the Bearer on cluster-management requests. Without
// this plumbing the SIS register call would race the auth-gate and hit /v1
// with no Authorization header.
var newClusterClientForSelfHosted = func(icmsURL string) (selfhosted.ClusterClient, error) {
	return selfhosted.NewClusterClientWithToken(icmsURL, selfHostedToken)
}

// newClusterClientForSelfHostedWithTrust is the trust-aware seam used by the
// compute-plane register flow (R-4): it applies the control-plane profile's
// managementTls to the management-API client. Tests override it like the
// plain factory above.
var newClusterClientForSelfHostedWithTrust = func(icmsURL string, tlsCfg *tls.Config) (selfhosted.ClusterClient, error) {
	return selfhosted.NewClusterClientWithTokenAndTrust(icmsURL, selfHostedToken, tlsCfg)
}

// loadClusterIdentityConfig is a package-level seam so unit tests can verify
// the kubectl-facing config without depending on process-global Viper state.
var loadClusterIdentityConfig = client.LoadConfigWithoutAuth

func clusterIdentityConfig(kctx string) (*client.Config, error) {
	cfg, err := loadClusterIdentityConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config for JWKS fetch: %w", err)
	}
	cfg.KubeContext = kctx
	return cfg, nil
}

// fetchClusterIdentity is a package-level seam so unit tests can inject a fake
// JWKS fetcher without invoking kubectl. It delegates to fetchClusterJWKS in
// cmd/cluster_registration.go (same package) using the loaded CLI config.
// kctx selects the kubeconfig context; empty string uses the current-context
// (single-cluster mode). M+9.E will pass non-empty values for split-cluster.
// Returns (oidcIssuer, jwks, identitySource, error).
var fetchClusterIdentity = func(ctx context.Context, kctx string) (issuer string, jwks string, identitySource string, err error) {
	cfg, err := clusterIdentityConfig(kctx)
	if err != nil {
		return "", "", "", err
	}
	return fetchClusterJWKS(cfg, "")
}

type discardProgressSink struct{}

func (discardProgressSink) Emit(context.Context, progress.Event) error { return nil }
func (discardProgressSink) Close() error                               { return nil }

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
	selfHostedInstallCmd.Flags().StringVar(&installClusterName, "cluster-name", "", "Cluster name for generated artifacts and compute-plane installs")
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

	var stackSource, stackOCI string
	if installControlPlane {
		stackSource = selfHostedControlPlaneStack
		stackOCI = builtInControlPlaneStackOCI()
	} else {
		stackSource = selfHostedComputePlaneStack
		stackOCI = builtInComputePlaneStackOCI()
	}
	resolved, err := selfhosted.ResolveStack(c.Context(), selfhosted.StackOptions{
		Source:        stackSource,
		BuiltInOCIRef: stackOCI,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.ErrOrStderr(), ">>> Resolving stack: %s\n", stackDescriptor(resolved))

	if installControlPlane {
		if err := selfhosted.Render(selfhosted.RenderOptions{
			StackPath:   resolved.Path,
			Env:         selfHostedEnv,
			Apply:       !selfHostedNoApply,
			KubeContext: selfHostedControlPlaneContext, // M+9: empty in single-cluster mode
			Stdout:      c.OutOrStdout(),
			Stderr:      c.ErrOrStderr(),
			Ctx:         c.Context(),
		}); err != nil {
			return err
		}
		path, err := writeControlPlaneProfile(controlPlaneProfileWriteRequest{
			Ctx:                 c.Context(),
			StackPath:           resolved.Path,
			ClusterName:         installClusterName,
			NCAID:               installNCAID,
			Region:              installRegion,
			Env:                 selfHostedEnv,
			ControlPlaneContext: selfHostedControlPlaneContext,
			ComputePlaneContext: selfHostedComputePlaneContext,
			ICMSURL:             resolveICMSURL(selfHostedICMSURL),
			NATSURL:             selfHostedNATSURL,
			SourceRootCA:        !selfHostedNoApply,
		})
		if err != nil {
			return fmt.Errorf("writing control-plane profile: %w", err)
		}
		fmt.Fprintf(c.ErrOrStderr(), "Wrote control-plane profile:\n  %s\n", path)
		if !selfHostedNoApply && selfHostedToken == "" {
			// Post-install admin-token mint is best-effort: it pre-warms the
			// admin JWT cache so the next command does not need to call
			// `init`. The install path itself does not consume the token, so
			// failures here are non-fatal -- surface a hint and let the
			// caller mint a token when ready. This removes the need for a
			// throwaway `--token=...` value just to bypass this gate when
			// running in CI / under --non-interactive / against a non-TTY
			// stdin.
			if err := authGatePhase5ForcedRefresh(c.Context(), discardProgressSink{}, time.Now().UTC()); err != nil {
				fmt.Fprintf(c.ErrOrStderr(),
					">>> Note: skipped post-install admin-token mint (%v).\n"+
						">>> Run `nvcf-cli init` to mint an admin token when ready.\n",
					err,
				)
			}
		}
		return nil
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

	endpoints := resolveNVCAEndpointValues(selfHostedEnv, selfHostedControlPlaneContext, selfHostedComputePlaneContext, icmsURL, selfHostedNATSURL)
	if err := writeRegisterValuesYAML(registerValuesWriteRequest{
		StackPath:      resolved.Path,
		ClusterName:    installClusterName,
		NCAID:          ncaID,
		Region:         region,
		IdentitySource: identitySource,
		ClusterID:      resp.ClusterID,
		ClusterGroupID: resp.ClusterGroupID,
		Endpoints:      endpoints,
	}); err != nil {
		return fmt.Errorf("writing register-values: %w", err)
	}

	return selfhosted.Render(selfhosted.RenderOptions{
		StackPath:   resolved.Path,
		Env:         selfHostedEnv,
		Apply:       !selfHostedNoApply,
		KubeContext: selfHostedComputePlaneContext, // M+9: empty in single-cluster mode
		Stdout:      c.OutOrStdout(),
		Stderr:      c.ErrOrStderr(),
		Ctx:         c.Context(),
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

func authGatePhase5ForcedRefresh(ctx context.Context, sink progress.EventSink, p5Start time.Time) error {
	previousRefreshToken := selfHostedRefreshToken
	selfHostedRefreshToken = true
	defer func() {
		selfHostedRefreshToken = previousRefreshToken
	}()
	return authGatePhase5(ctx, sink, p5Start)
}

func stackDescriptor(r *selfhosted.ResolvedStack) string {
	if r.OCIRef != "" {
		return r.OCIRef
	}
	return r.Path
}

// builtInControlPlaneStackOCI returns the digest-pinned default OCI URL for the
// control-plane stack, baked at CLI build time via ldflags.
func builtInControlPlaneStackOCI() string {
	if v := os.Getenv("NVCF_CLI_DEFAULT_CONTROL_PLANE_STACK"); v != "" {
		return v
	}
	return defaultControlPlaneStackOCI
}

// builtInComputePlaneStackOCI returns the digest-pinned default OCI URL for the
// compute-plane stack, baked at CLI build time via ldflags.
func builtInComputePlaneStackOCI() string {
	if v := os.Getenv("NVCF_CLI_DEFAULT_COMPUTE_PLANE_STACK"); v != "" {
		return v
	}
	return defaultComputePlaneStackOCI
}

// defaultControlPlaneStackOCI is set via -ldflags '-X nvcf-cli/cmd.defaultControlPlaneStackOCI=oci://...'.
// Empty in dev builds; the user must pass --control-plane-stack=.
var defaultControlPlaneStackOCI = ""

// defaultComputePlaneStackOCI is set via -ldflags '-X nvcf-cli/cmd.defaultComputePlaneStackOCI=oci://...'.
// Empty in dev builds; pass --compute-plane-stack=.
var defaultComputePlaneStackOCI = ""
