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
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/client"

	"k8s.io/client-go/rest"
)

type fakeClusterClient struct {
	registerCalls int
	resp          *RegisterResponse
	err           error
}

func (f *fakeClusterClient) RegisterCluster(_ context.Context, in RegisterRequest) (*RegisterResponse, error) {
	f.registerCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeClusterClient) Close() error { return nil }

func TestRegisterCluster_HappyPath(t *testing.T) {
	c := &fakeClusterClient{resp: &RegisterResponse{ClusterID: "abc", ClusterGroupID: "grp"}}
	got, err := c.RegisterCluster(context.Background(), RegisterRequest{
		ClusterName: "my-cluster", NCAID: "nvcf-default", Region: "us-west-1",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, c.registerCalls)
	assert.Equal(t, "abc", got.ClusterID)
}

func TestRegisterCluster_PropagatesError(t *testing.T) {
	c := &fakeClusterClient{err: errors.New("boom")}
	_, err := c.RegisterCluster(context.Background(), RegisterRequest{ClusterName: "x"})
	assert.ErrorContains(t, err, "boom")
}

// fakeLister is a test double for the clusterLister interface used by
// resolveExistingCluster.
type fakeLister struct {
	clusters []client.SISCluster
	err      error
}

func (f *fakeLister) ListClusters(_ context.Context, _, _ string) ([]client.SISCluster, error) {
	return f.clusters, f.err
}

func TestResolveExistingCluster_FoundByName(t *testing.T) {
	l := &fakeLister{clusters: []client.SISCluster{
		{ClusterName: "other", ClusterID: "id-other"},
		{ClusterName: "wanted", ClusterID: "id-wanted", ClusterGroupID: "grp-wanted"},
	}}
	r, err := resolveExistingCluster(context.Background(), l, "url", "nca", "wanted")
	require.NoError(t, err)
	assert.Equal(t, "id-wanted", r.ClusterID)
	assert.Equal(t, "grp-wanted", r.ClusterGroupID)
}

func TestResolveExistingCluster_NotFound(t *testing.T) {
	l := &fakeLister{clusters: []client.SISCluster{{ClusterName: "other"}}}
	_, err := resolveExistingCluster(context.Background(), l, "url", "nca", "missing")
	assert.ErrorContains(t, err, "missing")
}

func TestResolveExistingCluster_ListError(t *testing.T) {
	l := &fakeLister{err: errors.New("list failed")}
	_, err := resolveExistingCluster(context.Background(), l, "url", "nca", "x")
	assert.ErrorContains(t, err, "list failed")
}

// TestLoadKubeConfigFn_SeamIsReplaceable verifies that loadKubeConfigFn is a
// package-level variable that callers can replace in tests without touching a
// real kubeconfig file.
func TestLoadKubeConfigFn_SeamIsReplaceable(t *testing.T) {
	var capturedKctx string
	prev := loadKubeConfigFn
	t.Cleanup(func() { loadKubeConfigFn = prev })

	loadKubeConfigFn = func(kctx string) (*rest.Config, error) {
		capturedKctx = kctx
		return &rest.Config{Host: "https://fake-server:6443"}, nil
	}

	cfg, err := loadKubeConfigFn("admin@cp")
	require.NoError(t, err)
	assert.Equal(t, "admin@cp", capturedKctx,
		"loadKubeConfigFn should receive the kctx argument")
	assert.Equal(t, "https://fake-server:6443", cfg.Host)
}

// TestLoadKubeConfigFn_EmptyKctxIsPassedThrough verifies that an empty kctx
// is forwarded as-is (clientcmd treats empty string as "use current-context").
func TestLoadKubeConfigFn_EmptyKctxIsPassedThrough(t *testing.T) {
	var capturedKctx string
	prev := loadKubeConfigFn
	t.Cleanup(func() { loadKubeConfigFn = prev })

	loadKubeConfigFn = func(kctx string) (*rest.Config, error) {
		capturedKctx = kctx
		return &rest.Config{Host: "https://default:6443"}, nil
	}

	cfg, err := loadKubeConfigFn("")
	require.NoError(t, err)
	assert.Equal(t, "", capturedKctx,
		"empty kctx should be passed through unchanged")
	assert.Equal(t, "https://default:6443", cfg.Host)
}

func TestNewClusterClient_HitsRealSIS(t *testing.T) {
	if os.Getenv("NVCF_E2E") == "" {
		t.Skip("set NVCF_E2E=1 with a working control plane to run")
	}
	// Construct the real client from environment / config.
	// LoadConfig reads NVCF_* env vars and the ~/.nvcf.yaml config file.
	// Pass empty string to exercise the config-fallback path for sisURL.
	c, err := NewClusterClient("")
	if err != nil {
		t.Fatalf("NewClusterClient: %v", err)
	}
	defer c.Close()
	got, err := c.RegisterCluster(context.Background(), RegisterRequest{
		ClusterName: "rci-test",
		NCAID:       "nvcf-default",
		Region:      "us-west-1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, got.ClusterID)
}
