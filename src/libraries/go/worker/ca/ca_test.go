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

package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// genCertPEM returns a self-signed CA certificate in PEM form with the given
// common name so we can later assert it landed in the pool via Subjects().
func genCertPEM(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
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

// TestProxyCAs drives the single sync.Once-guarded build of the pool. Because
// the cache is process-global and sealed after the first invocation, all
// success-path inputs (SetExtraCAs, NVCF_PROXY_CA_FILE, NVCF_PROXY_CA_PEM) are
// staged here before the one ProxyCAs call this binary makes.
func TestProxyCAs(t *testing.T) {
	// The pool is a process-global, sealed-once singleton. Under go test
	// -count>1 it is already sealed from the first iteration, so SetExtraCAs
	// would panic. Skip rather than fail, making the build-once-per-process
	// constraint explicit instead of surprising a future contributor.
	mu.Lock()
	alreadySealed := sealed
	mu.Unlock()
	if alreadySealed {
		t.Skip("ca pool already sealed (e.g. go test -count>1); ProxyCAs builds once per process")
	}

	extraPEM := genCertPEM(t, "extra-ca-injected")
	SetExtraCAs(extraPEM)

	// Two colon-separated files plus one with surrounding whitespace and an
	// empty segment, to exercise the trim/skip-empty branches.
	fileCert1 := genCertPEM(t, "file-ca-1")
	fileCert2 := genCertPEM(t, "file-ca-2")
	dir := t.TempDir()
	p1 := filepath.Join(dir, "ca1.pem")
	p2 := filepath.Join(dir, "ca2.pem")
	require.NoError(t, os.WriteFile(p1, fileCert1, 0o644))
	require.NoError(t, os.WriteFile(p2, fileCert2, 0o644))
	t.Setenv(EnvCAFile, p1+": "+p2+":")

	inlinePEM := genCertPEM(t, "inline-ca")
	t.Setenv(EnvCAPEM, string(inlinePEM))

	pool, err := ProxyCAs()
	require.NoError(t, err)
	require.NotNil(t, pool)

	subjects := poolSubjectCNs(t, pool)
	require.Contains(t, subjects, "extra-ca-injected")
	require.Contains(t, subjects, "file-ca-1")
	require.Contains(t, subjects, "file-ca-2")
	require.Contains(t, subjects, "inline-ca")

	// Cached: a second call returns the identical pool pointer and no error.
	pool2, err2 := ProxyCAs()
	require.NoError(t, err2)
	require.Same(t, pool, pool2)
}

// TestSetExtraCAsAfterSealPanics verifies the seal guard. It must run after
// ProxyCAs has been invoked; Go runs tests in source order within a file, but
// to be robust we call ProxyCAs explicitly here too (idempotent once sealed).
func TestSetExtraCAsAfterSealPanics(t *testing.T) {
	_, _ = ProxyCAs() // ensure sealed
	require.PanicsWithValue(t,
		"ca: SetExtraCAs called after ProxyCAs was already invoked",
		func() { SetExtraCAs(genCertPEM(t, "too-late")) },
	)
}

func poolSubjectCNs(t *testing.T, pool *x509.CertPool) []string {
	t.Helper()
	var cns []string
	for _, raw := range pool.Subjects() { //nolint:staticcheck // Subjects is fine for test assertions
		var rdn pkix.RDNSequence
		if _, err := asn1.Unmarshal(raw, &rdn); err != nil {
			continue
		}
		var name pkix.Name
		name.FillFromRDNSequence(&rdn)
		if name.CommonName != "" {
			cns = append(cns, name.CommonName)
		}
	}
	return cns
}
