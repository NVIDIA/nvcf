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

package tlsreload

import (
	"bytes"
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
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestNewLoadsPair(t *testing.T) {
	t.Parallel()

	certFile, keyFile := pairPaths(t)
	_, keyPEM := writePair(t, certFile, keyFile, 1)

	var logs bytes.Buffer
	l, err := New(certFile, keyFile, WithLogger(zerolog.New(&logs)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := servedSerial(t, l); got != 1 {
		t.Fatalf("served serial = %d, want 1", got)
	}
	if l.interval != DefaultInterval {
		t.Fatalf("interval = %s, want %s", l.interval, DefaultInterval)
	}

	out := logs.String()
	for _, want := range []string{`"level":"info"`, "loaded tls certificate", `"subject":"CN=gateway.test"`, `"not_after"`, `"serial":"1"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("logs missing %s:\n%s", want, out)
		}
	}
	assertNoKeyMaterial(t, out, keyPEM)
}

func TestWithInterval(t *testing.T) {
	t.Parallel()

	certFile, keyFile := pairPaths(t)
	writePair(t, certFile, keyFile, 1)

	for _, tc := range []struct {
		in, want time.Duration
	}{
		{in: 5 * time.Second, want: 5 * time.Second},
		{in: 0, want: DefaultInterval},
		{in: -time.Second, want: DefaultInterval},
	} {
		l, err := New(certFile, keyFile, WithInterval(tc.in), WithLogger(zerolog.Nop()))
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if l.interval != tc.want {
			t.Fatalf("WithInterval(%s): interval = %s, want %s", tc.in, l.interval, tc.want)
		}
	}
}

func TestNewFailsWithoutLoadablePair(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writePair(t, certFile, keyFile, 1)
	_, otherKeyPEM := generatePair(t, 2)
	otherKey := filepath.Join(dir, "other.key")
	garbage := filepath.Join(dir, "garbage.crt")
	writeFile(t, otherKey, otherKeyPEM)
	writeFile(t, garbage, []byte("not a certificate"))

	tests := []struct {
		name     string
		certFile string
		keyFile  string
	}{
		{name: "empty paths"},
		{name: "missing cert", certFile: filepath.Join(dir, "missing.crt"), keyFile: keyFile},
		{name: "missing key", certFile: certFile, keyFile: filepath.Join(dir, "missing.key")},
		{name: "garbage cert", certFile: garbage, keyFile: keyFile},
		{name: "key from another pair", certFile: certFile, keyFile: otherKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			l, err := New(tc.certFile, tc.keyFile, WithLogger(zerolog.Nop()))
			if err == nil || l != nil {
				t.Fatalf("New() = %v, %v, want an error and no loader", l, err)
			}
		})
	}
}

func TestReloadOnceFollowsFileChanges(t *testing.T) {
	t.Parallel()

	certFile, keyFile := pairPaths(t)
	writePair(t, certFile, keyFile, 1)
	var logs bytes.Buffer
	l, err := New(certFile, keyFile, WithLogger(zerolog.New(&logs)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx := context.Background()

	if got := l.reloadOnce(ctx); got != reloadUnchanged {
		t.Fatalf("reload without a change = %v, want unchanged", got)
	}

	_, keyPEM := writePair(t, certFile, keyFile, 2)
	if got := l.reloadOnce(ctx); got != reloadSucceeded {
		t.Fatalf("reload after renewal = %v, want succeeded", got)
	}
	if got := servedSerial(t, l); got != 2 {
		t.Fatalf("served serial = %d, want 2", got)
	}
	if got := l.reloadOnce(ctx); got != reloadUnchanged {
		t.Fatalf("second reload of the same files = %v, want unchanged", got)
	}

	// A change to the key file alone also counts.
	bumpModTime(t, keyFile)
	if got := l.reloadOnce(ctx); got != reloadSucceeded {
		t.Fatalf("reload after key change = %v, want succeeded", got)
	}
	if got := strings.Count(logs.String(), "loaded tls certificate"); got != 3 {
		t.Fatalf("load logs = %d, want 3:\n%s", got, logs.String())
	}
	assertNoKeyMaterial(t, logs.String(), keyPEM)
}

// TestReloadFollowsKubernetesSecretSwap mirrors how the kubelet updates a
// Secret volume: tls.crt and tls.key are symlinks through ..data, which is
// atomically repointed at a directory holding the new files.
func TestReloadFollowsKubernetesSecretSwap(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	publish := func(version string, serial int64) {
		t.Helper()
		versionDir := filepath.Join(dir, version)
		if err := os.Mkdir(versionDir, 0o755); err != nil {
			t.Fatal(err)
		}
		certPEM, keyPEM := generatePair(t, serial)
		// Pin distinct times so the test does not depend on clock resolution.
		modTime := time.Unix(1_700_000_000+serial, 0)
		for name, data := range map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM} {
			path := filepath.Join(versionDir, name)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, modTime, modTime); err != nil {
				t.Fatal(err)
			}
		}
		tmp := filepath.Join(dir, "..data_tmp")
		if err := os.Symlink(version, tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "..data")); err != nil {
			t.Fatal(err)
		}
	}

	publish("..2026_09_29_00_00_00.000000001", 1)
	for _, name := range []string{"tls.crt", "tls.key"} {
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	l, err := New(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), WithLogger(zerolog.Nop()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := servedSerial(t, l); got != 1 {
		t.Fatalf("served serial = %d, want 1", got)
	}

	publish("..2026_09_29_01_00_00.000000002", 2)
	if got := l.reloadOnce(context.Background()); got != reloadSucceeded {
		t.Fatalf("reload after the Secret update = %v, want succeeded", got)
	}
	if got := servedSerial(t, l); got != 2 {
		t.Fatalf("served serial = %d, want 2", got)
	}
	if got := l.reloadOnce(context.Background()); got != reloadUnchanged {
		t.Fatalf("reload without a further update = %v, want unchanged", got)
	}
}

func TestRejectedReloadKeepsLastGoodPair(t *testing.T) {
	t.Parallel()

	certFile, keyFile := pairPaths(t)
	_, keyPEM := writePair(t, certFile, keyFile, 1)
	var logs bytes.Buffer
	l, err := New(certFile, keyFile, WithLogger(zerolog.New(&logs)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx := context.Background()

	rejectN := func(step string, n, wantWarnings int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if got := l.reloadOnce(ctx); got != reloadRejected {
				t.Fatalf("%s: reload %d = %v, want rejected", step, i, got)
			}
		}
		if got := servedSerial(t, l); got != 1 {
			t.Fatalf("%s: served serial = %d, want the last good 1", step, got)
		}
		if got := strings.Count(logs.String(), `"level":"warn"`); got != wantWarnings {
			t.Fatalf("%s: warnings = %d, want %d:\n%s", step, got, wantWarnings, logs.String())
		}
	}

	// A broken file is retried on every check but logged once.
	writeFile(t, certFile, []byte("not a certificate"))
	rejectN("garbage cert", 3, 1)
	if !strings.Contains(logs.String(), "failed to find any PEM data") ||
		!strings.Contains(logs.String(), `"serving_not_after"`) {
		t.Fatalf("warning does not explain the rejection:\n%s", logs.String())
	}

	// A different error is logged once more.
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	rejectN("missing key", 2, 2)

	certPEM, _ := generatePair(t, 3)
	_, otherKeyPEM := generatePair(t, 4)
	writeFile(t, certFile, certPEM)
	writeFile(t, keyFile, otherKeyPEM)
	rejectN("mismatched pair", 2, 3)

	writePair(t, certFile, keyFile, 5)
	if got := l.reloadOnce(ctx); got != reloadSucceeded {
		t.Fatalf("reload of a good pair = %v, want succeeded", got)
	}
	if got := servedSerial(t, l); got != 5 {
		t.Fatalf("served serial = %d, want 5", got)
	}

	// After a success the same error is news again.
	writeFile(t, certFile, []byte("not a certificate"))
	if got := l.reloadOnce(ctx); got != reloadRejected {
		t.Fatalf("reload of garbage after recovery = %v, want rejected", got)
	}
	if got := strings.Count(logs.String(), `"level":"warn"`); got != 4 {
		t.Fatalf("warnings = %d, want 4:\n%s", got, logs.String())
	}
	assertNoKeyMaterial(t, logs.String(), keyPEM)
}

func TestRecurringErrorIsLoggedAgainAfterFilesRecover(t *testing.T) {
	t.Parallel()

	certFile, keyFile := pairPaths(t)
	certPEM, _ := writePair(t, certFile, keyFile, 1)
	original, err := os.Stat(certFile)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	l, err := New(certFile, keyFile, WithLogger(zerolog.New(&logs)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx := context.Background()

	writeFile(t, certFile, []byte("not a certificate"))
	if got := l.reloadOnce(ctx); got != reloadRejected {
		t.Fatalf("reload of garbage = %v, want rejected", got)
	}

	// Put back exactly the served files: nothing to reload.
	writeFile(t, certFile, certPEM)
	if err := os.Chtimes(certFile, original.ModTime(), original.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := l.reloadOnce(ctx); got != reloadUnchanged {
		t.Fatalf("reload of the restored files = %v, want unchanged", got)
	}

	writeFile(t, certFile, []byte("not a certificate"))
	if got := l.reloadOnce(ctx); got != reloadRejected {
		t.Fatalf("second reload of garbage = %v, want rejected", got)
	}
	if got := strings.Count(logs.String(), `"level":"warn"`); got != 2 {
		t.Fatalf("warnings = %d, want 2 (one per broken episode):\n%s", got, logs.String())
	}
}

func TestRunServesRenewalsToConcurrentReaders(t *testing.T) {
	t.Parallel()

	certFile, keyFile := pairPaths(t)
	writePair(t, certFile, keyFile, 1)
	l, err := New(certFile, keyFile, WithInterval(time.Millisecond), WithLogger(zerolog.Nop()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		l.Run(ctx)
	}()

	const lastSerial = 6
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readerErrs := make(chan string, 8)
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			var last int64
			for {
				select {
				case <-stop:
					return
				default:
				}
				cert, err := l.GetCertificate(nil)
				if err != nil || cert == nil || cert.Leaf == nil {
					readerErrs <- "GetCertificate returned no certificate"
					return
				}
				if !bytes.Equal(cert.Leaf.Raw, cert.Certificate[0]) {
					readerErrs <- "leaf does not match the served chain"
					return
				}
				serial := cert.Leaf.SerialNumber.Int64()
				if serial < last || serial > lastSerial {
					readerErrs <- "served serials went backwards or out of range"
					return
				}
				last = serial
			}
		}()
	}

	for serial := int64(2); serial <= lastSerial; serial++ {
		writePair(t, certFile, keyFile, serial)
		waitForSerial(t, l, serial)
	}

	close(stop)
	readers.Wait()
	cancel()
	<-runDone
	close(readerErrs)
	for msg := range readerErrs {
		t.Error(msg)
	}
}

// TestReloadMetrics swaps the global meter provider, so it must not run in
// parallel with other tests.
func TestReloadMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	certFile, keyFile := pairPaths(t)
	writePair(t, certFile, keyFile, 1)
	l, err := New(certFile, keyFile, WithLogger(zerolog.Nop()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	first := l.current.Load().Leaf.NotAfter

	expiry, reloads := collectTLSMetrics(t, reader)
	if expiry != first.Unix() {
		t.Fatalf("expiry after initial load = %d, want %d", expiry, first.Unix())
	}
	if len(reloads) != 0 {
		t.Fatalf("reloads after initial load = %v, want none", reloads)
	}

	writePair(t, certFile, keyFile, 2)
	l.reloadOnce(context.Background())
	second := l.current.Load().Leaf.NotAfter
	if !second.After(first) {
		t.Fatalf("renewed NotAfter %s is not after %s", second, first)
	}

	writeFile(t, certFile, []byte("not a certificate"))
	l.reloadOnce(context.Background())
	l.reloadOnce(context.Background())

	expiry, reloads = collectTLSMetrics(t, reader)
	if expiry != second.Unix() {
		t.Fatalf("expiry = %d, want the renewed %d", expiry, second.Unix())
	}
	if reloads["success"] != 1 || reloads["rejected"] != 2 {
		t.Fatalf("reloads = %v, want success=1 rejected=2", reloads)
	}
}

func collectTLSMetrics(t *testing.T, reader *sdkmetric.ManualReader) (expiry int64, reloads map[string]int64) {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	reloads = map[string]int64{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch m.Name {
			case "llm_api_gateway_tls_certificate_expiry_seconds":
				gauge, ok := m.Data.(metricdata.Gauge[int64])
				if !ok || len(gauge.DataPoints) != 1 {
					t.Fatalf("expiry data = %#v, want one int64 gauge point", m.Data)
				}
				expiry = gauge.DataPoints[0].Value
			case "llm_api_gateway_tls_reloads_total":
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("reloads data = %#v, want an int64 sum", m.Data)
				}
				for _, point := range sum.DataPoints {
					outcome, _ := point.Attributes.Value(attribute.Key("outcome"))
					reloads[outcome.AsString()] += point.Value
				}
			}
		}
	}
	return expiry, reloads
}

func pairPaths(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
}

// writePair writes a new self-signed pair over certFile and keyFile.
func writePair(t *testing.T, certFile, keyFile string, serial int64) (certPEM, keyPEM []byte) {
	t.Helper()
	certPEM, keyPEM = generatePair(t, serial)
	writeFile(t, certFile, certPEM)
	writeFile(t, keyFile, keyPEM)
	return certPEM, keyPEM
}

// generatePair returns a self-signed certificate with the given serial whose
// NotAfter grows with the serial.
func generatePair(t *testing.T, serial int64) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "gateway.test"},
		DNSNames:     []string{"gateway.test"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Duration(serial) * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// writeFile replaces path with data. An existing file's modification time
// moves forward by a second so the change is visible regardless of the
// filesystem's timestamp resolution.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	previous, statErr := os.Stat(path)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if statErr == nil {
		next := previous.ModTime().Add(time.Second)
		if err := os.Chtimes(path, next, next); err != nil {
			t.Fatal(err)
		}
	}
}

func bumpModTime(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	next := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, next, next); err != nil {
		t.Fatal(err)
	}
}

func servedSerial(t *testing.T, l *Loader) int64 {
	t.Helper()
	cert, err := l.GetCertificate(nil)
	if err != nil || cert == nil || cert.Leaf == nil {
		t.Fatalf("GetCertificate() = %v, %v, want a certificate with a leaf", cert, err)
	}
	return cert.Leaf.SerialNumber.Int64()
}

func waitForSerial(t *testing.T, l *Loader, serial int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for servedSerial(t, l) != serial {
		if time.Now().After(deadline) {
			t.Fatalf("served serial = %d, want %d within 5s", servedSerial(t, l), serial)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertNoKeyMaterial(t *testing.T, logs string, keyPEM []byte) {
	t.Helper()
	if strings.Contains(logs, "PRIVATE KEY") || strings.Contains(logs, string(keyPEM)) {
		t.Fatalf("logs contain private key material:\n%s", logs)
	}
}
