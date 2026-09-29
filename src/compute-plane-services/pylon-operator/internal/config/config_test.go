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
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// noNamespaceSource clears both defaults of --operator-namespace.
func noNamespaceSource(t *testing.T) {
	t.Helper()
	t.Setenv(PodNamespaceEnv, "")
	setNamespaceFile(t, filepath.Join(t.TempDir(), "missing"))
}

func setNamespaceFile(t *testing.T, path string) {
	t.Helper()
	old := serviceAccountNamespaceFile
	serviceAccountNamespaceFile = path
	t.Cleanup(func() { serviceAccountNamespaceFile = old })
}

func parse(t *testing.T, args ...string) (Config, error) {
	t.Helper()
	var c Config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c.BindFlags(fs)
	err := fs.Parse(args)
	return c, err
}

func valid(t *testing.T, extra ...string) Config {
	t.Helper()
	c, err := parse(t, append([]string{
		"--cluster-id=spark-berlin",
		"--router-grpc-address=llm-request-router.gateway.svc:50071",
		"--pylon-image=nvcr.io/example/pylon:1.0",
		"--operator-namespace=pylon-operator",
	}, extra...)...)
	require.NoError(t, err)
	return c
}

func TestDefaults(t *testing.T) {
	noNamespaceSource(t)
	c, err := parse(t)
	require.NoError(t, err)
	assert.Equal(t, Config{
		TransportReplicas:       1,
		ProbeInterval:           10 * time.Second,
		ScrapeInterval:          5 * time.Second,
		MetricsBindAddress:      metricsserver.DefaultBindAddress,
		HealthProbeBindAddress:  ":8081",
		ClusterCredentialSecret: "pylon-operator-cluster-credential",
		InitialInputTPS:         100,
	}, c)
}

func TestOperatorNamespaceDefault(t *testing.T) {
	file := filepath.Join(t.TempDir(), "namespace")
	require.NoError(t, os.WriteFile(file, []byte("from-file\n"), 0o600))
	setNamespaceFile(t, file)

	t.Setenv(PodNamespaceEnv, " from-env ")
	c, err := parse(t)
	require.NoError(t, err)
	assert.Equal(t, "from-env", c.OperatorNamespace, "POD_NAMESPACE wins")

	t.Setenv(PodNamespaceEnv, "")
	c, err = parse(t)
	require.NoError(t, err)
	assert.Equal(t, "from-file", c.OperatorNamespace, "then the service account namespace")

	c, err = parse(t, "--operator-namespace=explicit")
	require.NoError(t, err)
	assert.Equal(t, "explicit", c.OperatorNamespace, "the flag overrides both")
}

func TestParseAllFlags(t *testing.T) {
	c := valid(t,
		"--pylon-image=nvcr.io/example/pylon:1.0",
		"--pylon-image-pull-policy=IfNotPresent",
		"--watch-namespaces= models, team-b,,models ",
		"--transport-replicas=2",
		"--probe-interval=30s",
		"--scrape-interval=2s",
		"--metrics-bind-address=:8443",
		"--health-probe-bind-address=:9440",
		"--leader-elect",
		"--dev-insecure-transport",
		"--cluster-credential-secret=cred",
		"--operator-namespace=ops",
		"--trust-bundle-configmap=router-ca",
		"--initial-input-tps=2200.5",
	)
	require.NoError(t, c.Validate())
	assert.Equal(t, Config{
		ClusterID:               "spark-berlin",
		RouterGRPCAddress:       "llm-request-router.gateway.svc:50071",
		PylonImage:              "nvcr.io/example/pylon:1.0",
		PylonImagePullPolicy:    "IfNotPresent",
		WatchNamespaces:         []string{"models", "team-b"},
		TransportReplicas:       2,
		ProbeInterval:           30 * time.Second,
		ScrapeInterval:          2 * time.Second,
		MetricsBindAddress:      ":8443",
		HealthProbeBindAddress:  ":9440",
		LeaderElect:             true,
		DevInsecureTransport:    true,
		ClusterCredentialSecret: "cred",
		OperatorNamespace:       "ops",
		TrustBundleConfigMap:    "router-ca",
		InitialInputTPS:         2200.5,
	}, c)
}

