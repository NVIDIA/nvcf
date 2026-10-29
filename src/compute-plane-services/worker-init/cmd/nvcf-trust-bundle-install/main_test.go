// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/trustbundle"
)

func TestRunWritesMergedBundle(t *testing.T) {
	dir := t.TempDir()
	certificate := installerTestCertificatePEM(t)
	trustPath := filepath.Join(dir, "trust.pem")
	outputPath := filepath.Join(dir, "out", "ca-certificates.crt")
	if err := os.WriteFile(filepath.Join(dir, "system.pem"), []byte("system-root\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustPath, []byte(certificate), 0644); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := trustbundle.FingerprintPEM(certificate)
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if code := run([]string{
		"--system-bundle", filepath.Join(dir, "system.pem"),
		"--trust-bundle", trustPath,
		"--output-bundle", outputPath,
		"--expected-fingerprint", fingerprint,
	}, &stderr); code != 0 {
		t.Fatalf("run exit code = %d, stderr = %s", code, stderr.String())
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "system-root\n\n" + certificate; string(got) != want {
		t.Fatalf("installed bundle = %q, want %q", got, want)
	}
}

func TestRunRejectsMalformedPEM(t *testing.T) {
	dir := t.TempDir()
	trustPath := filepath.Join(dir, "trust.pem")
	if err := os.WriteFile(trustPath, []byte("not pem"), 0644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if code := run([]string{
		"--trust-bundle", trustPath,
		"--output-bundle", filepath.Join(dir, "out.pem"),
		"--expected-fingerprint", "sha256:" + string(bytes.Repeat([]byte("0"), 64)),
	}, &stderr); code != 1 {
		t.Fatalf("run exit code = %d, stderr = %s", code, stderr.String())
	}
}

func installerTestCertificatePEM(t *testing.T) string {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "NVCF worker-init installer test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
