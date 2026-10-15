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
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/controlplaneprofile"
	"nvcf-cli/internal/selfhosted/reachability"
)

func resetComputePlaneFlags(t *testing.T) {
	t.Helper()
	selfHostedEnv = "local"
	selfHostedComputePlaneStack = ""
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
		selfHostedComputePlaneStack = ""
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
		computePlaneRegisterOutput = ""
		resolveSelfHostedHelmRuntimeMode = prevRuntimeResolver
		computePlaneRegisterReachabilityCheck = prevReachabilityCheck
	})
}

func TestComputePlaneInstallRequiresValues(t *testing.T) {
	resetComputePlaneFlags(t)

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "install",
		"--compute-plane-stack", t.TempDir(),
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--values is required")
}

func TestComputePlaneInstallTemplatesUserValuesFile(t *testing.T) {
	resetComputePlaneFlags(t)

	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))

	valuesFile := filepath.Join(t.TempDir(), "custom-nvca-values.yaml")
	require.NoError(t, os.WriteFile(valuesFile, []byte("clusterName: gpu-from-values\nncaId: nca-from-values\n"), 0o644))

	fakeBin := installFakeComputePlaneHelmfile(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "install",
		"--compute-plane-stack", stackDir,
		"--values", valuesFile,
		"--kube-context", "gpu-context",
		"--no-apply",
	})

	require.NoError(t, rootCmd.Execute())
	out := stdout.String()
	assert.Contains(t, out, "verb=template")
	assert.Contains(t, out, "arg=--kube-context=gpu-context")
	assert.Contains(t, out, "env:CLUSTER_NAME=gpu-from-values")
	assert.Contains(t, out, "env:NCA_ID=nca-from-values")
	assert.Contains(t, out, "arg="+stackDir)
	assert.FileExists(t, fakeBin)
}

func TestComputePlaneInstallAppliesByDefault(t *testing.T) {
	resetComputePlaneFlags(t)

	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))

	valuesFile := filepath.Join(t.TempDir(), "gpu-a-nvca-values.yaml")
	require.NoError(t, os.WriteFile(valuesFile, []byte("clusterID: id-a\nclusterGroupID: group-a\n"), 0o644))

	installFakeComputePlaneHelmfile(t)

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "install",
		"--compute-plane-stack", stackDir,
		"--values", valuesFile,
	})

	require.NoError(t, rootCmd.Execute())
	out := stdout.String()
	assert.Contains(t, out, "verb=apply")
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

	prevClientFactory := newClusterClientForSelfHostedWithTrust
	t.Cleanup(func() { newClusterClientForSelfHostedWithTrust = prevClientFactory })
	newClusterClientForSelfHostedWithTrust = func(string, *tls.Config) (selfhosted.ClusterClient, error) {
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
	// Even when the in-cluster scope is selected (single-cluster register
	// targeting the control-plane cluster itself), the CLI cannot dial the
	// in-cluster service DNS. computePlaneRegisterICMSURL must prefer the
	// compute-reachable URL so the SIS call goes through the gateway.
	assert.Contains(t, out, "icmsURL: https://sis.example.test")
	assert.Contains(t, out, "identitySource: psat")
}

func TestComputePlaneRegisterICMSURL_PrefersConfigFile(t *testing.T) {
	resetComputePlaneFlags(t)
	configureSelfHostedTestConfig(t, `
icms_url: "http://configured-sis.example:8080"
`)

	profile := controlplaneprofile.ControlPlaneProfile{
		ControlPlane: controlplaneprofile.ControlPlane{
			ClusterName: "cp-cluster",
			Endpoints: controlplaneprofile.Endpoints{
				InCluster: controlplaneprofile.EndpointScope{
					ICMSURL: "http://api.sis.svc.cluster.local:8080",
				},
				ComputeReachable: controlplaneprofile.EndpointScope{
					ICMSURL: "https://sis.gateway.example",
				},
			},
		},
	}
	selected := selfhosted.ControlPlaneProfileEndpointScopeSelection{
		Name:      selfhosted.EndpointScopeInCluster,
		Endpoints: profile.ControlPlane.Endpoints.InCluster,
	}

	got := computePlaneRegisterICMSURL(profile, selected)
	assert.Equal(t, "http://configured-sis.example:8080", got)
}

