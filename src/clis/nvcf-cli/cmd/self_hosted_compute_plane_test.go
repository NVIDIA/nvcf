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
	"nvcf-cli/internal/selfhosted/controlplaneprofile"
	"nvcf-cli/internal/selfhosted/reachability"
)

func resetComputePlaneFlags(t *testing.T) {
	t.Helper()
	selfHostedEnv = "local"
	selfHostedStack = ""
	selfHostedNoApply = false
	selfHostedICMSURL = ""
	selfHostedControlPlaneContext = ""
	selfHostedComputePlaneContext = ""
	prevRuntimeResolver := resolveSelfHostedHelmRuntimeMode
	resolveSelfHostedHelmRuntimeMode = func(context.Context) (selfhosted.HelmRuntimeMode, error) {
		return selfhosted.HelmRuntimeHelm3Legacy, nil
	}
	prevReachabilityCheck := computePlaneRegisterReachabilityCheck
	computePlaneRegisterReachabilityCheck = func(context.Context, reachability.CheckRequest) error {
		return nil
	}
	t.Cleanup(func() {
		selfHostedEnv = "local"
		selfHostedStack = ""
		selfHostedNoApply = false
		selfHostedICMSURL = ""
		selfHostedControlPlaneContext = ""
		selfHostedComputePlaneContext = ""
		computePlaneInstallValues = ""
		computePlaneInstallKubeContext = ""
		computePlaneInstallClusterName = ""
		computePlaneInstallNCAID = "nvcf-default"
		computePlaneRegisterDryRun = false
		computePlaneRegisterProfile = ""
		computePlaneRegisterClusterName = ""
		computePlaneRegisterKubeContext = ""
		computePlaneRegisterRegion = "us-west-1"
		resolveSelfHostedHelmRuntimeMode = prevRuntimeResolver
		computePlaneRegisterReachabilityCheck = prevReachabilityCheck
	})
}

func TestComputePlaneInstallRequiresValues(t *testing.T) {
	resetComputePlaneFlags(t)

	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "install",
		"--stack", t.TempDir(),
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--values is required")
}

func TestComputePlaneInstallTemplatesUserValuesFile(t *testing.T) {
	resetComputePlaneFlags(t)

	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "helmfile-nvca-operator.yaml.gotmpl"), []byte("releases: []\n"), 0o644))

	valuesFile := filepath.Join(t.TempDir(), "custom-nvca-values.yaml")
	require.NoError(t, os.WriteFile(valuesFile, []byte("clusterName: gpu-from-values\nncaId: nca-from-values\n"), 0o644))

	fakeBin := installFakeComputePlaneHelmfile(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "install",
		"--stack", stackDir,
		"--values", valuesFile,
		"--kube-context", "gpu-context",
		"--no-apply",
	})

	require.NoError(t, rootCmd.Execute())
	out := stdout.String()
	assert.Contains(t, out, "verb=template")
	assert.Contains(t, out, "arg=--kube-context=gpu-context")
	assert.Contains(t, out, "env:NVCF_NVCA_VALUES_FILE="+valuesFile)
	assert.Contains(t, out, "env:CLUSTER_NAME=gpu-from-values")
	assert.Contains(t, out, "env:NCA_ID=nca-from-values")
	assert.Contains(t, out, "arg="+filepath.Join(stackDir, "helmfile-nvca-operator.yaml.gotmpl"))
	assert.FileExists(t, fakeBin)
}

func TestComputePlaneInstallAppliesByDefault(t *testing.T) {
	resetComputePlaneFlags(t)

	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "helmfile-nvca-operator.yaml.gotmpl"), []byte("releases: []\n"), 0o644))

	valuesFile := filepath.Join(t.TempDir(), "gpu-a-nvca-values.yaml")
	require.NoError(t, os.WriteFile(valuesFile, []byte("clusterID: id-a\nclusterGroupID: group-a\n"), 0o644))

	installFakeComputePlaneHelmfile(t)

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "install",
		"--stack", stackDir,
		"--values", valuesFile,
	})

	require.NoError(t, rootCmd.Execute())
	out := stdout.String()
	assert.Contains(t, out, "verb=apply")
	assert.Contains(t, out, "env:NVCF_NVCA_VALUES_FILE="+valuesFile)
	assert.Contains(t, out, "env:CLUSTER_NAME=gpu-a")
}

