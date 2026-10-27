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

package config

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	mcsv1beta1 "sigs.k8s.io/mcs-api/pkg/apis/v1beta1"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
)

func parse(t *testing.T, env map[string]string, args ...string) (*Config, error) {
	t.Helper()
	var c Config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c.bindFlags(fs, func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return &c, c.Validate()
}

func TestDefaults(t *testing.T) {
	c, err := parse(t, map[string]string{GDSURLsEnv: "https://gds.example.internal, https://gds.b.example.internal", PodNamespaceEnv: "gds-system"},
		"--clusterset-id=prod", "--cluster-id=a")
	require.NoError(t, err)
	assert.Equal(t, []string{"https://gds.example.internal", "https://gds.b.example.internal"}, c.GDSURLs)
	assert.Equal(t, "gds-system", c.OperatorNamespace)
	assert.Equal(t, IdentitySourceAuto, c.IdentitySource)
	assert.Equal(t, DefaultLeaseTTL, c.LeaseTTL)
	assert.Equal(t, DefaultEndpointsPerSlice, c.EndpointsPerSlice)
	assert.Equal(t, DefaultMaxConcurrentReconciles, c.MaxConcurrentReconciles)
	assert.True(t, c.AutoExportPools)
	assert.True(t, c.LeaderElect)
	assert.False(t, c.CreateNamespaces)
	assert.False(t, c.DeregisterOnShutdown)
	assert.False(t, c.WriteClusterProperties)
	assert.Equal(t, DefaultHealthProbeBindAddress, c.HealthProbeBindAddress)
	assert.True(t, c.WatchesNamespace("anything"))
}

func TestFlags(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(ca, []byte("x"), 0o600))
	c, err := parse(t, map[string]string{GDSURLsEnv: "https://ignored.example"},
		"--gds-url=http://a.example:8080,http://b.example", "--gds-url", "http://c.example",
		"--gds-allow-insecure-http", "--gds-tls-ca-file="+ca,
		"--clusterset-id=prod", "--cluster-id=a", "--cluster-identity-source=flags", "--region=us-west-2",
		"--cluster-labels=env=prod, tier=gpu", "--lease-ttl=45s", "--watch-namespaces=b,a,b",
		"--create-namespaces", "--adopt-unmanaged-imports", "--auto-export-pools=false",
		"--write-cluster-properties", "--deregister-on-shutdown", "--endpoints-per-slice=50",
		"--max-concurrent-reconciles=8", "--metrics-bind-address=0", "--health-probe-bind-address=:9000",
		"--leader-elect=false", "--otel-enabled")
	require.NoError(t, err)
	assert.Equal(t, []string{"http://a.example:8080", "http://b.example", "http://c.example"}, c.GDSURLs)
	assert.Equal(t, map[string]string{"env": "prod", "tier": "gpu"}, c.ClusterLabels)
	assert.Equal(t, []string{"a", "b"}, c.WatchNamespaces)
	assert.Equal(t, 45*time.Second, c.LeaseTTL)
	assert.True(t, c.CreateNamespaces && c.AdoptUnmanagedImports && c.WriteClusterProperties && c.DeregisterOnShutdown && c.OTelEnabled)
	assert.False(t, c.AutoExportPools)
	assert.Equal(t, 50, c.EndpointsPerSlice)
	assert.Equal(t, 8, c.MaxConcurrentReconciles)
	assert.True(t, c.WatchesNamespace("a"))
	assert.False(t, c.WatchesNamespace("c"))
	assert.Equal(t, "env=prod,tier=gpu", (*labelMap)(&c.ClusterLabels).String())
	assert.Equal(t, "a,b", (*namespaceList)(&c.WatchNamespaces).String())
}

func TestInvalidLabelFlag(t *testing.T) {
	_, err := parse(t, nil, "--cluster-labels=novalue")
	require.Error(t, err)
}

