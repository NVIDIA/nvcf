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

package selfhosted

import (
	"context"
	"fmt"
	"strings"

	"nvcf-cli/internal/client"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// RegisterRequest carries the fields needed to register a cluster with SIS.
// NCAID defaults to the config's ClientID when empty.
type RegisterRequest struct {
	ClusterName string
	NCAID       string
	Region      string

	// Compute-plane OIDC identity. Caller supplies these — typically by
	// calling kubectl + the K8s API server's /.well-known/openid-configuration
	// endpoint before construction (see cmd/cluster_registration.go's
	// fetchClusterJWKS helper). Empty values produce an unauthable cluster
	// in SIS, so M4 Task 14 must populate them.
	JWKS           string
	OIDCIssuer     string
	IdentitySource string // "psat" | "spire" — defaults handled by caller
}

// RegisterResponse is the distilled result of a successful cluster registration.
type RegisterResponse struct {
	ClusterID      string
	ClusterGroupID string
}

// ClusterClient abstracts the existing nvcf-cli cluster register call.
// Production callers wire in the real client from internal/client/clusters.go;
// tests pass a fake.
type ClusterClient interface {
	DeleteCluster(ctx context.Context, clusterID string) error
	DeleteClusterByName(ctx context.Context, ncaID, name string) (int, error)
	RegisterCluster(ctx context.Context, req RegisterRequest) (*RegisterResponse, error)
	Close() error
}

// clusterLister is a narrow seam used by resolveExistingCluster so the
// recovery-path logic can be unit-tested independently of the full adapter.
type clusterLister interface {
	ListClusters(ctx context.Context, sisURL, ncaID string) ([]client.SISCluster, error)
}

// resolveExistingCluster handles the --ignore-existing recovery branch: the
// cluster is already registered so we look it up by name and return its IDs.
func resolveExistingCluster(ctx context.Context, l clusterLister, sisURL, ncaID, name string) (*RegisterResponse, error) {
	list, err := l.ListClusters(ctx, sisURL, ncaID)
	if err != nil {
		return nil, err
	}
	for _, cl := range list {
		if cl.ClusterName == name {
			return &RegisterResponse{ClusterID: cl.ClusterID, ClusterGroupID: cl.ClusterGroupID}, nil
		}
	}
	return nil, fmt.Errorf("cluster %q reported as already-existing but not found in list", name)
}

// clusterClientAdapter wraps *client.Client to satisfy ClusterClient.
// It mirrors the --ignore-existing logic from cmd/cluster_registration.go so
// the install --compute-plane flow gets idempotent register semantics without
// re-implementing auth/HTTP/retry concerns.
type clusterClientAdapter struct {
	inner  *client.Client
	sisURL string
	cfg    *client.Config
}

// NewClusterClient constructs a ClusterClient backed by the production
// internal/client.Client. If sisURL is empty it falls back to
// config.BaseHTTPURL loaded from the standard Viper sources (env vars, config
// file, state file) — the same path used by every other CLI subcommand.
func NewClusterClient(sisURL string) (ClusterClient, error) {
	cfg, err := client.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load client config: %w", err)
	}
	c, err := client.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}
	url := sisURL
	if url == "" {
		url = cfg.BaseHTTPURL
	}
	return &clusterClientAdapter{inner: c, sisURL: url, cfg: cfg}, nil
}

// Close releases HTTP resources held by the underlying client.
func (a *clusterClientAdapter) Close() error {
	return a.inner.Close()
}