func TestComputePlaneRegisterDryRunPrintsClusterIdentity(t *testing.T) {
	resetComputePlaneFlags(t)

	profileFile := writeTestControlPlaneProfile(t, "cp-cluster")

	prevFetcher := fetchClusterIdentity
	t.Cleanup(func() { fetchClusterIdentity = prevFetcher })
	fetchClusterIdentity = func(_ context.Context, kctx string) (string, string, string, error) {
		assert.Equal(t, "gpu-context", kctx)
		return "https://k8s.example/issuer", `{"keys":[{"kid":"key-1"}]}`, "psat", nil
	}

	prevClientFactory := newClusterClientForSelfHosted
	t.Cleanup(func() { newClusterClientForSelfHosted = prevClientFactory })
	newClusterClientForSelfHosted = func(string) (selfhosted.ClusterClient, error) {
		t.Fatal("dry-run must not construct a SIS cluster client")
		return nil, nil
	}

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "register",
		"--dry-run",
		"--control-plane-profile", profileFile,
		"--cluster-name", "gpu-a",
		"--kube-context", "gpu-context",
		"--region", "eu-west-1",
	})

	require.NoError(t, rootCmd.Execute())
	out := stdout.String()
	assert.Contains(t, out, "dryRun: true")
	assert.Contains(t, out, "clusterName: gpu-a")
	assert.Contains(t, out, "region: eu-west-1")
	assert.Contains(t, out, "endpointScope: compute-reachable")
	assert.Contains(t, out, "icmsURL: https://sis.example.test")
	assert.Contains(t, out, "oidcIssuer: https://k8s.example/issuer")
	assert.Contains(t, out, "identitySource: psat")
	assert.Contains(t, out, "sisMutation: skipped")
}

func TestComputePlaneRegisterDryRunUsesInClusterScopeForControlPlaneCluster(t *testing.T) {
	resetComputePlaneFlags(t)

	profileFile := writeTestControlPlaneProfile(t, "cp-cluster")

	prevFetcher := fetchClusterIdentity
	t.Cleanup(func() { fetchClusterIdentity = prevFetcher })
	fetchClusterIdentity = func(context.Context, string) (string, string, string, error) {
		return "https://k8s.example/issuer", `{"keys":[]}`, "", nil
	}

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "register",
		"--dry-run",
		"--control-plane-profile", profileFile,
		"--cluster-name", "cp-cluster",
	})

	require.NoError(t, rootCmd.Execute())
	out := stdout.String()
	assert.Contains(t, out, "endpointScope: in-cluster")
	assert.Contains(t, out, "icmsURL: http://api.sis.svc.cluster.local:8080")
	assert.Contains(t, out, "identitySource: psat")
}

func TestComputePlaneRegisterDryRunRunsReachabilityCheck(t *testing.T) {
	resetComputePlaneFlags(t)

	profileFile := writeTestControlPlaneProfile(t, "cp-cluster")

	prevFetcher := fetchClusterIdentity
	t.Cleanup(func() { fetchClusterIdentity = prevFetcher })
	fetchClusterIdentity = func(context.Context, string) (string, string, string, error) {
		return "https://k8s.example/issuer", `{"keys":[]}`, "psat", nil
	}

	var got reachability.CheckRequest
	computePlaneRegisterReachabilityCheck = func(_ context.Context, req reachability.CheckRequest) error {
		got = req
		return nil
	}

	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "register",
		"--dry-run",
		"--control-plane-profile", profileFile,
		"--cluster-name", "gpu-a",
	})

	require.NoError(t, rootCmd.Execute())
	assert.Equal(t, "gpu-a", got.TargetClusterName)
	assert.Equal(t, "https://sis.example.test", got.ICMSURL)
	assert.Equal(t, "https://reval.example.test", got.ReValURL)
	assert.Equal(t, "tls://nats.example.test:4222", got.NATSURL)
	assert.Equal(t, "sis.example.test", got.SISHost)
	assert.Equal(t, "reval.example.test", got.ReValHost)
	assert.False(t, got.ProbeHTTP)
}

