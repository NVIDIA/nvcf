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

package configutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGetTLSConfigFromBase64LoadsRootCA(t *testing.T) {
	clientCertPEM, clientKeyPEM, caCertPEM := generateCertificateChain(t)

	tlsConfig, err := GetTLSConfigFromBase64(
		base64.StdEncoding.EncodeToString(clientCertPEM),
		base64.StdEncoding.EncodeToString(clientKeyPEM),
		base64.StdEncoding.EncodeToString(caCertPEM),
		false,
	)

	require.NoError(t, err)
	require.False(t, tlsConfig.InsecureSkipVerify)
	require.Len(t, tlsConfig.Certificates, 1)
	require.NotNil(t, tlsConfig.RootCAs)
	require.Len(t, tlsConfig.RootCAs.Subjects(), 1)
}

func TestGetTLSConfigFromBase64AllowsSystemRoots(t *testing.T) {
	clientCertPEM, clientKeyPEM, _ := generateCertificateChain(t)

	tlsConfig, err := GetTLSConfigFromBase64(
		base64.StdEncoding.EncodeToString(clientCertPEM),
		base64.StdEncoding.EncodeToString(clientKeyPEM),
		"",
		false,
	)

	require.NoError(t, err)
	require.Nil(t, tlsConfig.RootCAs)
}

func TestGetTLSConfigFromBase64RejectsInvalidRootCA(t *testing.T) {
	clientCertPEM, clientKeyPEM, _ := generateCertificateChain(t)

	_, err := GetTLSConfigFromBase64(
		base64.StdEncoding.EncodeToString(clientCertPEM),
		base64.StdEncoding.EncodeToString(clientKeyPEM),
		base64.StdEncoding.EncodeToString([]byte("not a certificate")),
		false,
	)

	require.ErrorContains(t, err, "failed to parse CA certificate")
}

func generateCertificateChain(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caTemplate, &clientKey.PublicKey, caKey)
	require.NoError(t, err)

	clientKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCertDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: clientKeyDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
}