func TestParseRejectsBadReplicas(t *testing.T) {
	_, err := parse(t, "--transport-replicas=many")
	assert.Error(t, err)
	_, err = parse(t, "--transport-replicas=4294967296")
	assert.Error(t, err)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		mutate  func(*Config)
		wantErr []string
	}{
		{name: "valid"},
		{
			name: "missing required flags",
			mutate: func(c *Config) {
				c.ClusterID, c.RouterGRPCAddress, c.PylonImage, c.OperatorNamespace = "", "", "", ""
			},
			wantErr: []string{
				"--cluster-id is required",
				"--router-grpc-address is required",
				"--pylon-image is required",
				"--operator-namespace is required; set it or POD_NAMESPACE",
			},
		},
		{
			name:    "operator namespace is not a namespace name",
			args:    []string{"--operator-namespace=Ops_NS"},
			wantErr: []string{`--operator-namespace "Ops_NS" is not a namespace name`},
		},
		{
			name:    "bad trust bundle name",
			args:    []string{"--trust-bundle-configmap=Not A Name"},
			wantErr: []string{`--trust-bundle-configmap "Not A Name" is not a ConfigMap name`},
		},
		{
			name:    "zero initial input tps",
			args:    []string{"--initial-input-tps=0"},
			wantErr: []string{"--initial-input-tps must be a positive number, got 0"},
		},
		{
			name:    "NaN initial input tps",
			mutate:  func(c *Config) { c.InitialInputTPS = math.NaN() },
			wantErr: []string{"--initial-input-tps must be a positive number, got NaN"},
		},
		{
			name:    "infinite initial input tps",
			mutate:  func(c *Config) { c.InitialInputTPS = math.Inf(1) },
			wantErr: []string{"--initial-input-tps must be a positive number, got +Inf"},
		},
		{
			name:    "cluster id is not a DNS label",
			args:    []string{"--cluster-id=Spark/Berlin"},
			wantErr: []string{`--cluster-id "Spark/Berlin" is not a DNS label`},
		},
		{
			name:    "bad namespace",
			args:    []string{"--watch-namespaces=models,Bad_NS"},
			wantErr: []string{`--watch-namespaces entry "Bad_NS"`},
		},
		{
			name:    "zero replicas",
			args:    []string{"--transport-replicas=0"},
			wantErr: []string{"--transport-replicas must be at least 1, got 0"},
		},
		{
			name:    "non-positive intervals",
			args:    []string{"--probe-interval=0s", "--scrape-interval=-1s"},
			wantErr: []string{"--probe-interval must be positive", "--scrape-interval must be positive"},
		},
		{
			name: "every pull policy",
			args: []string{"--pylon-image-pull-policy=Never"},
		},
		{
			name:    "unknown pull policy",
			args:    []string{"--pylon-image-pull-policy=ifnotpresent"},
			wantErr: []string{`--pylon-image-pull-policy must be Always, IfNotPresent or Never, got "ifnotpresent"`},
		},
		{
			name:    "bad secret name",
			args:    []string{"--cluster-credential-secret=Not A Name"},
			wantErr: []string{`--cluster-credential-secret "Not A Name"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid(t, tt.args...)
			if tt.mutate != nil {
				tt.mutate(&c)
			}
			err := c.Validate()
			if len(tt.wantErr) == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestManagerOptions(t *testing.T) {
	scheme := runtime.NewScheme()

	c := valid(t, "--leader-elect", "--metrics-bind-address=0")
	opts := c.ManagerOptions(scheme)
	assert.Same(t, scheme, opts.Scheme)
	assert.Equal(t, "0", opts.Metrics.BindAddress)
	assert.Equal(t, ":8081", opts.HealthProbeBindAddress)
	assert.True(t, opts.LeaderElection)
	assert.Equal(t, "pylon-operator.pylon.nvidia.com", opts.LeaderElectionID)
	assert.True(t, opts.LeaderElectionReleaseOnCancel)
	assert.Nil(t, opts.Cache.DefaultNamespaces, "all namespaces by default")

	require.Len(t, opts.Cache.ByObject, 3, "no ConfigMap cache without a trust bundle")
	managed := labels.SelectorFromSet(labels.Set{"app.kubernetes.io/managed-by": "pylon-operator"}).String()
	deployments := byObject[*appsv1.Deployment](t, opts.Cache.ByObject)
	assert.Equal(t, managed, deployments.Label.String())
	assert.Nil(t, deployments.Namespaces)
	pods := byObject[*corev1.Pod](t, opts.Cache.ByObject)
	assert.Equal(t, managed, pods.Label.String(), "only transport pods")
	assert.Nil(t, pods.Namespaces)
	secrets := byObject[*corev1.Secret](t, opts.Cache.ByObject)
	assert.Equal(t, fields.OneTermEqualSelector("metadata.name", DefaultClusterCredentialSecret).String(), secrets.Field.String())
	assert.Nil(t, secrets.Namespaces, "every namespace")

	c = valid(t, "--watch-namespaces=models,team-b", "--trust-bundle-configmap=router-ca")
	opts = c.ManagerOptions(scheme)
	assert.False(t, opts.LeaderElection)
	require.Len(t, opts.Cache.DefaultNamespaces, 2)
	assert.Contains(t, opts.Cache.DefaultNamespaces, "models")
	assert.Contains(t, opts.Cache.DefaultNamespaces, "team-b")
	secrets = byObject[*corev1.Secret](t, opts.Cache.ByObject)
	assert.Equal(t, map[string]cache.Config{"models": {}, "team-b": {}, "pylon-operator": {}}, secrets.Namespaces,
		"the watched namespaces and the operator namespace")
	configMaps := byObject[*corev1.ConfigMap](t, opts.Cache.ByObject)
	assert.Equal(t, fields.OneTermEqualSelector("metadata.name", "router-ca").String(), configMaps.Field.String())
	assert.Equal(t, secrets.Namespaces, configMaps.Namespaces)
}

func byObject[T client.Object](t *testing.T, m map[client.Object]cache.ByObject) cache.ByObject {
	t.Helper()
	for obj, by := range m {
		if _, ok := obj.(T); ok {
			return by
		}
	}
	var zero T
	t.Fatalf("no cache.ByObject for %T", zero)
	return cache.ByObject{}
}

func TestFlagValueStrings(t *testing.T) {
	var nilList *namespaceList
	assert.Equal(t, "", nilList.String())
	list := namespaceList{"a", "b"}
	assert.Equal(t, "a,b", list.String())

	var nilInt *int32Value
	assert.Equal(t, "0", nilInt.String())
	v := int32Value(3)
	assert.Equal(t, "3", v.String())
}
