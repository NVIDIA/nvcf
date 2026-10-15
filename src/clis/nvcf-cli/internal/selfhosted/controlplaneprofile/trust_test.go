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

package controlplaneprofile

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"nvcf-cli/internal/trustbundle"
)

func trustTestCertPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-root-ca"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func trustTestKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// profileWithTrust round-trips the complete valid profile and overlays the
// given trust blocks, so trust validation is exercised against an otherwise
// valid document.
func profileWithTrust(t *testing.T, mgmt ManagementTLS, transport TransportTLS) []byte {
	t.Helper()
	var doc ControlPlaneProfile
	require.NoError(t, yaml.Unmarshal([]byte(validControlPlaneProfileYAML()), &doc))
	doc.ManagementTLS = mgmt
	doc.TransportTLS = transport
	body, err := yaml.Marshal(doc)
	require.NoError(t, err)
	return body
}

func TestTransportTLSSystemAndAbsentAreValid(t *testing.T) {
	_, err := ParseAndValidate(profileWithTrust(t, ManagementTLS{}, TransportTLS{TrustMode: TrustModeSystem}), ValidateOptions{Require: RequireBoth})
	require.NoError(t, err)
	_, err = ParseAndValidate(profileWithTrust(t, ManagementTLS{}, TransportTLS{}), ValidateOptions{Require: RequireBoth})
	require.NoError(t, err)
}

func TestTransportTLSBundleValid(t *testing.T) {
	cert := trustTestCertPEM(t)
	fp, err := trustbundle.Fingerprint([]byte(cert))
	require.NoError(t, err)
	_, err = ParseAndValidate(profileWithTrust(t, ManagementTLS{}, TransportTLS{
		TrustMode: TrustModeBundle, TrustBundlePEM: cert, TrustBundleFingerprint: fp,
	}), ValidateOptions{Require: RequireBoth})
	require.NoError(t, err)
}

func TestTransportTLSBundleAllowsWhitespaceAroundMultipleCertificates(t *testing.T) {
	certA := trustTestCertPEM(t)
	certB := trustTestCertPEM(t)
	bundle := "\n" + certA + "\n\n" + certB + "\n"
	fp, err := trustbundle.Fingerprint([]byte(bundle))
	require.NoError(t, err)

	_, err = ParseAndValidate(profileWithTrust(t, ManagementTLS{}, TransportTLS{
		TrustMode: TrustModeBundle, TrustBundlePEM: bundle, TrustBundleFingerprint: fp,
	}), ValidateOptions{Require: RequireBoth})
	require.NoError(t, err)
}

func TestTransportTLSSystemRejectsTrustMaterial(t *testing.T) {
	cert := trustTestCertPEM(t)
	fp, err := trustbundle.Fingerprint([]byte(cert))
	require.NoError(t, err)

	tests := []struct {
		name      string
		transport TransportTLS
		field     string
	}{
		{
			name:      "pem",
			transport: TransportTLS{TrustMode: TrustModeSystem, TrustBundlePEM: cert},
			field:     fieldTransportTrustBundlePEM,
		},
		{
			name:      "fingerprint",
			transport: TransportTLS{TrustMode: TrustModeSystem, TrustBundleFingerprint: fp},
			field:     "transportTls.trustBundleFingerprint",
		},
		{
			name:      "pem and fingerprint",
			transport: TransportTLS{TrustMode: TrustModeSystem, TrustBundlePEM: cert, TrustBundleFingerprint: fp},
			field:     "transportTls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseAndValidate(profileWithTrust(t, ManagementTLS{}, tt.transport), ValidateOptions{Require: RequireBoth})
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.field)
		})
	}
}

func TestTransportTLSBundleRejectsNonWhitespaceAroundCertificates(t *testing.T) {
	certA := trustTestCertPEM(t)
	certB := trustTestCertPEM(t)

	tests := []struct {
		name   string
		bundle string
	}{
		{name: "before", bundle: "plaintext before\n" + certA},
		{name: "between", bundle: certA + "\nplaintext between\n" + certB},
		{name: "after", bundle: certA + "\nplaintext after\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp, err := trustbundle.Fingerprint([]byte(tt.bundle))
			require.NoError(t, err)
			_, err = ParseAndValidate(profileWithTrust(t, ManagementTLS{}, TransportTLS{
				TrustMode: TrustModeBundle, TrustBundlePEM: tt.bundle, TrustBundleFingerprint: fp,
			}), ValidateOptions{Require: RequireBoth})
			require.Error(t, err)
			require.Contains(t, err.Error(), fieldTransportTrustBundlePEM)
		})
	}
}