func TestComputePlaneRegisterDryRunProbesHTTPForNonLocalComputeReachable(t *testing.T) {
	resetComputePlaneFlags(t)
	selfHostedEnv = "prd"

	profileFile := writeTestControlPlaneProfile(t, "cp-cluster")

	prevFetcher := fetchClusterIdentity
	t.Cleanup(func() { fetchClusterIdentity = prevFetcher })
	fetchClusterIdentity = func(context.Context, string) (string, string, string, error) {
		return "https://k8s.example/issuer", `{"keys":[]}`, "psat", nil
	}

	var got reachability.CheckRequest
	computePlaneRegisterReachabilityCheck = func(_ context.Context, req reachability.CheckRequest) error {
		got = req
		return nil
	}

	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "register",
		"--dry-run",
		"--control-plane-profile", profileFile,
		"--cluster-name", "gpu-a",
	})

	require.NoError(t, rootCmd.Execute())
	assert.True(t, got.ProbeHTTP)
}

func TestComputePlaneRegisterDryRunStopsOnReachabilityFailure(t *testing.T) {
	resetComputePlaneFlags(t)

	profileFile := writeTestControlPlaneProfile(t, "cp-cluster")

	fetchCalls := 0
	prevFetcher := fetchClusterIdentity
	t.Cleanup(func() { fetchClusterIdentity = prevFetcher })
	fetchClusterIdentity = func(context.Context, string) (string, string, string, error) {
		fetchCalls++
		return "", "", "", nil
	}

	computePlaneRegisterReachabilityCheck = func(context.Context, reachability.CheckRequest) error {
		return assert.AnError
	}

	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "register",
		"--dry-run",
		"--control-plane-profile", profileFile,
		"--cluster-name", "gpu-a",
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 0, fetchCalls, "identity discovery must not run after reachability failure")
}

func TestComputePlaneRegisterCallsSISAfterValidation(t *testing.T) {
	resetComputePlaneFlags(t)

	profileFile := writeTestControlPlaneProfile(t, "cp-cluster")

	prevFetcher := fetchClusterIdentity
	t.Cleanup(func() { fetchClusterIdentity = prevFetcher })
	fetchClusterIdentity = func(_ context.Context, kctx string) (string, string, string, error) {
		assert.Equal(t, "gpu-context", kctx)
		return "https://k8s.example/issuer", `{"keys":[{"kid":"key-1"}]}`, "psat", nil
	}

	var gotReachability reachability.CheckRequest
	computePlaneRegisterReachabilityCheck = func(_ context.Context, req reachability.CheckRequest) error {
		gotReachability = req
		return nil
	}

	fakeCC := &fakeClusterClient{resp: &selfhosted.RegisterResponse{ClusterID: "cluster-id", ClusterGroupID: "group-id"}}
	var gotSISURL string
	prevClientFactory := newClusterClientForSelfHosted
	t.Cleanup(func() { newClusterClientForSelfHosted = prevClientFactory })
	newClusterClientForSelfHosted = func(sisURL string) (selfhosted.ClusterClient, error) {
		gotSISURL = sisURL
		return fakeCC, nil
	}

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "--icms-url", "https://sis-admin.example.test", "compute-plane", "register",
		"--control-plane-profile", profileFile,
		"--cluster-name", "gpu-a",
		"--kube-context", "gpu-context",
		"--region", "eu-west-1",
	})

	require.NoError(t, rootCmd.Execute())
	assert.Equal(t, "https://sis-admin.example.test", gotSISURL)
	assert.Equal(t, "https://sis-admin.example.test", gotReachability.ICMSURL)
	assert.Equal(t, 1, fakeCC.registerCalls)
	assert.Equal(t, "gpu-a", fakeCC.lastRegisterRequest.ClusterName)
	assert.Equal(t, "nvcf-default", fakeCC.lastRegisterRequest.NCAID)
	assert.Equal(t, "eu-west-1", fakeCC.lastRegisterRequest.Region)
	assert.Equal(t, "https://k8s.example/issuer", fakeCC.lastRegisterRequest.OIDCIssuer)
	assert.Equal(t, `{"keys":[{"kid":"key-1"}]}`, fakeCC.lastRegisterRequest.JWKS)

	out := stdout.String()
	assert.Contains(t, out, "dryRun: false")
	assert.Contains(t, out, "icmsURL: https://sis-admin.example.test")
	assert.Contains(t, out, "clusterID: cluster-id")
	assert.Contains(t, out, "clusterGroupID: group-id")
	assert.Contains(t, out, "sisMutation: completed")
	assert.Contains(t, out, "valuesWrite: skipped")
}

