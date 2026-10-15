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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted/controlplaneprofile"
	"nvcf-cli/internal/trustbundle"
)

func TestControlPlaneProfileValidateCommandSucceeds(t *testing.T) {
	path := writeControlPlaneProfileFixture(t, validControlPlaneProfileYAML())
	resetControlPlaneProfileValidateCommand(t)

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "control-plane", "profile", "validate",
		"--file", path,
		"--require", "both",
	})

	err := rootCmd.Execute()
	require.NoError(t, err)

	assert.Contains(t, stdout.String(), "control-plane profile is valid")
	assert.Contains(t, stdout.String(), "in-cluster: usable")
	assert.Contains(t, stdout.String(), "compute-reachable: usable")
}

func TestControlPlaneProfileValidateCommandFailsWithFieldErrors(t *testing.T) {
	doc := removeLine(validControlPlaneProfileYAML(), "      natsURL: tls://nats.nvcf-cp.internal:4222")
	path := writeControlPlaneProfileFixture(t, doc)
	resetControlPlaneProfileValidateCommand(t)

	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "control-plane", "profile", "validate",
		"--file", path,
		"--require", "compute-reachable",
	})

	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "controlPlane.endpoints.computeReachable.natsURL")
}

func TestControlPlaneProfileValidateCommandHelpShowsAnyRequireMode(t *testing.T) {
	resetControlPlaneProfileValidateCommand(t)

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{
		"self-hosted", "control-plane", "profile", "validate",
		"--help",
	})

	err := rootCmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "any")
	assert.Contains(t, stdout.String(), "Endpoint scope to require")
}

func TestControlPlaneProfileExportCommandRecreatesProfileFromOpenBao(t *testing.T) {
	resetControlPlaneProfileValidateCommand(t)
	resetViperForProfileTest(t)
	stackDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackDir, "helmfile.d"), 0o755))
	rootCA := controlPlaneProfileTestCertPEM(t)
	wantFingerprint, err := trustbundle.Fingerprint([]byte(rootCA))
	require.NoError(t, err)

	prevFetch := fetchControlPlaneRootCAPEM
	fetchControlPlaneRootCAPEM = func(_ context.Context, kctx string) (string, error) {
		assert.Equal(t, "cp-context", kctx)
		return rootCA, nil
	}
	t.Cleanup(func() { fetchControlPlaneRootCAPEM = prevFetch })

	var stderr bytes.Buffer
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"self-hosted",
		"--control-plane-stack", stackDir,
		"--env", "local",
		"--control-plane-context", "cp-context",
		"--compute-plane-context", "compute-context",
		"--icms-url", "http://sis.localhost:8080",
		"--nats-url", "nats://nats.localhost:4222",
		"control-plane", "profile", "export",
		"--cluster-name", "nvcf-cp",
		"--nca-id", "nvcf-test",
		"--region", "us-east-1",
	})

	require.NoError(t, rootCmd.Execute())
	assert.Contains(t, stderr.String(), "Wrote control-plane profile")

	body, err := os.ReadFile(filepath.Join(stackDir, "out", controlPlaneProfileFileName))
	require.NoError(t, err)
	result, err := controlplaneprofile.ParseAndValidate(body, controlplaneprofile.ValidateOptions{Require: controlplaneprofile.RequireBoth})
	require.NoError(t, err)
	assert.Equal(t, "nvcf-cp", result.Profile.ControlPlane.ClusterName)
	assert.Equal(t, "nvcf-test", result.Profile.ControlPlane.NCAID)
	assert.Equal(t, "us-east-1", result.Profile.ControlPlane.Region)
	assert.Equal(t, controlplaneprofile.TrustModeBundle, result.Profile.ManagementTLS.TrustMode)
	assert.Equal(t, strings.TrimSpace(rootCA), strings.TrimSpace(result.Profile.ManagementTLS.CABundlePEM))
	assert.Equal(t, controlplaneprofile.TrustModeBundle, result.Profile.TransportTLS.TrustMode)
	assert.Equal(t, strings.TrimSpace(rootCA), strings.TrimSpace(result.Profile.TransportTLS.TrustBundlePEM))
	assert.Equal(t, wantFingerprint, result.Profile.TransportTLS.TrustBundleFingerprint)
}

func TestParseControlPlaneProfileRequireModeAcceptsAny(t *testing.T) {
	requireMode, err := parseControlPlaneProfileRequireMode("any")
	require.NoError(t, err)
	assert.Equal(t, "any", string(requireMode))
}

func resetControlPlaneProfileValidateCommand(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		rootCmd.SetOut(os.Stdout)
		rootCmd.SetErr(os.Stderr)
		rootCmd.SetArgs(nil)
		controlPlaneProfileValidateFile = ""
		controlPlaneProfileValidateRequire = ""
		controlPlaneProfileExportCluster = ""
		controlPlaneProfileExportNCAID = "nvcf-default"
		controlPlaneProfileExportRegion = "us-west-1"
		selfHostedControlPlaneStack = ""
		selfHostedComputePlaneStack = ""
		selfHostedEnv = "local"
		selfHostedICMSURL = ""
		selfHostedNATSURL = ""
		selfHostedControlPlaneContext = ""
		selfHostedComputePlaneContext = ""
	})
}

func writeControlPlaneProfileFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control-plane-profile.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func removeLine(content, needle string) string {
	lines := bytes.Split([]byte(content), []byte("\n"))
	out := lines[:0]
	for _, line := range lines {
		if string(line) == needle {
			continue
		}
		out = append(out, line)
	}
	return string(bytes.Join(out, []byte("\n")))
}

// resetViperForProfileTest isolates the profile tests from any developer config
// file (~/.nvcf-cli.yaml) that LoadConfigWithoutAuth may otherwise pick up via
// the running shell context. resolveProfileGatewayHTTPURL consults cfg.BaseHTTPURL
// so leftover state (e.g. base_http_url=https://api.nvcf.nvidia.com) would
// override the icmsURL-derived gateway URL the test wants to assert against.
func resetViperForProfileTest(t *testing.T) {
	t.Helper()
	configureSelfHostedTestConfig(t, "")
}

func TestBuildControlPlaneProfile_LocalK3DKeepsServicePrefixedHosts(t *testing.T) {
	resetViperForProfileTest(t)
	t.Setenv("API_HOST", "")
	t.Setenv("API_KEYS_HOST", "")
	t.Setenv("INVOKE_HOST", "")
	t.Setenv("NVCF_ICMS_HOST", "")
	t.Setenv("NVCF_REVAL_HOST", "")
	t.Setenv("NVCF_NATS_HOST", "")
	t.Setenv("NVCF_BASE_HTTP_URL", "")
	t.Setenv("NVCF_BASE_GRPC_URL", "")

	prevEnv := selfHostedEnv
	t.Cleanup(func() { selfHostedEnv = prevEnv })
	selfHostedEnv = "local"

	got := buildControlPlaneProfile(controlPlaneProfileWriteRequest{
		ClusterName: "ncp-local",
		NCAID:       "nvcf-default",
		Region:      "us-west-1",
		Env:         "local",
		ICMSURL:     "http://sis.localhost:8080",
	})

	assert.Equal(t, "api.localhost", got.ControlPlane.Hosts.API)
	assert.Equal(t, "api-keys.localhost", got.ControlPlane.Hosts.APIKeys)
	assert.Equal(t, "sis.localhost", got.ControlPlane.Hosts.SIS)
	assert.Equal(t, "reval.localhost", got.ControlPlane.Hosts.ReVal)
	assert.Equal(t, "nats.localhost", got.ControlPlane.Hosts.NATS)
	assert.Equal(t, "invocation.localhost", got.ControlPlane.Hosts.Invocation)
	assert.Equal(t, "http://sis.localhost:8080", got.ControlPlane.Endpoints.ComputeReachable.ICMSURL)
	assert.Equal(t, "http://reval.localhost:8080", got.ControlPlane.Endpoints.ComputeReachable.ReValURL)
	assert.Equal(t, "nats://nats.localhost:4222", got.ControlPlane.Endpoints.ComputeReachable.NATSURL)
}

func TestBuildControlPlaneProfile_BareELBProjectsServicePrefixes(t *testing.T) {
	// Simulate GATEWAY_ADDR-routed EKS where icms_url is a bare ELB hostname:
	// the emitted hosts and computeReachable URLs must carry the canonical
	// sis./reval./nats. service prefixes that the gateway HTTPRoutes match.
	resetViperForProfileTest(t)
	t.Setenv("API_HOST", "")
	t.Setenv("API_KEYS_HOST", "")
	t.Setenv("INVOKE_HOST", "")
	t.Setenv("NVCF_ICMS_HOST", "")
	t.Setenv("NVCF_REVAL_HOST", "")
	t.Setenv("NVCF_NATS_HOST", "")
	// In the bare-ELB topology the operator's base_http_url points at the same
	// gateway ELB. Force resolveProfileGatewayHTTPURL to use it so the test
	// does not pick up an unrelated default like https://api.nvcf.nvidia.com.
	t.Setenv("NVCF_BASE_HTTP_URL", "http://abc123.elb.us-east-1.amazonaws.com")
	t.Setenv("NVCF_BASE_GRPC_URL", "")

	prevEnv := selfHostedEnv
	t.Cleanup(func() { selfHostedEnv = prevEnv })
	selfHostedEnv = "qa"

	got := buildControlPlaneProfile(controlPlaneProfileWriteRequest{
		ClusterName: "nvcf-cp-qa",
		NCAID:       "nvcf-default",
		Region:      "us-east-1",
		Env:         "qa",
		ICMSURL:     "http://abc123.elb.us-east-1.amazonaws.com",
	})

	assert.Equal(t, "abc123.elb.us-east-1.amazonaws.com", got.ControlPlane.Hosts.API)
	assert.Equal(t, "api-keys.abc123.elb.us-east-1.amazonaws.com", got.ControlPlane.Hosts.APIKeys)
	assert.Equal(t, "sis.abc123.elb.us-east-1.amazonaws.com", got.ControlPlane.Hosts.SIS)
	assert.Equal(t, "reval.abc123.elb.us-east-1.amazonaws.com", got.ControlPlane.Hosts.ReVal)
	assert.Equal(t, "nats.abc123.elb.us-east-1.amazonaws.com", got.ControlPlane.Hosts.NATS)
	assert.Equal(t, "invocation.abc123.elb.us-east-1.amazonaws.com", got.ControlPlane.Hosts.Invocation)
	assert.Equal(t, "http://sis.abc123.elb.us-east-1.amazonaws.com", got.ControlPlane.Endpoints.ComputeReachable.ICMSURL)
	assert.Equal(t, "http://reval.abc123.elb.us-east-1.amazonaws.com", got.ControlPlane.Endpoints.ComputeReachable.ReValURL)
	assert.Equal(t, "nats://nats.abc123.elb.us-east-1.amazonaws.com:4222", got.ControlPlane.Endpoints.ComputeReachable.NATSURL)
}

