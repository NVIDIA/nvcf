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
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelfHosted_RegisteredOnRoot(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"self-hosted"})
	assert.NoError(t, err)
	assert.NotNil(t, cmd)
	assert.Equal(t, "self-hosted", cmd.Name())
}

func TestSelfHosted_HasGlobalFlags(t *testing.T) {
	cmd, _, _ := rootCmd.Find([]string{"self-hosted"})
	for _, name := range []string{"stack", "env", "no-apply", "non-interactive", "token", "output", "wait", "icms-url", "nats-url",
		"control-plane-context", "compute-plane-context"} {
		assert.NotNil(t, cmd.PersistentFlags().Lookup(name), "missing flag %q", name)
	}
}

func TestSelfHostedFlags_OnlyOneContextErrors(t *testing.T) {
	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"self-hosted", "up", "--control-plane-context=cp", "--cluster-name=t", "--non-interactive"})
	t.Cleanup(func() {
		selfHostedControlPlaneContext = ""
		selfHostedComputePlaneContext = ""
	})
	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must both be set or both be empty")
}

func TestSelfHostedFlags_SameContextErrors(t *testing.T) {
	rootCmd.SetArgs([]string{"self-hosted", "check", "--control-plane-context=cp", "--compute-plane-context=cp"})
	t.Cleanup(func() {
		selfHostedControlPlaneContext = ""
		selfHostedComputePlaneContext = ""
	})
	err := rootCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot both be")
}

