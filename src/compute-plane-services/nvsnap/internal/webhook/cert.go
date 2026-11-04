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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CertOptions controls self-signed cert generation. Defaults are
// production-reasonable: ECDSA P-256, 365-day validity, dnsName-based
// CN. The output cert is suitable for the K8s admission webhook
// "Service" mode (the apiserver dials the Service's cluster DNS name,
// which is what we put in DNSNames).
//
// In production, prefer cert-manager. This package exists for:
//   - bootstrap before cert-manager is installed,
//   - dev / test environments,
//   - air-gapped installs where cert-manager isn't an option.
type CertOptions struct {
	// CommonName is the certificate's CN. Default: first entry of DNSNames.
	CommonName string

	// DNSNames are the SAN dnsName entries the cert is valid for. The
	// K8s apiserver dials webhooks via Service DNS, so this should
	// include "<service>.<namespace>.svc" and
	// "<service>.<namespace>.svc.cluster.local".
	DNSNames []string

	// Validity is how long the cert is valid for. Default 365 days.
	Validity time.Duration
}

// GeneratedCert bundles a freshly-generated self-signed cert with its
// PEM-encoded counterparts (suitable for writing to disk and loading
// into TLS) and the CA bundle (which equals the cert itself for a
// self-signed setup). caBundle goes into the
// MutatingWebhookConfiguration.webhooks[].clientConfig.caBundle field.
type GeneratedCert struct {
	CertPEM  []byte
	KeyPEM   []byte
	CABundle []byte // identical to CertPEM for self-signed
}

// GenerateSelfSigned returns a fresh ECDSA self-signed cert that's valid
// for the configured DNS names.
func GenerateSelfSigned(opts CertOptions) (*GeneratedCert, error) {
	if len(opts.DNSNames) == 0 {
		return nil, errors.New("webhook: GenerateSelfSigned: at least one DNSName required")
	}
	if opts.CommonName == "" {
		opts.CommonName = opts.DNSNames[0]
	}
	if opts.Validity == 0 {
		opts.Validity = 365 * 24 * time.Hour
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: opts.CommonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(opts.Validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              opts.DNSNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("create cert: %w", err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	return &GeneratedCert{CertPEM: certPEM, KeyPEM: keyPEM, CABundle: certPEM}, nil
}

// WriteToDir writes cert.pem + key.pem to dir, creating dir if needed.
// Permissions: cert=0644, key=0600.
func (c *GeneratedCert) WriteToDir(dir string) (certPath, keyPath string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, c.CertPEM, 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyPath, c.KeyPEM, 0o600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}