func TestComputePlaneRegisterStopsBeforeSISOnReachabilityFailure(t *testing.T) {
	resetComputePlaneFlags(t)

	profileFile := writeTestControlPlaneProfile(t, "cp-cluster")

	computePlaneRegisterReachabilityCheck = func(context.Context, reachability.CheckRequest) error {
		return assert.AnError
	}

	prevClientFactory := newClusterClientForSelfHosted
	t.Cleanup(func() { newClusterClientForSelfHosted = prevClientFactory })
	newClusterClientForSelfHosted = func(string) (selfhosted.ClusterClient, error) {
		t.Fatal("SIS client must not be constructed after reachability failure")
		return nil, nil
	}

	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "register",
		"--control-plane-profile", profileFile,
		"--cluster-name", "gpu-a",
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
}

func installFakeComputePlaneHelmfile(t *testing.T) string {
	t.Helper()
	fakeBin := filepath.Join(t.TempDir(), "helmfile")
	body := `#!/bin/sh
last=
for arg in "$@"; do
  printf 'arg=%s\n' "$arg"
  last="$arg"
done
printf 'verb=%s\n' "$last"
printf 'env:NVCF_NVCA_VALUES_FILE=%s\n' "$NVCF_NVCA_VALUES_FILE"
printf 'env:CLUSTER_NAME=%s\n' "$CLUSTER_NAME"
printf 'env:NCA_ID=%s\n' "$NCA_ID"
`
	require.NoError(t, os.WriteFile(fakeBin, []byte(body), 0o755))
	t.Setenv("PATH", filepath.Dir(fakeBin)+":"+os.Getenv("PATH"))
	return fakeBin
}

func writeTestControlPlaneProfile(t *testing.T, controlPlaneCluster string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control-plane-control-plane-profile.yaml")
	require.NoError(t, controlplaneprofile.WriteFile(path, controlplaneprofile.ControlPlaneProfile{
		APIVersion: controlplaneprofile.APIVersion,
		Kind:       controlplaneprofile.Kind,
		ControlPlane: controlplaneprofile.ControlPlane{
			ClusterName: controlPlaneCluster,
			NCAID:       "nvcf-default",
			Region:      "us-west-1",
			Endpoints: controlplaneprofile.Endpoints{
				InCluster: controlplaneprofile.EndpointScope{
					ICMSURL:  "http://api.sis.svc.cluster.local:8080",
					ReValURL: "http://reval.nvcf.svc.cluster.local:8080",
					NATSURL:  "nats://nats.nats-system.svc.cluster.local:4222",
				},
				ComputeReachable: controlplaneprofile.EndpointScope{
					ICMSURL:  "https://sis.example.test",
					ReValURL: "https://reval.example.test",
					NATSURL:  "tls://nats.example.test:4222",
				},
			},
			Gateway: controlplaneprofile.Gateway{
				HTTPURL: "https://api.example.test",
				GRPCURL: "api.example.test:10081",
			},
			Hosts: controlplaneprofile.Hosts{
				API:        "api.example.test",
				APIKeys:    "api-keys.example.test",
				SIS:        "sis.example.test",
				ReVal:      "reval.example.test",
				NATS:       "nats.example.test",
				Invocation: "invocation.example.test",
			},
		},
	}))
	return path
}
