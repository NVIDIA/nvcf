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

package managementtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted/controlplaneprofile"
)

func testCert(t *testing.T) (*x509.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mgmt-api"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return cert, pemStr
}

func TestSystemTrustNeverSkipsVerify(t *testing.T) {
	for _, mode := range []string{"", controlplaneprofile.TrustModeSystem} {
		cfg, err := TLSConfig(controlplaneprofile.ManagementTLS{TrustMode: mode})
		require.NoError(t, err)
		require.False(t, cfg.InsecureSkipVerify)
		require.Nil(t, cfg.RootCAs)
	}
}

func TestBundleTrustUsesProvidedCAOnly(t *testing.T) {
	_, pemStr := testCert(t)
	cfg, err := TLSConfig(controlplaneprofile.ManagementTLS{TrustMode: controlplaneprofile.TrustModeBundle, CABundlePEM: pemStr})
	require.NoError(t, err)
	require.False(t, cfg.InsecureSkipVerify)
	require.NotNil(t, cfg.RootCAs)

	_, err = TLSConfig(controlplaneprofile.ManagementTLS{TrustMode: controlplaneprofile.TrustModeBundle, CABundlePEM: "not a pem"})
	require.Error(t, err)
}

func TestPinModeRejected(t *testing.T) {
	cfg, err := TLSConfig(controlplaneprofile.ManagementTLS{
		TrustMode: "pin",
	})
	require.Error(t, err)
	require.Nil(t, cfg)
	require.Contains(t, err.Error(), "system or bundle")
}

func TestUnknownModeRejected(t *testing.T) {
	_, err := TLSConfig(controlplaneprofile.ManagementTLS{TrustMode: "weird"})
	require.Error(t, err)
}
