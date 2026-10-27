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

package gds

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/pkg/fakegds"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
)

const clusterset = "prod"

// logSink records every log line.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *logSink) logger() logr.Logger {
	return funcr.New(func(prefix, args string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.lines = append(s.lines, prefix+" "+args)
	}, funcr.Options{})
}

func (s *logSink) contains(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range s.lines {
		if strings.Contains(line, text) {
			return true
		}
	}
	return false
}

func caPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gds-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func gdsConfig(urls ...string) config.Config {
	return config.Config{
		RouterSource:    config.RouterSourceGDS,
		GDSURLs:         urls,
		GDSClustersetID: clusterset,
	}
}

// startRouters runs Routers against a fake GDS until the test ends.
func startRouters(t *testing.T, cfg config.Config, logs *logSink, syncTimeout time.Duration) *Routers {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r, err := New(ctx, cfg, nil, Options{
		Registerer:  prometheus.NewRegistry(),
		Logger:      logs.logger(),
		SyncTimeout: syncTimeout,
		TTL:         time.Second,
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Error("Routers.Start did not return after cancellation")
		}
	})
	return r
}

func serveFake(t *testing.T) (*fakegds.Server, string) {
	t.Helper()
	srv := fakegds.New()
	run, err := srv.ServeLocal()
	require.NoError(t, err)
	t.Cleanup(run.Stop)
	return srv, "http://" + run.Addr()
}

func TestRouterCountFollowsGDS(t *testing.T) {
	srv, url := serveFake(t)
	srv.PutExport(clusterset, "cluster-a", "nvcf", "llm-request-router-backend-router",
		fakegds.RouterSpec(50071, 0, "http"), fakegds.ReadyEndpoints("10.1.0.1", "10.1.0.2"))
	logs := &logSink{}
	r := startRouters(t, gdsConfig(url), logs, 10*time.Second)

	require.Eventually(t, func() bool {
		n, ok := r.RouterCount()
		return ok && n == 1
	}, 10*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool { return logs.contains("GDS lists routers for the clusterset") },
		10*time.Second, 20*time.Millisecond)

	srv.PutExport(clusterset, "cluster-b", "nvcf", "llm-request-router-backend-router",
		fakegds.RouterSpec(50071, 0, "http"), fakegds.ReadyEndpoints("10.2.0.1"))
	require.Eventually(t, func() bool {
		n, ok := r.RouterCount()
		return ok && n == 2
	}, 10*time.Second, 20*time.Millisecond, "a new router cluster is counted")

	// A router without ready replicas has no seeds, so pylons cannot use it.
	srv.SetEndpoints(clusterset, "cluster-b", "nvcf", "llm-request-router-backend-router", nil)
	require.Eventually(t, func() bool {
		n, ok := r.RouterCount()
		return ok && n == 1
	}, 10*time.Second, 20*time.Millisecond)
}

func TestStartupWarnsWithoutRouters(t *testing.T) {
	_, url := serveFake(t)
	logs := &logSink{}
	r := startRouters(t, gdsConfig(url), logs, 10*time.Second)

	require.Eventually(t, func() bool { return logs.contains(ErrNoRouters.Error()) },
		10*time.Second, 20*time.Millisecond)
	assert.True(t, logs.contains("GDS lists no router"))
	n, ok := r.RouterCount()
	assert.True(t, ok)
	assert.Zero(t, n)
}

func TestStartupReportsAnUnreachableGDS(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	url := "http://" + lis.Addr().String()
	require.NoError(t, lis.Close())
	logs := &logSink{}
	r := startRouters(t, gdsConfig(url), logs, 200*time.Millisecond)

	require.Eventually(t, func() bool { return logs.contains("Could not list routers from GDS") },
		10*time.Second, 20*time.Millisecond)
	_, ok := r.RouterCount()
	assert.False(t, ok, "the router list is unknown")
}

func TestNewRejectsBadConfiguration(t *testing.T) {
	_, err := New(context.Background(), gdsConfig("ftp://gds.example.test"), nil, Options{})
	assert.ErrorContains(t, err, "connecting to GDS")

	_, err = New(context.Background(), gdsConfig("https://gds.example.test"), []byte("not PEM"), Options{})
	assert.ErrorContains(t, err, "holds no PEM certificate")

	cfg := gdsConfig("https://gds.example.test")
	cfg.GDSClustersetID = ""
	_, err = New(context.Background(), cfg, nil, Options{})
	assert.ErrorContains(t, err, "creating the GDS router cache")

	r, err := New(context.Background(), gdsConfig("https://gds.example.test"), caPEM(t), Options{})
	require.NoError(t, err)
	require.NoError(t, r.client.Close())
}

func TestTLSConfig(t *testing.T) {
	tc, err := TLSConfig(nil)
	require.NoError(t, err)
	assert.Nil(t, tc, "the client default trusts the system roots")

	_, err = TLSConfig([]byte("-----BEGIN CERTIFICATE-----\nnot base64\n-----END CERTIFICATE-----\n"))
	assert.Error(t, err)

	tc, err = TLSConfig(caPEM(t))
	require.NoError(t, err)
	require.NotNil(t, tc.RootCAs)
}

func TestLoadCA(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	ca := caPEM(t)
	objects := []*corev1.ConfigMap{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "text"}, Data: map[string]string{"ca.crt": string(ca)}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "binary"}, BinaryData: map[string][]byte{"ca.crt": ca}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "other-key"}, Data: map[string]string{"tls.crt": "x"}},
	}
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, obj := range objects {
		builder = builder.WithObjects(obj)
	}
	reader := builder.Build()
	ctx := context.Background()

	got, err := LoadCA(ctx, reader, "ops", "")
	require.NoError(t, err)
	assert.Nil(t, got)
	for _, name := range []string{"text", "binary"} {
		got, err = LoadCA(ctx, reader, "ops", name)
		require.NoError(t, err, name)
		assert.Equal(t, ca, got, name)
	}
	_, err = LoadCA(ctx, reader, "ops", "other-key")
	assert.ErrorContains(t, err, "has no ca.crt key")
	_, err = LoadCA(ctx, reader, "ops", "missing")
	assert.ErrorContains(t, err, "reading the GDS CA ConfigMap ops/missing")
	assert.NotContains(t, fmt.Sprint(err), string(ca))
}

func TestPlaintext(t *testing.T) {
	assert.True(t, plaintext([]string{"http://gds:9090"}))
	assert.False(t, plaintext([]string{"https://gds"}))
	assert.False(t, plaintext(nil))
}
