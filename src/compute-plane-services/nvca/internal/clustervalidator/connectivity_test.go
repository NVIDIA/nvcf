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

package clustervalidator

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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestCA generates a self-signed CA certificate PEM file at path and
// returns it. Used to give inClusterTLSConfig a valid CA bundle in tests.
func writeTestCA(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NoError(t, os.WriteFile(path, pemBytes, 0o600))
}

func TestInClusterTLSConfig_MissingCA_FailsClosed(t *testing.T) {
	orig := inClusterCAPath
	t.Cleanup(func() { inClusterCAPath = orig })
	inClusterCAPath = filepath.Join(t.TempDir(), "does-not-exist.crt")

	cfg, ok := inClusterTLSConfig()
	assert.False(t, ok)
	assert.Nil(t, cfg)
}

func TestInClusterTLSConfig_InvalidCA_FailsClosed(t *testing.T) {
	orig := inClusterCAPath
	t.Cleanup(func() { inClusterCAPath = orig })
	path := filepath.Join(t.TempDir(), "invalid.crt")
	require.NoError(t, os.WriteFile(path, []byte("not a certificate"), 0o600))
	inClusterCAPath = path

	cfg, ok := inClusterTLSConfig()
	assert.False(t, ok)
	assert.Nil(t, cfg)
}

func TestInClusterTLSConfig_ValidCA_VerifiesTLS(t *testing.T) {
	orig := inClusterCAPath
	t.Cleanup(func() { inClusterCAPath = orig })
	path := filepath.Join(t.TempDir(), "ca.crt")
	writeTestCA(t, path)
	inClusterCAPath = path

	cfg, ok := inClusterTLSConfig()
	require.True(t, ok)
	require.NotNil(t, cfg)
	assert.False(t, cfg.InsecureSkipVerify)
	assert.NotNil(t, cfg.RootCAs)
}

func TestProbeKubernetesAPIServiceIP_MissingCA_ReturnsFalse(t *testing.T) {
	orig := inClusterCAPath
	t.Cleanup(func() { inClusterCAPath = orig })
	inClusterCAPath = filepath.Join(t.TempDir(), "does-not-exist.crt")

	assert.False(t, probeKubernetesAPIServiceIP(context.Background()))
}
