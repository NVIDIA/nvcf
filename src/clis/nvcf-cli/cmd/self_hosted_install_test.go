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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted"
)

// resetInstallFlags restores the install command flag vars to their zero
// values between tests that share rootCmd. Cobra parses flags into package-
// level vars which persist across sequential test executions.
func resetInstallFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		installControlPlane = false
		installComputePlane = false
		installClusterName = ""
	})
}

func TestSelfHostedInstall_ControlPlane_NoApply(t *testing.T) {
	resetInstallFlags(t)
	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "global.yaml.gotmpl"), []byte("# stub\n"), 0o644))

	// Provide a fake helmfile that emits a stub manifest.
	fakeBin := filepath.Join(t.TempDir(), "helmfile")
	require.NoError(t, os.WriteFile(fakeBin,
		[]byte("#!/bin/sh\nprintf 'apiVersion: v1\\nkind: ConfigMap\\nmetadata:\\n  name: from-fake\\n'\n"),
		0o755))
	t.Setenv("PATH", filepath.Dir(fakeBin)+":"+os.Getenv("PATH"))

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetArgs([]string{
		"self-hosted", "install", "--control-plane",
		"--stack", stackDir,
		"--no-apply",
	})
	require.NoError(t, rootCmd.Execute())

	assert.Contains(t, stdout.String(), "kind: ConfigMap")
}

func TestSelfHostedInstall_ComputePlane_RegistersAndRenders(t *testing.T) {
	resetInstallFlags(t)
	// Inject fake cluster client.
	prevClientFactory := newClusterClientForSelfHosted
	t.Cleanup(func() { newClusterClientForSelfHosted = prevClientFactory })
	fakeCC := &fakeClusterClient{resp: &selfhosted.RegisterResponse{ClusterID: "id-A", ClusterGroupID: "grp-A"}}
	newClusterClientForSelfHosted = func(string) (selfhosted.ClusterClient, error) { return fakeCC, nil }

	// Inject fake JWKS fetcher.
	prevFetcher := fetchClusterIdentity
	t.Cleanup(func() { fetchClusterIdentity = prevFetcher })
	fetchClusterIdentity = func(context.Context, string) (string, string, string, error) {
		return "https://k8s.example/.well-known/oidc", `{"keys":[]}`, "psat", nil
	}

	// Provide a fake helmfile that echoes CLUSTER_NAME as a ConfigMap name.
	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	fakeBin := filepath.Join(t.TempDir(), "helmfile")
	require.NoError(t, os.WriteFile(fakeBin,
		[]byte("#!/bin/sh\nprintf 'apiVersion: v1\\nkind: ConfigMap\\nmetadata:\\n  name: %s\\n' \"$CLUSTER_NAME\"\n"),
		0o755))
	t.Setenv("PATH", filepath.Dir(fakeBin)+":"+os.Getenv("PATH"))

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetArgs([]string{"self-hosted", "install", "--compute-plane", "--cluster-name=ncp-A", "--stack", stackDir, "--no-apply"})
	require.NoError(t, rootCmd.Execute())

	assert.Equal(t, 1, fakeCC.registerCalls)
	assert.Contains(t, stdout.String(), "name: ncp-A")
}

func TestComputePlaneTarget_BundledStack(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "helmfile.d"), 0o755))

	helmfileFile, selector := computePlaneTarget(dir)
	assert.Equal(t, "", helmfileFile, "bundled layout should leave HelmfileFile empty")
	assert.Equal(t, "release-group=workers", selector, "bundled layout should filter by release-group")
}

func TestComputePlaneTarget_MultiClusterSplit(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "helmfile-nvca-operator.yaml.gotmpl"), []byte("releases: []\n"), 0o644))

	helmfileFile, selector := computePlaneTarget(dir)
	assert.Equal(t, "helmfile-nvca-operator.yaml.gotmpl", helmfileFile)
	assert.Equal(t, "", selector, "split layout should not narrow with a selector")
}

// fakeClusterClient is a test double for selfhosted.ClusterClient.
type fakeClusterClient struct {
	registerCalls int
	resp          *selfhosted.RegisterResponse
}

func (f *fakeClusterClient) RegisterCluster(_ context.Context, _ selfhosted.RegisterRequest) (*selfhosted.RegisterResponse, error) {
	f.registerCalls++
	return f.resp, nil
}

func (f *fakeClusterClient) Close() error { return nil }