func TestWriteControlPlaneProfileSourcesOpenBaoRootCA(t *testing.T) {
	resetViperForProfileTest(t)
	rootCA := controlPlaneProfileTestCertPEM(t)
	wantFingerprint, err := trustbundle.Fingerprint([]byte(rootCA))
	require.NoError(t, err)

	prevFetch := fetchControlPlaneRootCAPEM
	fetchControlPlaneRootCAPEM = func(_ context.Context, kctx string) (string, error) {
		assert.Equal(t, "cp-context", kctx)
		return rootCA, nil
	}
	t.Cleanup(func() { fetchControlPlaneRootCAPEM = prevFetch })

	path, err := writeControlPlaneProfile(controlPlaneProfileWriteRequest{
		StackPath:           t.TempDir(),
		ClusterName:         "nvcf-cp",
		NCAID:               "nvcf-default",
		Region:              "us-west-1",
		Env:                 "local",
		ControlPlaneContext: "cp-context",
		ICMSURL:             "http://sis.localhost:8080",
		Ctx:                 context.Background(),
		SourceRootCA:        true,
	})
	require.NoError(t, err)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	result, err := controlplaneprofile.ParseAndValidate(body, controlplaneprofile.ValidateOptions{Require: controlplaneprofile.RequireAny})
	require.NoError(t, err)
	assert.Equal(t, controlplaneprofile.TrustModeBundle, result.Profile.ManagementTLS.TrustMode)
	assert.Equal(t, strings.TrimSpace(rootCA), strings.TrimSpace(result.Profile.ManagementTLS.CABundlePEM))
	assert.Equal(t, controlplaneprofile.TrustModeBundle, result.Profile.TransportTLS.TrustMode)
	assert.Equal(t, strings.TrimSpace(rootCA), strings.TrimSpace(result.Profile.TransportTLS.TrustBundlePEM))
	assert.Equal(t, wantFingerprint, result.Profile.TransportTLS.TrustBundleFingerprint)
}

func TestRewriteURLHost(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		newHost string
		want    string
	}{
		{name: "rewrites bare hostname", in: "http://x.elb.amazonaws.com", newHost: "sis.x.elb.amazonaws.com", want: "http://sis.x.elb.amazonaws.com"},
		{name: "preserves port", in: "http://x.elb.amazonaws.com:8080", newHost: "sis.x.elb.amazonaws.com", want: "http://sis.x.elb.amazonaws.com:8080"},
		{name: "preserves nats scheme and port", in: "nats://x.elb.amazonaws.com:4222", newHost: "nats.x.elb.amazonaws.com", want: "nats://nats.x.elb.amazonaws.com:4222"},
		{name: "empty newHost is no-op", in: "http://sis.localhost:8080", newHost: "", want: "http://sis.localhost:8080"},
		{name: "empty rawURL is no-op", in: "", newHost: "sis.localhost", want: ""},
		{name: "no host is no-op", in: "/relative/path", newHost: "sis.localhost", want: "/relative/path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, rewriteURLHost(tc.in, tc.newHost))
		})
	}
}

func controlPlaneProfileTestCertPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "NVCF Root CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func validControlPlaneProfileYAML() string {
	return `apiVersion: nvcf.nvidia.com/v1alpha1
kind: ControlPlaneProfile

controlPlane:
  clusterName: nvcf-cp-euw1
  ncaID: nvcf-default
  region: eu-west-1

  endpoints:
    inCluster:
      icmsURL: http://api.sis.svc.cluster.local:8080
      revalURL: http://reval.nvcf.svc.cluster.local:8080
      natsURL: nats://nats.nats-system.svc.cluster.local:4222

    computeReachable:
      icmsURL: https://sis.nvcf-cp.internal
      revalURL: https://reval.nvcf-cp.internal
      natsURL: tls://nats.nvcf-cp.internal:4222

  gateway:
    httpURL: https://api.nvcf-cp.internal
    grpcURL: api.nvcf-cp.internal:10081

  hosts:
    api: api.nvcf-cp.internal
    apiKeys: api-keys.nvcf-cp.internal
    sis: sis.nvcf-cp.internal
    reval: reval.nvcf-cp.internal
    nats: nats.nvcf-cp.internal
    invocation: invocation.nvcf-cp.internal
`
}
