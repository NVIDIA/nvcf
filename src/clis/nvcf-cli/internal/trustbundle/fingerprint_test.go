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

package trustbundle

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"regexp"
	"testing"
	"time"
)

var fpRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// makeCertPEM generates a self-signed CERTIFICATE PEM block with the given CN
// and serial, so tests have distinct, valid DER to hash.
func makeCertPEM(t *testing.T, cn string, serial int64) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func mustFP(t *testing.T, b []byte) string {
	t.Helper()
	fp, err := Fingerprint(b)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	return fp
}

func TestFingerprint_FormatAndDeterministic(t *testing.T) {
	a := makeCertPEM(t, "a", 1)
	fp := mustFP(t, a)
	if !fpRe.MatchString(fp) {
		t.Fatalf("unexpected format: %q", fp)
	}
	if again := mustFP(t, a); again != fp {
		t.Fatalf("not deterministic: %q != %q", fp, again)
	}
}

func TestFingerprint_OrderInsensitive(t *testing.T) {
	a := makeCertPEM(t, "a", 1)
	b := makeCertPEM(t, "b", 2)
	if mustFP(t, concat(a, b)) != mustFP(t, concat(b, a)) {
		t.Fatal("fingerprint must be independent of certificate order")
	}
}

func TestFingerprint_DuplicateInsensitive(t *testing.T) {
	a := makeCertPEM(t, "a", 1)
	if mustFP(t, concat(a, a, a)) != mustFP(t, a) {
		t.Fatal("fingerprint must be independent of duplicate certificates")
	}
}

func TestFingerprint_WhitespaceAndCommentInsensitive(t *testing.T) {
	a := makeCertPEM(t, "a", 1)
	b := makeCertPEM(t, "b", 2)
	clean := concat(a, b)
	noisy := concat(
		[]byte("# leading comment\n\n"),
		a,
		[]byte("\n\n   \n# between blocks\n"),
		b,
		[]byte("\n"),
	)
	if mustFP(t, clean) != mustFP(t, noisy) {
		t.Fatal("fingerprint must ignore PEM whitespace and comments")
	}
}

func TestFingerprint_IgnoresNonCertificateBlocks(t *testing.T) {
	a := makeCertPEM(t, "a", 1)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if mustFP(t, concat(keyPEM, a)) != mustFP(t, a) {
		t.Fatal("non-CERTIFICATE PEM blocks must not affect the fingerprint")
	}
}

func TestFingerprint_EmptyOrNoCerts(t *testing.T) {
	for _, in := range [][]byte{nil, []byte(""), []byte("not pem at all"),
		[]byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n")} {
		if _, err := Fingerprint(in); !errors.Is(err, ErrNoCertificates) {
			t.Fatalf("expected ErrNoCertificates for %q, got %v", string(in), err)
		}
	}
}

func TestFingerprint_RejectsMalformedCertificate(t *testing.T) {
	bad := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-a-der-cert")})
	if _, err := Fingerprint(bad); err == nil {
		t.Fatal("expected error for malformed CERTIFICATE DER")
	}
}