func TestComputePlaneRegisterICMSURL_FallsBackToComputeReachableWhenInClusterSelected(t *testing.T) {
	resetComputePlaneFlags(t)
	configureSelfHostedTestConfig(t, "")

	profile := controlplaneprofile.ControlPlaneProfile{
		ControlPlane: controlplaneprofile.ControlPlane{
			ClusterName: "cp-cluster",
			Endpoints: controlplaneprofile.Endpoints{
				InCluster: controlplaneprofile.EndpointScope{
					ICMSURL: "http://api.sis.svc.cluster.local:8080",
				},
				ComputeReachable: controlplaneprofile.EndpointScope{
					ICMSURL: "https://sis.gateway.example",
				},
			},
		},
	}
	selected := selfhosted.ControlPlaneProfileEndpointScopeSelection{
		Name:      selfhosted.EndpointScopeInCluster,
		Endpoints: profile.ControlPlane.Endpoints.InCluster,
	}

	got := computePlaneRegisterICMSURL(profile, selected)
	assert.Equal(t, "https://sis.gateway.example", got)
}

func TestComputePlaneRegisterICMSURL_KeepsComputeReachableWhenSelected(t *testing.T) {
	resetComputePlaneFlags(t)
	configureSelfHostedTestConfig(t, "")

	profile := controlplaneprofile.ControlPlaneProfile{
		ControlPlane: controlplaneprofile.ControlPlane{
			ClusterName: "cp-cluster",
			Endpoints: controlplaneprofile.Endpoints{
				InCluster: controlplaneprofile.EndpointScope{
					ICMSURL: "http://api.sis.svc.cluster.local:8080",
				},
				ComputeReachable: controlplaneprofile.EndpointScope{
					ICMSURL: "https://sis.gateway.example",
				},
			},
		},
	}
	selected := selfhosted.ControlPlaneProfileEndpointScopeSelection{
		Name:      selfhosted.EndpointScopeComputeReachable,
		Endpoints: profile.ControlPlane.Endpoints.ComputeReachable,
	}

	got := computePlaneRegisterICMSURL(profile, selected)
	assert.Equal(t, "https://sis.gateway.example", got)
}

func TestComputePlaneRegisterICMSURL_FlagWinsOverProfile(t *testing.T) {
	resetComputePlaneFlags(t)
	configureSelfHostedTestConfig(t, "")
	selfHostedICMSURL = "http://flag-sis.example:8080"

	profile := controlplaneprofile.ControlPlaneProfile{
		ControlPlane: controlplaneprofile.ControlPlane{
			ClusterName: "cp-cluster",
			Endpoints: controlplaneprofile.Endpoints{
				InCluster: controlplaneprofile.EndpointScope{
					ICMSURL: "http://api.sis.svc.cluster.local:8080",
				},
				ComputeReachable: controlplaneprofile.EndpointScope{
					ICMSURL: "https://sis.gateway.example",
				},
			},
		},
	}
	selected := selfhosted.ControlPlaneProfileEndpointScopeSelection{
		Name:      selfhosted.EndpointScopeInCluster,
		Endpoints: profile.ControlPlane.Endpoints.InCluster,
	}

	got := computePlaneRegisterICMSURL(profile, selected)
	assert.Equal(t, "http://flag-sis.example:8080", got)
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

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
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

	stackDir := writeComputePlaneStack(t)
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
	prevClientFactory := newClusterClientForSelfHostedWithTrust
	t.Cleanup(func() { newClusterClientForSelfHostedWithTrust = prevClientFactory })
	newClusterClientForSelfHostedWithTrust = func(sisURL string, _ *tls.Config) (selfhosted.ClusterClient, error) {
		gotSISURL = sisURL
		return fakeCC, nil
	}

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "--icms-url", "https://sis-admin.example.test", "compute-plane", "register",
		"--compute-plane-stack", stackDir,
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
	assert.Contains(t, out, "valuesWrite: completed")
}