func TestValidate(t *testing.T) {
	base := []string{"--gds-url=https://gds.example", "--leader-elect=false"}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no url", []string{"--leader-elect=false"}, "--gds-url"},
		{"bad scheme", []string{"--gds-url=ftp://x", "--leader-elect=false"}, "scheme must be https or http"},
		{"no host", []string{"--gds-url=https://", "--leader-elect=false"}, "has no host"},
		{"plaintext", []string{"--gds-url=http://x", "--leader-elect=false"}, "--gds-allow-insecure-http"},
		{"mixed schemes", []string{"--gds-url=https://x,http://y", "--gds-allow-insecure-http", "--leader-elect=false"}, "every URL must use the scheme"},
		{"unparsable url", []string{"--gds-url=https://x:bad:port", "--leader-elect=false"}, "--gds-url"},
		{"ca file", append(base, "--gds-tls-ca-file=/does/not/exist"), "--gds-tls-ca-file"},
		{"identity source", append(base, "--cluster-identity-source=dns"), "--cluster-identity-source"},
		{"flags source needs ids", append(base, "--cluster-identity-source=flags", "--cluster-id=a"), "requires --clusterset-id"},
		{"cluster id", append(base, "--cluster-id=Not_A_Label"), "--cluster-id"},
		{"label key", append(base, "--cluster-labels=bad key=v"), "--cluster-labels key"},
		{"label value", append(base, "--cluster-labels=k=bad value"), "--cluster-labels value"},
		{"ttl", append(base, "--lease-ttl=1s"), "--lease-ttl"},
		{"namespace", append(base, "--watch-namespaces=Bad"), "--watch-namespaces"},
		{"per slice", append(base, "--endpoints-per-slice=0"), "--endpoints-per-slice"},
		{"per slice max", append(base, "--endpoints-per-slice=1001"), "--endpoints-per-slice"},
		{"workers", append(base, "--max-concurrent-reconciles=0"), "--max-concurrent-reconciles"},
		{"health", append(base, "--health-probe-bind-address="), "--health-probe-bind-address"},
		{"leader namespace", []string{"--gds-url=https://gds.example"}, "--operator-namespace"},
		{"operator namespace", append(base, "--operator-namespace=Bad"), "--operator-namespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(t, nil, tt.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
	t.Run("too many labels", func(t *testing.T) {
		c := Config{GDSURLs: []string{"https://x"}, LeaseTTL: DefaultLeaseTTL, EndpointsPerSlice: 1, MaxConcurrentReconciles: 1,
			HealthProbeBindAddress: ":1", IdentitySource: IdentitySourceAuto, ClusterLabels: map[string]string{}}
		for i := range api.MaxLabels + 1 {
			c.ClusterLabels["k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
		}
		require.ErrorContains(t, c.Validate(), "at most")
	})
}

func TestBindFlagsReadsEnvironment(t *testing.T) {
	t.Setenv(GDSURLsEnv, "https://env.example")
	t.Setenv(PodNamespaceEnv, "ns-from-env")
	var c Config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c.BindFlags(fs)
	require.NoError(t, fs.Parse(nil))
	assert.Equal(t, []string{"https://env.example"}, c.GDSURLs)
	assert.Equal(t, "ns-from-env", c.OperatorNamespace)
	assert.Equal(t, "https://env.example", fs.Lookup("gds-url").Value.String())
}

func TestSchemeAndManagerOptions(t *testing.T) {
	s, err := NewScheme()
	require.NoError(t, err)
	for _, gvk := range []schema.GroupVersionKind{
		{Version: "v1", Kind: "Service"},
		{Group: "discovery.k8s.io", Version: "v1", Kind: "EndpointSlice"},
		{Group: mcsv1beta1.GroupName, Version: "v1beta1", Kind: "ServiceExport"},
		{Group: mcsv1beta1.GroupName, Version: "v1beta1", Kind: "ServiceImport"},
	} {
		assert.True(t, s.Recognizes(gvk), gvk.String())
	}
	assert.False(t, s.Recognizes(ClusterPropertyGVK), "ClusterProperty stays unstructured")

	c := Config{MetricsBindAddress: "0", HealthProbeBindAddress: ":8081", LeaderElect: true, OperatorNamespace: "gds-system"}
	opts := c.ManagerOptions(s)
	assert.Equal(t, api.LeaderElectionID, opts.LeaderElectionID)
	assert.Equal(t, "gds-system", opts.LeaderElectionNamespace)
	assert.True(t, opts.LeaderElection)
	assert.True(t, opts.LeaderElectionReleaseOnCancel)
	assert.Equal(t, "0", opts.Metrics.BindAddress)
	assert.Nil(t, opts.Cache.DefaultNamespaces)
	assert.NotNil(t, opts.Cache.DefaultTransform)

	c.WatchNamespaces = []string{"a", "b"}
	opts = c.ManagerOptions(s)
	assert.Equal(t, map[string]cache.Config{"a": {}, "b": {}}, opts.Cache.DefaultNamespaces)
}

func TestURLListString(t *testing.T) {
	var l *urlList
	assert.Equal(t, "", l.String())
	var nl *namespaceList
	assert.Equal(t, "", nl.String())
	var lm *labelMap
	assert.Equal(t, "", lm.String())
}