// RegisterCluster implements ClusterClient. It translates the narrow
// RegisterRequest into the full client.RegisterClusterRequest, calls the SIS
// endpoint, and applies --ignore-existing semantics: if the cluster already
// exists the adapter looks it up and returns its IDs instead of failing.
func (a *clusterClientAdapter) RegisterCluster(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	ncaID := req.NCAID
	if ncaID == "" {
		ncaID = a.cfg.ClientID
	}

	apiReq := &client.RegisterClusterRequest{
		ClusterName:      req.ClusterName,
		ClusterGroupName: req.ClusterName,
		NcaID:            ncaID,
		CloudProvider:    "ON-PREM",
		Region:           req.Region,
		NvcaVersion:      "0.0.0",
		Capabilities:     []string{"DynamicGPUDiscovery"},
	}
	if req.JWKS != "" {
		apiReq.JWKS = &req.JWKS
	}
	if req.OIDCIssuer != "" {
		apiReq.OIDCIssuer = &req.OIDCIssuer
	}

	resp, err := a.inner.RegisterCluster(ctx, a.sisURL, ncaID, apiReq)
	if err != nil {
		// Idempotent semantics: if cluster already exists, look it up.
		if strings.Contains(err.Error(), "already exists") {
			return resolveExistingCluster(ctx, a.inner, a.sisURL, ncaID, req.ClusterName)
		}
		return nil, fmt.Errorf("failed to register cluster: %w", err)
	}

	return &RegisterResponse{
		ClusterID:      resolveClusterID(resp),
		ClusterGroupID: resolveClusterGroupID(resp),
	}, nil
}

// DeleteClusterByName removes every SIS cluster row matching name. It is used
// by one-click reruns, where the local GPU cluster must be registered from
// scratch each time after the compute plane is torn down.
func (a *clusterClientAdapter) DeleteClusterByName(ctx context.Context, ncaID, name string) (int, error) {
	if ncaID == "" {
		ncaID = a.cfg.ClientID
	}
	list, err := a.inner.ListClusters(ctx, a.sisURL, ncaID)
	if err != nil {
		return 0, fmt.Errorf("list clusters: %w", err)
	}
	deleted := 0
	for _, cl := range list {
		if cl.ClusterName != name && cl.ClusterID != name {
			continue
		}
		if cl.ClusterID == "" {
			return deleted, fmt.Errorf("cluster %q has empty cluster ID", name)
		}
		if err := a.inner.DeleteCluster(ctx, a.sisURL, cl.ClusterID); err != nil {
			if clusterDeleteNotFound(err) {
				continue
			}
			return deleted, fmt.Errorf("delete cluster %q (%s): %w", name, cl.ClusterID, err)
		}
		deleted++
	}
	return deleted, nil
}

func (a *clusterClientAdapter) DeleteCluster(ctx context.Context, clusterID string) error {
	if clusterID == "" {
		return nil
	}
	if err := a.inner.DeleteCluster(ctx, a.sisURL, clusterID); err != nil {
		if clusterDeleteNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete cluster %s: %w", clusterID, err)
	}
	return nil
}

func clusterDeleteNotFound(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "not found") || strings.Contains(msg, "404")
}

// resolveClusterGroupID extracts the cluster-group ID from whichever field the
// SIS response populated (nested vs. flat), mirroring registeredClusterIDs in
// cmd/cluster_registration.go.
func resolveClusterGroupID(resp *client.RegisterClusterResponse) string {
	if resp.ClusterGroup.ID != "" {
		return resp.ClusterGroup.ID
	}
	return resp.ClusterGroupID
}

// resolveClusterID extracts the cluster ID from the SIS response.
func resolveClusterID(resp *client.RegisterClusterResponse) string {
	if len(resp.ClusterGroup.Clusters) > 0 {
		return resp.ClusterGroup.Clusters[0].ID
	}
	return resp.ClusterID
}

// loadKubeConfigFn is a package-level seam that unit tests can replace to
// avoid touching a real kubeconfig file.
var loadKubeConfigFn = loadKubeConfig

// loadKubeConfig builds a *rest.Config for the given kubeconfig context.
// When kctx is empty the kubeconfig's current-context is used, preserving
// single-cluster behavior. The loading-rules chain follows the same priority
// as kubectl: KUBECONFIG env var → ~/.kube/config.
func loadKubeConfig(kctx string) (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if kctx != "" {
		overrides.CurrentContext = kctx
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, overrides,
	).ClientConfig()
}
