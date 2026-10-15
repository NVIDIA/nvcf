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

func TestInClusterTLSConfig(t *testing.T) {
	tests := []struct {
		name    string
		setupCA func(t *testing.T, path string)
		wantOK  bool
	}{
		{
			name:    "missing CA fails closed",
			setupCA: func(t *testing.T, path string) {},
			wantOK:  false,
		},
		{
			name: "invalid CA fails closed",
			setupCA: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, []byte("not a certificate"), 0o600))
			},
			wantOK: false,
		},
		{
			name: "valid CA verifies TLS",
			setupCA: func(t *testing.T, path string) {
				writeTestCA(t, path)
			},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := inClusterCAPath
			t.Cleanup(func() { inClusterCAPath = orig })
			path := filepath.Join(t.TempDir(), "ca.crt")
			tt.setupCA(t, path)
			inClusterCAPath = path

			cfg, ok := inClusterTLSConfig()
			assert.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				assert.Nil(t, cfg)
				return
			}
			require.NotNil(t, cfg)
			assert.False(t, cfg.InsecureSkipVerify)
			assert.NotNil(t, cfg.RootCAs)
		})
	}
}

func TestProbeKubernetesAPIServiceIP_MissingCA_ReturnsFalse(t *testing.T) {
	orig := inClusterCAPath
	t.Cleanup(func() { inClusterCAPath = orig })
	inClusterCAPath = filepath.Join(t.TempDir(), "does-not-exist.crt")

	assert.False(t, probeKubernetesAPIServiceIP(context.Background()))
}