func TestComputePlaneRegisterWritesNVCAValuesAndHandoffCommands(t *testing.T) {
	resetComputePlaneFlags(t)

	stackDir := filepath.Join(t.TempDir(), "stack with space")
	writeComputePlaneStackAt(t, stackDir)
	profileFile := writeTestControlPlaneProfile(t, "cp-cluster")

	prevFetcher := fetchClusterIdentity
	t.Cleanup(func() { fetchClusterIdentity = prevFetcher })
	fetchClusterIdentity = func(context.Context, string) (string, string, string, error) {
		return "https://k8s.example/issuer", `{"keys":[{"kid":"key-1"}]}`, "psat", nil
	}

	fakeCC := &fakeClusterClient{resp: &selfhosted.RegisterResponse{ClusterID: "cluster-id", ClusterGroupID: "group-id"}}
	prevClientFactory := newClusterClientForSelfHostedWithTrust
	t.Cleanup(func() { newClusterClientForSelfHostedWithTrust = prevClientFactory })
	newClusterClientForSelfHostedWithTrust = func(string, *tls.Config) (selfhosted.ClusterClient, error) {
		return fakeCC, nil
	}

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "register",
		"--compute-plane-stack", stackDir,
		"--control-plane-profile", profileFile,
		"--cluster-name", "gpu-a",
		"--kube-context", "gpu-context",
		"--region", "eu-west-1",
	})

	require.NoError(t, rootCmd.Execute())

	valuesPath := filepath.Join(stackDir, "out", "gpu-a-register-values.yaml")
	body, err := os.ReadFile(valuesPath)
	require.NoError(t, err)
	values := string(body)
	assert.Contains(t, values, "clusterName: gpu-a")
	assert.Contains(t, values, "clusterID: cluster-id")
	assert.Contains(t, values, "clusterGroupID: group-id")
	assert.Contains(t, values, "ncaID: nvcf-default")
	assert.Contains(t, values, "region: eu-west-1")
	assert.Contains(t, values, "icmsServiceURL: https://sis.example.test")
	assert.Contains(t, values, "icmsServiceHostHeaderOverride: sis.example.test")
	assert.Contains(t, values, "revalServiceURL: https://reval.example.test")
	assert.Contains(t, values, "revalServiceHostHeaderOverride: reval.example.test")
	assert.Contains(t, values, "natsURL: tls://nats.example.test:4222")
	assert.Contains(t, values, "natsHostOverride: nats.example.test")

	out := stdout.String()
	assert.Contains(t, out, "valuesPath: "+valuesPath)
	assert.Contains(t, out, "helm upgrade --install nvca-operator nvcf/helm-nvca-operator --version 1.11.1")
	assert.Contains(t, out, "--values "+shellQuote(valuesPath))
	assert.Contains(t, out, shellCommand("nvcf", "self-hosted", "compute-plane", "install", "--compute-plane-stack", stackDir, "--values", valuesPath, "--kube-context", "gpu-context"))
}

func TestComputePlaneRegisterUsesDefaultStackForValuesHandoff(t *testing.T) {
	resetComputePlaneFlags(t)

	stackDir := writeComputePlaneStack(t)
	t.Setenv("NVCF_CLI_DEFAULT_COMPUTE_PLANE_STACK", stackDir)
	profileFile := writeTestControlPlaneProfile(t, "cp-cluster")

	prevFetcher := fetchClusterIdentity
	t.Cleanup(func() { fetchClusterIdentity = prevFetcher })
	fetchClusterIdentity = func(context.Context, string) (string, string, string, error) {
		return "https://k8s.example/issuer", `{"keys":[]}`, "psat", nil
	}

	fakeCC := &fakeClusterClient{resp: &selfhosted.RegisterResponse{ClusterID: "cluster-id", ClusterGroupID: "group-id"}}
	prevClientFactory := newClusterClientForSelfHostedWithTrust
	t.Cleanup(func() { newClusterClientForSelfHostedWithTrust = prevClientFactory })
	newClusterClientForSelfHostedWithTrust = func(string, *tls.Config) (selfhosted.ClusterClient, error) {
		return fakeCC, nil
	}

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "compute-plane", "register",
		"--control-plane-profile", profileFile,
		"--cluster-name", "gpu-a",
	})

	require.NoError(t, rootCmd.Execute())
	valuesPath := filepath.Join(stackDir, "out", "gpu-a-register-values.yaml")
	assert.FileExists(t, valuesPath)
	assert.NotContains(t, stdout.String(), "--compute-plane-stack")
}

