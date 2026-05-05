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
package hacks

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"time"
)

func GeneratePk() (*x509.Certificate, crypto.PrivateKey) {
	ca, caPrivateKey, err := GenerateCA()
	if err != nil {
		panic(err)
	}
	leafCert, leafPrivateKey, err := GenerateLeafCert(ca, caPrivateKey)
	if err != nil {
		panic(err)
	}
	return leafCert, leafPrivateKey
}

func GenerateCA() (*x509.Certificate, crypto.PrivateKey, error) {
	certTempl := &x509.Certificate{
		SerialNumber:          big.NewInt(2019),
		Subject:               pkix.Name{},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	pub := priv.Public()
	caBytes, err := x509.CreateCertificate(rand.Reader, certTempl, certTempl, pub, priv)
	if err != nil {
		return nil, nil, err
	}
	ca, err := x509.ParseCertificate(caBytes)
	if err != nil {
		return nil, nil, err
	}
	return ca, priv, nil
}

func GenerateLeafCert(ca *x509.Certificate, caPriv crypto.PrivateKey) (*x509.Certificate, crypto.PrivateKey, error) {
	certTempl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	pub := priv.Public()
	certBytes, err := x509.CreateCertificate(rand.Reader, certTempl, ca, pub, caPriv)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, priv, nil
}
