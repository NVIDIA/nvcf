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

package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestHTTPServiceRoutes(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())

	version.Version, version.GitHash = "0.0.0", "abcdefgh"
	defer func() { version.Version, version.GitHash = "", "" }()

	s := NewHTTPService("")
	s.AddHealthRoute(ctx)
	s.AddVersionRoute(ctx)
	s.AddMetricsRoute(ctx)
	s.AddAdminRoute(ctx)
	s.Use(NewHTTPMiddleware(ctx, WithRequestMetrics("testroutes"),
		WithHandlerTimeout(500*time.Millisecond),
		WithRequestBodyLimit(int64(10)))...)

	run := func(method, path, payload string) (string, int) {
		req := httptest.NewRequest(method, path, strings.NewReader(payload))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)

		resp := w.Result()
		body, _ := io.ReadAll(resp.Body)
		return string(body), resp.StatusCode
	}

	body, code := run("GET", "/notexist", "")
	assert.Contains(t, body, "not found")
	assert.Equal(t, http.StatusNotFound, code)

	body, code = run("GET", "/healthz", "")
	assert.Equal(t, "ok\n", body)
	assert.Equal(t, http.StatusOK, code)

	body, code = run("GET", "/version", "")
	assert.Equal(t, "0.0.0+abcdefgh\n", body)
	assert.Equal(t, http.StatusOK, code)

	body, code = run("GET", "/metrics", "")
	assert.Contains(t, body, "testroutes_http_duration_seconds")
	assert.Equal(t, http.StatusOK, code)

	assert.Equal(t, logrus.InfoLevel, GetLogger(ctx).Logger.GetLevel())
	body, code = run("GET", "/admin", "")
	assert.Contains(t, body, `"log-level": "info"`)
	assert.Equal(t, http.StatusOK, code)

	body, code = run("GET", "/admin?log-level=notexist", "")
	assert.Contains(t, body, "not a valid logrus Level")
	assert.Equal(t, http.StatusBadRequest, code)

	body, code = run("GET", "/admin?log-level=debug", "")
	assert.Contains(t, body, `"log-level": "debug"`)
	assert.Equal(t, http.StatusOK, code)

	body, code = run("GET", "/admin", "")
	assert.Contains(t, body, `"log-level": "debug"`)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, logrus.DebugLevel, GetLogger(ctx).Logger.GetLevel())

	// test request body limit
	{
		s.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write(body)
		}).Methods("POST")

		body, code := run("POST", "/echo", "123")
		assert.Equal(t, "123", body)
		assert.Equal(t, http.StatusOK, code)

		body, code = run("POST", "/echo", "123456789abcdef")
		assert.Contains(t, body, "request body too large")
		assert.Equal(t, http.StatusBadRequest, code)
	}

	// test request handler timeout, handlerTimeout was set to 500ms.
	{
		makeSlowAPIHandler := func(d time.Duration) func(w http.ResponseWriter, r *http.Request) {
			return func(w http.ResponseWriter, r *http.Request) {
				log := GetLogger(r.Context())

				select {
				case <-r.Context().Done():
					log.Infof("SlowAPICall was supposed to take %v, but was canceled. Err: %v", d, r.Context().Err())
				case <-time.After(d):
					log.Printf("SlowAPIcall done after %v", d)
					w.WriteHeader(http.StatusOK)
					w.Write([]byte("ok\n"))
				}
			}
		}
		s.HandleFunc("/100ms", makeSlowAPIHandler(100*time.Millisecond)).Methods("GET")
		s.HandleFunc("/30s", makeSlowAPIHandler(30*time.Second)).Methods("GET")

		body, code := run("GET", "/100ms", "")
		assert.Equal(t, "ok\n", body)
		assert.Equal(t, http.StatusOK, code)

		body, code = run("GET", "/30s", "")
		assert.Contains(t, body, "Request timed out")
		assert.Equal(t, http.StatusServiceUnavailable, code)
	}
}

