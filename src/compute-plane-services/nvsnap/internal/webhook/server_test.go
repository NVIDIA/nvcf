/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// generateCertOnDisk produces tls.crt+tls.key in a tempdir suitable for the Server.
func generateCertOnDisk(t *testing.T) (certFile, keyFile string, caBundle []byte) {
	t.Helper()
	gc, err := GenerateSelfSigned(CertOptions{
		CommonName: "localhost",
		DNSNames:   []string{"localhost", "127.0.0.1"},
		Validity:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile, err = gc.WriteToDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, gc.CABundle
}

func TestGenerateSelfSigned_BasicShape(t *testing.T) {
	gc, err := GenerateSelfSigned(CertOptions{
		DNSNames: []string{"nvsnap-webhook.nvsnap-system.svc"},
		Validity: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gc.CertPEM) == 0 || len(gc.KeyPEM) == 0 || len(gc.CABundle) == 0 {
		t.Fatalf("PEMs should be non-empty: cert=%d key=%d ca=%d",
			len(gc.CertPEM), len(gc.KeyPEM), len(gc.CABundle))
	}
	// Parse + verify SAN.
	cert, err := tls.X509KeyPair(gc.CertPEM, gc.KeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Subject.CommonName != "nvsnap-webhook.nvsnap-system.svc" {
		t.Errorf("CN should default to first DNSName; got %q", parsed.Subject.CommonName)
	}
	found := false
	for _, n := range parsed.DNSNames {
		if n == "nvsnap-webhook.nvsnap-system.svc" {
			found = true
		}
	}
	if !found {
		t.Errorf("Service DNS not in SAN dnsNames: %v", parsed.DNSNames)
	}
}

func TestGenerateSelfSigned_RequiresDNSNames(t *testing.T) {
	_, err := GenerateSelfSigned(CertOptions{})
	if err == nil {
		t.Fatal("expected error for empty DNSNames")
	}
}

func TestWriteToDir_FilesExistAndReadable(t *testing.T) {
	gc, _ := GenerateSelfSigned(CertOptions{DNSNames: []string{"x"}, Validity: time.Hour})
	dir := t.TempDir()
	certPath, keyPath, err := gc.WriteToDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(certPath) != dir {
		t.Errorf("cert not in dir: %s", certPath)
	}
	for _, p := range []string{certPath, keyPath} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() == 0 {
			t.Fatalf("%s is empty", p)
		}
	}
}

func TestServer_RunServesAdmissionReviewOverTLS(t *testing.T) {
	certFile, keyFile, caBundle := generateCertOnDisk(t)

	// Backend isn't strictly needed for the no-annotation pass-through test.
	h := &Handler{Mutator: &Mutator{}}
	srv := NewServer(ServerConfig{
		ListenAddr: "127.0.0.1:0", // any free port
		CertFile:   certFile,
		KeyFile:    keyFile,
		Path:       "/mutate",
	}, h)

	// We can't ask the http.Server for its actual listen addr after Run,
	// because Run uses ListenAndServeTLS. Use a fixed high port.
	addr := "127.0.0.1:18443"
	srv.cfg.ListenAddr = addr
	srv.httpSrv.Addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := srv.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logf("Run returned: %v", err)
		}
	}()
	defer cancel()

	// Build an HTTP client that trusts the self-signed CA.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caBundle)
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12},
		},
	}

	// Wait briefly for the server to start listening.
	if !waitFor(t, 2*time.Second, func() bool {
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test-only liveness probe against the self-signed test server
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}) {
		t.Fatalf("server never started listening on %s", addr)
	}

	// POST an admission request — no annotation, expect allowed=true.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
	}
	podBytes, _ := json.Marshal(pod)
	body, _ := json.Marshal(admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:    types.UID("uid-x"),
			Object: runtime.RawExtension{Raw: podBytes},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+"/mutate", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var review admissionv1.AdmissionReview
	if derr := json.NewDecoder(resp.Body).Decode(&review); derr != nil {
		t.Fatal(derr)
	}
	if review.Response == nil || !review.Response.Allowed {
		t.Fatalf("expected allowed=true; got %+v", review.Response)
	}

	// Healthz should also work.
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/healthz", http.NoBody)
	if err != nil {
		t.Fatalf("new healthz request: %v", err)
	}
	hresp, err := client.Do(hreq)
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	_ = hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", hresp.StatusCode)
	}
}

func TestServer_GracefulShutdown(t *testing.T) {
	certFile, keyFile, _ := generateCertOnDisk(t)
	srv := NewServer(ServerConfig{
		ListenAddr: "127.0.0.1:18444",
		CertFile:   certFile,
		KeyFile:    keyFile,
	}, &Handler{Mutator: &Mutator{}})

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run(ctx) }()

	// Wait for listening (best-effort).
	if !waitFor(t, 2*time.Second, func() bool {
		conn, err := tls.Dial("tcp", "127.0.0.1:18444", &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test-only liveness probe against the self-signed test server
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}) {
		t.Fatal("server never started")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("graceful shutdown returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5s after ctx cancel")
	}
}

func TestServer_ValidateRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  ServerConfig
		h    *Handler
	}{
		{name: "nil handler", cfg: ServerConfig{CertFile: "x", KeyFile: "y"}, h: nil},
		{name: "missing cert", cfg: ServerConfig{KeyFile: "y"}, h: &Handler{}},
		{name: "missing key", cfg: ServerConfig{CertFile: "x"}, h: &Handler{}},
		{name: "cert file does not exist", cfg: ServerConfig{CertFile: "/no/such", KeyFile: "/no/such"}, h: &Handler{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(tc.cfg, tc.h)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := srv.Run(ctx)
			if err == nil {
				t.Fatalf("expected error for bad config %s", tc.name)
			}
		})
	}
}

// waitFor polls cond every 25ms until it returns true or timeout.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return cond()
}
