// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package trustbundle

import (
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
)

func TestMergeFilesWritesValidatedMergedBundle(t *testing.T) {
	dir := t.TempDir()
	trustBundle := testCertificatePEM(t)
	trustPath := filepath.Join(dir, "trust.pem")
	outputPath := filepath.Join(dir, "out", "ca-certificates.crt")
	if err := os.WriteFile(filepath.Join(dir, "system.pem"), []byte("system-root\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustPath, []byte(trustBundle), 0644); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := FingerprintPEM(trustBundle)
	if err != nil {
		t.Fatal(err)
	}

	err = MergeFiles(InstallOptions{
		SystemBundlePath:    filepath.Join(dir, "system.pem"),
		TrustBundlePath:     trustPath,
		OutputBundlePath:    outputPath,
		ExpectedFingerprint: fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "system-root\n\n" + trustBundle; string(got) != want {
		t.Fatalf("merged bundle = %q, want %q", got, want)
	}
}

func TestMergeFilesAllowsMissingSystemBundle(t *testing.T) {
	dir := t.TempDir()
	trustBundle := testCertificatePEM(t)
	trustPath := filepath.Join(dir, "trust.pem")
	outputPath := filepath.Join(dir, "out", "ca-certificates.crt")
	if err := os.WriteFile(trustPath, []byte(trustBundle), 0644); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := FingerprintPEM(trustBundle)
	if err != nil {
		t.Fatal(err)
	}

	err = MergeFiles(InstallOptions{
		SystemBundlePath:    filepath.Join(dir, "missing-system.pem"),
		TrustBundlePath:     trustPath,
		OutputBundlePath:    outputPath,
		ExpectedFingerprint: fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := trustBundle; string(got) != want {
		t.Fatalf("merged bundle = %q, want %q", got, want)
	}
}

func TestFingerprintPEMIsCanonicalForDuplicateCertificates(t *testing.T) {
	certificate := testCertificatePEM(t)
	single, err := FingerprintPEM(certificate)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := FingerprintPEM(certificate + "\n" + certificate)
	if err != nil {
		t.Fatal(err)
	}
	if single != duplicate {
		t.Fatalf("duplicate fingerprint = %q, want %q", duplicate, single)
	}
}

func TestValidatePEMRejectsMalformedTrustBundles(t *testing.T) {
	for _, trustBundle := range []string{
		"not pem",
		"-----BEGIN PRIVATE KEY-----\nYWJj\n-----END PRIVATE KEY-----\n",
		"-----BEGIN CERTIFICATE-----\nnot-a-certificate\n-----END CERTIFICATE-----\n",
	} {
		if err := ValidatePEM(trustBundle); err == nil {
			t.Fatalf("ValidatePEM(%q) unexpectedly succeeded", trustBundle)
		}
	}
}

func TestMergeFilesDoesNotWriteOutputForFingerprintMismatch(t *testing.T) {
	dir := t.TempDir()
	trustPath := filepath.Join(dir, "trust.pem")
	outputPath := filepath.Join(dir, "out.pem")
	if err := os.WriteFile(trustPath, []byte(testCertificatePEM(t)), 0644); err != nil {
		t.Fatal(err)
	}

	err := MergeFiles(InstallOptions{
		TrustBundlePath:     trustPath,
		OutputBundlePath:    outputPath,
		ExpectedFingerprint: "sha256:" + strings.Repeat("0", 64),
	})
	if err == nil {
		t.Fatal("MergeFiles unexpectedly succeeded")
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("output should not exist after fingerprint mismatch: %v", statErr)
	}
}

func testCertificatePEM(t *testing.T) string {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "NVCF trust bundle test"},
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