func TestHTTPServiceStart(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())

	get := func(c *http.Client, url string) (string, int) {
		resp, err := c.Get(url)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return string(body), resp.StatusCode
	}

	{
		ctx, cancel := context.WithCancel(ctx)

		socketDir, err := os.MkdirTemp("/tmp", "nvcf")
		require.NoError(t, err)
		t.Cleanup(func() { os.RemoveAll(socketDir) })
		socketPath := filepath.Join(socketDir, "s.sock")

		s := NewHTTPService("unix://" + socketPath)
		s.AddHealthRoute(ctx)
		s.Use(NewHTTPMiddleware(ctx)...)
		s.Start(ctx)

		c := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		}
		body, code := get(c, "http://localhost/healthz")
		assert.Equal(t, "ok\n", body)
		assert.Equal(t, http.StatusOK, code)

		cancel()
	}

	{
		ctx, cancel := context.WithCancel(ctx)
		s := NewHTTPService("0.0.0.0:0")
		s.AddHealthRoute(ctx)
		s.Use(NewHTTPMiddleware(ctx)...)
		ln, err := s.Start(ctx)
		assert.NoError(t, err)

		t.Logf("addr: %s", ln.Addr().(*net.TCPAddr).String())
		addr := ln.Addr().(*net.TCPAddr).String()

		c := &http.Client{Timeout: 10 * time.Second}
		body, code := get(c, fmt.Sprintf("http://%s/healthz", addr))
		assert.Equal(t, "ok\n", body)
		assert.Equal(t, http.StatusOK, code)

		cancel()
	}

	{
		caCert, srvCertFile, srvKeyFile := certSetup(t)

		ctx, cancel := context.WithCancel(ctx)
		s := NewHTTPService("localhost:0")
		s.TLSCertFile, s.TLSKeyFile = srvCertFile, srvKeyFile
		s.AddHealthRoute(ctx)
		s.Use(NewHTTPMiddleware(ctx)...)
		ln, err := s.Start(ctx)
		assert.NoError(t, err)

		t.Logf("addr: %s", ln.Addr().(*net.TCPAddr).String())
		port := ln.Addr().(*net.TCPAddr).Port

		rootCAs := x509.NewCertPool()
		appendedCA := rootCAs.AppendCertsFromPEM([]byte(caCert))
		require.True(t, appendedCA)

		trpt := http.DefaultTransport.(*http.Transport)
		trpt.TLSClientConfig = &tls.Config{
			RootCAs: rootCAs,
		}
		c := &http.Client{Transport: trpt}

		body, code := get(c, fmt.Sprintf("https://localhost:%d/healthz", port))
		assert.Equal(t, "ok\n", body)
		assert.Equal(t, http.StatusOK, code)

		cancel()
	}
}

func certSetup(t *testing.T) (caCert, srvCert, srvKey string) {
	t.Helper()

	getBaseCertTemplate := func(t *testing.T, cn string, dnsNames []string) *x509.Certificate {
		t.Helper()
		serialNumberUpperBound := new(big.Int).Lsh(big.NewInt(1), 128)
		serialNumber, err := rand.Int(rand.Reader, serialNumberUpperBound)
		require.NoError(t, err)
		return &x509.Certificate{
			SerialNumber: serialNumber,
			Subject: pkix.Name{
				Organization: []string{"nvidia"},
				CommonName:   cn,
			},
			DNSNames:  dnsNames,
			NotBefore: time.Now(),
			NotAfter:  time.Now().Add(time.Hour * 24 * 365),
			KeyUsage:  x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{
				x509.ExtKeyUsageServerAuth,
				x509.ExtKeyUsageClientAuth,
			},
			BasicConstraintsValid: true,
		}
	}

	ca := getBaseCertTemplate(t, "my-self-signing-ca", nil)
	ca.IsCA = true
	ca.KeyUsage |= x509.KeyUsageCertSign

	// rsa keypair
	caPrivKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// create ca cert
	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, caPrivKey.Public(), caPrivKey)
	require.NoError(t, err)

	// pem encode
	caPEM := &bytes.Buffer{}
	err = pem.Encode(caPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caBytes,
	})
	require.NoError(t, err)

	svcDNSNames := []string{"localhost"}
	// create tls certificate
	cert := getBaseCertTemplate(t, svcDNSNames[0], svcDNSNames)

	certPrivKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	certBytes, err := x509.CreateCertificate(rand.Reader, cert, ca, certPrivKey.Public(), caPrivKey)
	require.NoError(t, err)

	srvCertFile, err := os.CreateTemp(t.TempDir(), "")
	require.NoError(t, err)
	srvCertFileName := srvCertFile.Name()
	err = pem.Encode(srvCertFile, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})
	srvCertFile.Close()
	require.NoError(t, err)

	srvKeyFile, err := os.CreateTemp(t.TempDir(), "")
	require.NoError(t, err)
	srvKeyFileName := srvKeyFile.Name()
	err = pem.Encode(srvKeyFile, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(certPrivKey),
	})
	srvKeyFile.Close()
	require.NoError(t, err)

	return caPEM.String(), srvCertFileName, srvKeyFileName
}

func Test_telemetryMiddleware(t *testing.T) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())

	router := mux.NewRouter()
	router.Use(NewHTTPMiddleware(context.Background(), WithTelemetry("svc", attribute.Bool("foo-bar", false)))...)
	router.HandleFunc("/test-with-telemetry", func(rw http.ResponseWriter, r *http.Request) {
		// Pull the span from the context should be a noop
		tSpan := trace.SpanFromContext(r.Context())
		assert.True(t, tSpan.SpanContext().HasSpanID())
	})
	router.HandleFunc(httpHealthRoutePath, func(rw http.ResponseWriter, r *http.Request) {
		// Pull the span from the context should be a noop
		tSpan := trace.SpanFromContext(r.Context())
		assert.False(t, tSpan.SpanContext().HasSpanID())
	})

	r := httptest.NewRequest("GET", "/test-with-telemetry", nil)
	router.ServeHTTP(httptest.NewRecorder(), r)

	r = httptest.NewRequest("GET", httpHealthRoutePath, nil)
	router.ServeHTTP(httptest.NewRecorder(), r)
}