func TestTransportTLSRejectsPrivateKey(t *testing.T) {
	cert := trustTestCertPEM(t)
	fp, _ := trustbundle.Fingerprint([]byte(cert))
	_, err := ParseAndValidate(profileWithTrust(t, ManagementTLS{}, TransportTLS{
		TrustMode: TrustModeBundle, TrustBundlePEM: cert + trustTestKeyPEM(t), TrustBundleFingerprint: fp,
	}), ValidateOptions{Require: RequireBoth})
	require.Error(t, err)
	require.Contains(t, err.Error(), "transportTls.trustBundlePem")
}

func TestTransportTLSRejectsFingerprintMismatch(t *testing.T) {
	cert := trustTestCertPEM(t)
	_, err := ParseAndValidate(profileWithTrust(t, ManagementTLS{}, TransportTLS{
		TrustMode: TrustModeBundle, TrustBundlePEM: cert, TrustBundleFingerprint: "sha256:" + strings.Repeat("0", 64),
	}), ValidateOptions{Require: RequireBoth})
	require.Error(t, err)
	require.Contains(t, err.Error(), "transportTls.trustBundleFingerprint")
}

func TestTransportTLSRejectsTrustMaterialWithoutMode(t *testing.T) {
	cert := trustTestCertPEM(t)
	fp, _ := trustbundle.Fingerprint([]byte(cert))
	_, err := ParseAndValidate(profileWithTrust(t, ManagementTLS{}, TransportTLS{
		TrustBundlePEM: cert, TrustBundleFingerprint: fp, // trustMode omitted
	}), ValidateOptions{Require: RequireBoth})
	require.Error(t, err)
	require.Contains(t, err.Error(), "transportTls.trustMode")
}

func TestManagementTLSRejectsPrivateKey(t *testing.T) {
	_, err := ParseAndValidate(profileWithTrust(t,
		ManagementTLS{TrustMode: TrustModeBundle, CABundlePEM: trustTestCertPEM(t) + trustTestKeyPEM(t)},
		TransportTLS{TrustMode: TrustModeSystem}), ValidateOptions{Require: RequireBoth})
	require.Error(t, err)
	require.Contains(t, err.Error(), "managementTls.caBundlePem")
}

func TestManagementTLSSystemRejectsCABundlePEM(t *testing.T) {
	for _, tt := range []struct {
		name string
		pem  string
	}{
		{name: "certificate", pem: trustTestCertPEM(t)},
		{name: "private key", pem: trustTestKeyPEM(t)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseAndValidate(profileWithTrust(t,
				ManagementTLS{TrustMode: TrustModeSystem, CABundlePEM: tt.pem},
				TransportTLS{TrustMode: TrustModeSystem}), ValidateOptions{Require: RequireBoth})
			require.Error(t, err)
			require.Contains(t, err.Error(), "managementTls.caBundlePem")
		})
	}
}

func TestManagementTLSRejectsPinMode(t *testing.T) {
	_, err := ParseAndValidate(profileWithTrust(t,
		ManagementTLS{TrustMode: "pin"},
		TransportTLS{TrustMode: TrustModeSystem}), ValidateOptions{Require: RequireBoth})
	require.Error(t, err)
	require.Contains(t, err.Error(), "managementTls.trustMode")
}

func TestManagementTLSRejectsAcceptedFingerprintField(t *testing.T) {
	doc := validControlPlaneProfileYAML() + `
managementTls:
  trustMode: system
  acceptedFingerprint: sha256:1111111111111111111111111111111111111111111111111111111111111111
`

	_, err := ParseAndValidate([]byte(doc), ValidateOptions{Require: RequireBoth})
	require.Error(t, err)
	require.Contains(t, err.Error(), "acceptedFingerprint")
}