func TestDeriveICMSFromAPI(t *testing.T) {
	cases := []struct {
		in          string
		wantURL     string
		wantDerived bool
	}{
		{"http://api.localhost:8080", "http://sis.localhost:8080", true},
		{"https://api.example.com", "https://sis.example.com", true},
		{"https://api.dev.foo.com:443/v1", "https://sis.dev.foo.com:443/v1", true},
		{"http://localhost:8080", "http://localhost:8080", false},
		{"http://nvcf.example.com", "http://nvcf.example.com", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := deriveICMSFromAPI(tc.in)
		assert.Equal(t, tc.wantURL, got, "input %q", tc.in)
		assert.Equal(t, tc.wantDerived, ok, "input %q", tc.in)
	}
}

func TestDeriveSiblingHTTPServiceURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		svc  string
		want string
	}{
		{name: "api hostname to reval", in: "http://api.localhost:18080", svc: "reval", want: "http://reval.localhost:18080"},
		{name: "sis hostname to reval", in: "https://sis.dev.example.com:8443", svc: "reval", want: "https://reval.dev.example.com:8443"},
		{name: "plain host falls back unchanged", in: "http://localhost:18080", svc: "reval", want: "http://localhost:18080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveSiblingHTTPServiceURL(tc.in, tc.svc)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDeriveNATSURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "api hostname", in: "http://api.localhost:18080", want: "nats://nats.localhost:4222"},
		{name: "sis hostname", in: "https://sis.dev.example.com:8443", want: "nats://nats.dev.example.com:4222"},
		{name: "plain host", in: "http://localhost:18080", want: "nats://localhost:4222"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveNATSURL(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLocalEndpointDefaultsFromLocalSISURL(t *testing.T) {
	got, ok := localEndpointDefaultsFromICMSURL("http://sis.localhost:8080")
	require.True(t, ok)
	assert.Equal(t, "http://api.localhost:8080", got.BaseHTTPURL)
	assert.Equal(t, "http://invocation.localhost:8080", got.InvokeURL)
	assert.Equal(t, "http://api-keys.localhost:8080", got.APIKeysURL)
	assert.Equal(t, "api.localhost", got.APIHost)
	assert.Equal(t, "invocation.localhost", got.InvokeHost)
	assert.Equal(t, "api-keys.localhost", got.APIKeysHost)
}

func TestLocalEndpointDefaultsIgnoreNonLocalURL(t *testing.T) {
	_, ok := localEndpointDefaultsFromICMSURL("https://sis.example.com")
	assert.False(t, ok)
}

func TestResolveICMSURL_FlagWins(t *testing.T) {
	t.Setenv("NVCF_ICMS_URL", "http://env.example:8080")
	got := resolveICMSURL("http://flag.example:8080")
	assert.Equal(t, "http://flag.example:8080", got)
}

func TestResolveICMSURL_EnvBeforeConfig(t *testing.T) {
	t.Setenv("NVCF_ICMS_URL", "http://env.example:8080")
	got := resolveICMSURL("")
	assert.Equal(t, "http://env.example:8080", got)
}

func TestResolveICMSURL_LegacySISEnv(t *testing.T) {
	t.Setenv("NVCF_ICMS_URL", "")
	t.Setenv("NVCF_SIS_URL", "http://sis.localhost:8080")
	got := resolveICMSURL("")
	assert.Equal(t, "http://sis.localhost:8080", got)
}

func TestResolveICMSURL_ConfigBeforeDerived(t *testing.T) {
	configureSelfHostedTestConfig(t, `
base_http_url: "http://api.localhost:8080"
icms_url: "http://configured-sis.localhost:8080"
`)

	got := resolveICMSURL("")
	assert.Equal(t, "http://configured-sis.localhost:8080", got)
}

func configureSelfHostedTestConfig(t *testing.T, body string) {
	t.Helper()
	prevCfgFile := cfgFile
	viper.Reset()
	viper.SetEnvPrefix("NVCF")
	viper.AutomaticEnv()
	t.Setenv("NVCF_ICMS_URL", "")
	t.Setenv("NVCF_SIS_URL", "")
	cfgFile = ""

	configPath := filepath.Join(t.TempDir(), "nvcf-cli.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(body), 0o600))
	viper.SetConfigFile(configPath)
	require.NoError(t, viper.ReadInConfig())

	t.Cleanup(func() {
		cfgFile = prevCfgFile
		viper.Reset()
		viper.SetEnvPrefix("NVCF")
		viper.AutomaticEnv()
	})
}

func TestResolveRegisterEndpointValues_LocalSplitUsesControlPlaneExternalEndpoints(t *testing.T) {
	got := resolveRegisterEndpointValues(
		"local",
		"admin@cp",
		"admin@gpu1",
		"http://sis.localhost:18080",
		"",
	)

	assert.Equal(t, "http://sis.nvcf-control-plane.test:18080", got.ICMSServiceURL)
	assert.Equal(t, "http://reval.nvcf-control-plane.test:18080", got.ReValServiceURL)
	assert.Equal(t, "nats://nats.nvcf-control-plane.test:4222", got.NATSURL)
}

func TestResolveRegisterEndpointValues_LocalSplitKeepsExplicitExternalDomain(t *testing.T) {
	got := resolveRegisterEndpointValues(
		"local",
		"admin@cp",
		"admin@gpu1",
		"http://sis.custom-control.test:18080",
		"",
	)

	assert.Equal(t, "http://sis.custom-control.test:18080", got.ICMSServiceURL)
	assert.Equal(t, "http://reval.custom-control.test:18080", got.ReValServiceURL)
	assert.Equal(t, "nats://nats.custom-control.test:4222", got.NATSURL)
}

func TestResolveRegisterEndpointValues_LocalSplitUsesControlPlaneEnvOverrides(t *testing.T) {
	t.Setenv("CONTROL_PLANE_DOMAIN", "nvcf-control-plane.internal")
	t.Setenv("CONTROL_PLANE_HTTP_PORT", "19080")
	t.Setenv("CONTROL_PLANE_NATS_PORT", "14222")

	got := resolveRegisterEndpointValues(
		"local",
		"admin@cp",
		"admin@gpu1",
		"http://sis.localhost",
		"",
	)

	assert.Equal(t, "http://sis.nvcf-control-plane.internal:19080", got.ICMSServiceURL)
	assert.Equal(t, "http://reval.nvcf-control-plane.internal:19080", got.ReValServiceURL)
	assert.Equal(t, "nats://nats.nvcf-control-plane.internal:14222", got.NATSURL)
}

func TestResolveRegisterEndpointValues_NonLocalSplitPreservesExplicitEndpoints(t *testing.T) {
	got := resolveRegisterEndpointValues(
		"prd",
		"admin@cp",
		"admin@gpu1",
		"https://sis.example.com",
		"nats://nats.example.com:4222",
	)

	assert.Equal(t, "https://sis.example.com", got.ICMSServiceURL)
	assert.Equal(t, "https://reval.example.com", got.ReValServiceURL)
	assert.Equal(t, "nats://nats.example.com:4222", got.NATSURL)
}