func TestComputePlaneRegisterStopsBeforeSISOnReachabilityFailure(t *testing.T) {
	resetComputePlaneFlags(t)

	profileFile := writeTestControlPlaneProfile(t, "cp-cluster")

	computePlaneRegisterReachabilityCheck = func(context.Context, reachability.CheckRequest) error {
		return assert.AnError
	}

	prevClientFactory := newClusterClientForSelfHostedWithTrust
	t.Cleanup(func() { newClusterClientForSelfHostedWithTrust = prevClientFactory })
	newClusterClientForSelfHostedWithTrust = func(string, *tls.Config) (selfhosted.ClusterClient, error) {
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

func TestTransportTLSAgentConfigValuesSystemOmitsTrustMaterial(t *testing.T) {
	got, err := transportTLSAgentConfigValues(controlplaneprofile.TransportTLS{
		TrustMode:              controlplaneprofile.TrustModeSystem,
		TrustBundleFingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TrustBundlePEM:         "-----BEGIN CERTIFICATE-----\nignored\n-----END CERTIFICATE-----\n",
	})
	require.NoError(t, err)

	require.NotNil(t, got)
	assert.Contains(t, got.MergeConfig, "trustMode: system")
	assert.NotContains(t, got.MergeConfig, "trustBundleFingerprint")
	assert.NotContains(t, got.MergeConfig, "trustBundlePem")
}

func TestTransportTLSAgentConfigValuesBundleIncludesTrustMaterial(t *testing.T) {
	got, err := transportTLSAgentConfigValues(controlplaneprofile.TransportTLS{
		TrustMode:              controlplaneprofile.TrustModeBundle,
		TrustBundleFingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TrustBundlePEM:         "-----BEGIN CERTIFICATE-----\nrendered\n-----END CERTIFICATE-----\n",
	})
	require.NoError(t, err)

	require.NotNil(t, got)
	var mergeConfig map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(got.MergeConfig), &mergeConfig))
	workload, ok := mergeConfig["workload"].(map[string]any)
	require.True(t, ok)
	transportTLS, ok := workload["transportTLS"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, controlplaneprofile.TrustModeBundle, transportTLS["trustMode"])
	assert.Equal(t, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", transportTLS["trustBundleFingerprint"])
	assert.Equal(t, "-----BEGIN CERTIFICATE-----\nrendered\n-----END CERTIFICATE-----\n", transportTLS["trustBundlePem"])
	assert.NotContains(t, transportTLS, "installerImage")
}

func TestComputePlaneChartFromStack(t *testing.T) {
	t.Run("reads chart and version from nvca helmfile", func(t *testing.T) {
		stackDir := writeComputePlaneStack(t)

		chart, version, err := computePlaneChartFromStack(stackDir)
		require.NoError(t, err)
		assert.Equal(t, "nvcf/helm-nvca-operator", chart)
		assert.Equal(t, "1.11.1", version)
	})

	t.Run("ignores non nvca helmfiles", func(t *testing.T) {
		stackDir := t.TempDir()
		helmfileDir := filepath.Join(stackDir, "helmfile.d")
		require.NoError(t, os.MkdirAll(helmfileDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(helmfileDir, "01-core.yaml.gotmpl"), []byte(`
releases:
  - name: api
    chart: nvcf/helm-nvcf-api
    version: 1.2.3
`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(helmfileDir, "02-nvca.yaml.gotmpl"), []byte(`
releases:
  - name: nvca-operator
    chart: "nvcf/helm-nvca-operator"
    version: '1.12.0'
`), 0o644))

		chart, version, err := computePlaneChartFromStack(stackDir)
		require.NoError(t, err)
		assert.Equal(t, "nvcf/helm-nvca-operator", chart)
		assert.Equal(t, "1.12.0", version)
	})

	t.Run("reads chart and version from worker helmfile", func(t *testing.T) {
		stackDir := t.TempDir()
		helmfileDir := filepath.Join(stackDir, "helmfile.d")
		require.NoError(t, os.MkdirAll(helmfileDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(helmfileDir, "04-worker.yaml.gotmpl"), []byte(`
releases:
  - name: nvca-operator
    chart: nvcf/helm-nvca-operator
    version: 1.11.1
`), 0o644))

		chart, version, err := computePlaneChartFromStack(stackDir)
		require.NoError(t, err)
		assert.Equal(t, "nvcf/helm-nvca-operator", chart)
		assert.Equal(t, "1.11.1", version)
	})

	t.Run("skips incomplete nvca file and uses next", func(t *testing.T) {
		stackDir := t.TempDir()
		helmfileDir := filepath.Join(stackDir, "helmfile.d")
		require.NoError(t, os.MkdirAll(helmfileDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(helmfileDir, "01-nvca.yaml.gotmpl"), []byte(`
releases:
  - name: nvca-operator
    chart: nvcf/helm-nvca-operator
`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(helmfileDir, "02-nvca.yaml.gotmpl"), []byte(`
releases:
  - name: nvca-operator
    chart: nvcf/helm-nvca-operator
    version: 2.0.0
`), 0o644))

		chart, version, err := computePlaneChartFromStack(stackDir)
		require.NoError(t, err)
		assert.Equal(t, "nvcf/helm-nvca-operator", chart)
		assert.Equal(t, "2.0.0", version)
	})

	t.Run("errors when helmfile directory is missing", func(t *testing.T) {
		stackDir := t.TempDir()

		chart, version, err := computePlaneChartFromStack(stackDir)
		require.Error(t, err)
		assert.Empty(t, chart)
		assert.Empty(t, version)
		assert.Contains(t, err.Error(), "reading compute-plane helmfile directory")
	})

	t.Run("errors when no parseable nvca chart reference exists", func(t *testing.T) {
		stackDir := t.TempDir()
		helmfileDir := filepath.Join(stackDir, "helmfile.d")
		require.NoError(t, os.MkdirAll(helmfileDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(helmfileDir, "02-nvca.yaml.gotmpl"), []byte(`
releases:
  - name: nvca-operator
    chart: nvcf/helm-nvca-operator
`), 0o644))

		chart, version, err := computePlaneChartFromStack(stackDir)
		require.Error(t, err)
		assert.Empty(t, chart)
		assert.Empty(t, version)
		assert.Contains(t, err.Error(), "compute-plane chart reference not found in stack")
	})
}

func TestReadNVCAValuesMetadata(t *testing.T) {
	t.Run("clean values file decodes successfully", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gpu-a-nvca-values.yaml")
		body := "clusterName: gpu-a\nclusterID: cid-123\nclusterGroupID: cg-456\nncaID: nvcf-default\nregion: us-west-1\n"
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

		meta, err := readNVCAValuesMetadata(path)
		require.NoError(t, err)
		assert.Equal(t, "gpu-a", meta.ClusterName)
		assert.Equal(t, "nvcf-default", meta.NCAID)
	})

	t.Run("lowercase ncaId alias still accepted", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gpu-b-nvca-values.yaml")
		body := "clusterName: gpu-b\nncaId: nca-from-values\n"
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

		meta, err := readNVCAValuesMetadata(path)
		require.NoError(t, err)
		assert.Equal(t, "gpu-b", meta.ClusterName)
		assert.Equal(t, "nca-from-values", meta.NCAID)
	})

	t.Run("generated values with agent block decode successfully", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "gpu-c-register-values.yaml")
		require.NoError(t, writeComputePlaneNVCAValues(computePlaneNVCAValuesRequest{
			Path:           path,
			ClusterName:    "gpu-c",
			NCAID:          "nca-from-generated",
			Region:         "us-west-1",
			IdentitySource: "psat",
			Registration: &selfhosted.RegisterResponse{
				ClusterID:      "cluster-id",
				ClusterGroupID: "group-id",
			},
			RequestRouterAddress: "llm-request-router.nvcf.svc.cluster.local:50071",
		}))

		body, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(body), "agent:")
		require.Contains(t, string(body), "requestRouterAddress: llm-request-router.nvcf.svc.cluster.local:50071")

		meta, err := readNVCAValuesMetadata(path)
		require.NoError(t, err)
		assert.Equal(t, "gpu-c", meta.ClusterName)
		assert.Equal(t, "nca-from-generated", meta.NCAID)
	})

	t.Run("generated values render transport trust through agent config merge", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "gpu-d-register-values.yaml")
		require.NoError(t, writeComputePlaneNVCAValues(computePlaneNVCAValuesRequest{
			Path:           path,
			ClusterName:    "gpu-d",
			NCAID:          "nca-from-generated",
			Region:         "us-west-1",
			IdentitySource: "psat",
			Registration: &selfhosted.RegisterResponse{
				ClusterID:      "cluster-id",
				ClusterGroupID: "group-id",
			},
			TransportTLS: controlplaneprofile.TransportTLS{
				TrustMode:              controlplaneprofile.TrustModeBundle,
				TrustBundleFingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				TrustBundlePEM:         "-----BEGIN CERTIFICATE-----\nrendered\n-----END CERTIFICATE-----\n",
			},
		}))

		body, err := os.ReadFile(path)
		require.NoError(t, err)

		var values map[string]any
		require.NoError(t, yaml.Unmarshal(body, &values))
		selfManaged, ok := values["selfManaged"].(map[string]any)
		require.True(t, ok)
		assert.NotContains(t, selfManaged, "transportTls")

		agentConfig, ok := values["agentConfig"].(map[string]any)
		require.True(t, ok)
		mergeConfig, ok := agentConfig["mergeConfig"].(string)
		require.True(t, ok)

		var merged map[string]any
		require.NoError(t, yaml.Unmarshal([]byte(mergeConfig), &merged))
		workload, ok := merged["workload"].(map[string]any)
		require.True(t, ok)
		transportTLS, ok := workload["transportTLS"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, controlplaneprofile.TrustModeBundle, transportTLS["trustMode"])
		assert.Equal(t, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", transportTLS["trustBundleFingerprint"])
		assert.Equal(t, "-----BEGIN CERTIFICATE-----\nrendered\n-----END CERTIFICATE-----\n", transportTLS["trustBundlePem"])
		assert.NotContains(t, transportTLS, "installerImage")
	})

	t.Run("typo in known field surfaces a decode error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "typo-nvca-values.yaml")
		// cluterID is a typo of clusterID; strict decode must reject it
		// instead of silently producing an empty cluster identity.
		body := "clusterName: gpu-c\ncluterID: cid-typo\nncaID: nvcf-default\n"
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

		_, err := readNVCAValuesMetadata(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cluterID")
	})

	t.Run("missing file returns a read error", func(t *testing.T) {
		_, err := readNVCAValuesMetadata(filepath.Join(t.TempDir(), "missing.yaml"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading values file")
	})
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

func writeComputePlaneStack(t *testing.T) string {
	t.Helper()
	return writeComputePlaneStackAt(t, t.TempDir())
}

func writeComputePlaneStackAt(t *testing.T, stackDir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "helmfile.d", "02-nvca.yaml.gotmpl"), []byte(`
releases:
  - name: nvca-operator
    chart: nvcf/helm-nvca-operator
    version: 1.11.1
`), 0o644))
	return stackDir
}
