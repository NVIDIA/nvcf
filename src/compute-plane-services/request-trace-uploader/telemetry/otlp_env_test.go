// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// writeTLSFiles writes a self-signed certificate and its key, usable both
// as a CA file and as a client certificate pair.
func writeTLSFiles(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// TestOTLPIgnoresEveryOtherExporterVariable sets, one case at a time, each
// OTEL_EXPORTER_OTLP_* variable other than the metrics endpoint and
// protocol, to a value that would change or break the export if it were
// honored, and checks the export is unchanged over both protocols.
func TestOTLPIgnoresEveryOtherExporterVariable(t *testing.T) {
	cert, key := writeTLSFiles(t)
	decoy, decoyEndpoint := httpReceiver(t)
	decoyBase := strings.TrimSuffix(decoyEndpoint, "/v1/metrics")
	cases := map[string]map[string]string{
		"endpoint":                      {"OTEL_EXPORTER_OTLP_ENDPOINT": decoyBase},
		"protocol":                      {"OTEL_EXPORTER_OTLP_PROTOCOL": "http/json"},
		"certificate":                   {"OTEL_EXPORTER_OTLP_CERTIFICATE": cert},
		"metrics certificate":           {"OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE": cert},
		"client certificate":            {"OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE": cert, "OTEL_EXPORTER_OTLP_CLIENT_KEY": key},
		"metrics client certificate":    {"OTEL_EXPORTER_OTLP_METRICS_CLIENT_CERTIFICATE": cert, "OTEL_EXPORTER_OTLP_METRICS_CLIENT_KEY": key},
		"insecure":                      {"OTEL_EXPORTER_OTLP_INSECURE": "false"},
		"metrics insecure":              {"OTEL_EXPORTER_OTLP_METRICS_INSECURE": "false"},
		"headers":                       {"OTEL_EXPORTER_OTLP_HEADERS": "x-env-marker=1"},
		"metrics headers":               {"OTEL_EXPORTER_OTLP_METRICS_HEADERS": "x-env-marker=1"},
		"compression":                   {"OTEL_EXPORTER_OTLP_COMPRESSION": "gzip"},
		"metrics compression":           {"OTEL_EXPORTER_OTLP_METRICS_COMPRESSION": "gzip"},
		"timeout":                       {"OTEL_EXPORTER_OTLP_TIMEOUT": "1"},
		"metrics timeout":               {"OTEL_EXPORTER_OTLP_METRICS_TIMEOUT": "1"},
		"temporality":                   {"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "delta"},
		"default histogram aggregation": {"OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION": "base2_exponential_bucket_histogram"},
	}
	receivers := map[Protocol]func(*testing.T) (*receiver, string){
		ProtocolHTTP: httpReceiver,
		ProtocolGRPC: grpcReceiver,
	}
	for name, env := range cases {
		for protocol, start := range receivers {
			t.Run(name+"/"+string(protocol), func(t *testing.T) {
				for k, v := range env {
					t.Setenv(k, v)
				}
				r, endpoint := start(t)
				exporter, err := NewOTLP(context.Background(), OTLPConfig{Endpoint: endpoint, Protocol: protocol, Interval: time.Hour}, "fake")
				if err != nil {
					t.Fatalf("NewOTLP() error = %v", err)
				}
				defer func() { _ = exporter.Shutdown(context.Background()) }()

				exporter.Add(SubmitsAmbiguous, 1, nil)
				exporter.Observe(UploadDuration, 1, nil)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := exporter.Flush(ctx); err != nil {
					t.Fatalf("Flush() error = %v", err)
				}

				sum := r.metric(MetricPrefix + SubmitsAmbiguous).GetSum()
				if sum.GetAggregationTemporality() != metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE || len(sum.GetDataPoints()) != 1 || sum.GetDataPoints()[0].GetAsDouble() != 1 {
					t.Fatalf("submits_ambiguous = %v, want a cumulative 1", sum)
				}
				if r.metric(MetricPrefix+UploadDuration).GetHistogram() == nil {
					t.Fatal("upload.duration is not an explicit-bucket histogram")
				}
				r.mu.Lock()
				for _, headers := range r.headers {
					if _, ok := headers["x-env-marker"]; ok {
						t.Error("an export carries a header from the environment")
					}
				}
				r.mu.Unlock()
				if decoy.count() != 0 {
					t.Fatalf("the generic endpoint got %d exports, want none", decoy.count())
				}
			})
		}
	}
}