func Test_recordingResponseWriter(t *testing.T) {
	// make sure the recordingResponseWriter preserves interfaces implemented by the wrapped writer
	router := mux.NewRouter()

	// Ensure response is properly recorded
	router.HandleFunc("/test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	r := httptest.NewRequest("GET", "/test", nil)
	w1 := getRRW(httptest.NewRecorder())
	defer putRRW(w1)

	router.ServeHTTP(w1.writer, r)
	assert.Equal(t, http.StatusTeapot, w1.statusCode)
}

func TestHTTPCodeError(t *testing.T) {
	assert.Equal(t, HTTPCodeError(0).Error(), "unexpected HTTP status code 0")
	assert.Equal(t, HTTPCodeError(400).Error(), "unexpected HTTP status code 400")
	expErr := HTTPCodeError(0)
	assert.ErrorAs(t, fmt.Errorf("foobar %w", HTTPCodeError(400)), &expErr)
}

func TestWithPrometheusRegisterer(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())

	// Create a custom registry
	registry := prometheus.NewRegistry()

	// Create a new HTTP service with custom registerer
	router := mux.NewRouter()
	router.Use(NewHTTPMiddleware(ctx,
		WithRequestMetrics("custom_metrics"),
		WithPrometheusRegisterer(registry))...)

	router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok\n"))
	}).Methods("GET")

	router.HandleFunc("/error", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error\n"))
	}).Methods("GET")

	// Make some requests
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	req := httptest.NewRequest("GET", "/error", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	// Verify metrics are in the custom registry
	metricFamilies, err := registry.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, metricFamilies)

	// Check that our custom metrics exist
	var foundDuration, foundCount bool
	for _, mf := range metricFamilies {
		if mf.GetName() == "custom_metrics_http_duration_seconds" {
			foundDuration = true
			// Verify we have metrics for both /test and /error paths
			assert.True(t, len(mf.GetMetric()) >= 2, "Expected metrics for /test and /error")
		}
		if mf.GetName() == "custom_metrics_http_request_counts" {
			foundCount = true
			// Verify we have counts for different status codes
			assert.True(t, len(mf.GetMetric()) >= 2, "Expected count metrics for different endpoints")
		}
	}

	assert.True(t, foundDuration, "Should find custom_metrics_http_duration_seconds in custom registry")
	assert.True(t, foundCount, "Should find custom_metrics_http_request_counts in custom registry")
}

func TestHTTPAdminHandler_NormalResponse(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())

	// Test that the handler handles normal cases gracefully
	handler := HTTPAdminHandler(ctx)
	req := httptest.NewRequest("GET", "/admin", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "log-level")
}

func TestWithPrometheusRegisterer_DefaultRegistry(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())

	// Create service without custom registerer (should use default)
	router := mux.NewRouter()
	router.Use(NewHTTPMiddleware(ctx, WithRequestMetrics("default_test_metrics"))...)

	router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok\n"))
	}).Methods("GET")

	// Make a request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// This test just verifies no panic occurs when using default registry
	// We can't easily verify the metrics are in prometheus.DefaultRegisterer
	// without affecting other tests, but the lack of panic is sufficient
}

func TestWithPrometheusRegisterer_NoMetrics(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())

	// Create a custom registry
	registry := prometheus.NewRegistry()

	// Create service WITHOUT WithRequestMetrics - metrics should not be created
	router := mux.NewRouter()
	router.Use(NewHTTPMiddleware(ctx, WithPrometheusRegisterer(registry))...)

	router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok\n"))
	}).Methods("GET")

	// Make a request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Verify no metrics were created in the custom registry
	metricFamilies, err := registry.Gather()
	require.NoError(t, err)
	assert.Empty(t, metricFamilies, "Should not have any metrics when WithRequestMetrics is not used")
}

func TestHTTPAdminHandler_WriteError(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	handler := HTTPAdminHandler(ctx)

	// Create a recorder that will fail on write
	req := httptest.NewRequest("GET", "/admin", nil)
	w := &brokenWriter{}

	// This should trigger the error path in Write
	handler.ServeHTTP(w, req)

	// The handler should attempt to write and encounter the error
	assert.True(t, w.writeCalled)
}

// brokenWriter is a ResponseWriter that fails on Write.
// writeCount tracks how many times Write has been called;
// since every call returns an error, successCount is always 0.
type brokenWriter struct {
	writeCalled bool
	writeCount  int
	headerCode  int
}

func (b *brokenWriter) Header() http.Header {
	return http.Header{}
}

func (b *brokenWriter) Write(data []byte) (int, error) {
	b.writeCalled = true
	b.writeCount++
	return 0, fmt.Errorf("write error")
}

func (b *brokenWriter) WriteHeader(statusCode int) {
	b.headerCode = statusCode
}
