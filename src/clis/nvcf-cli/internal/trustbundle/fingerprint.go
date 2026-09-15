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

// Package trustbundle computes the canonical nvcf-trust-bundle-v1 digest of a
// PEM CA trust bundle, as defined by the PKI for Multi-Cluster Self-Hosted NVCF
// Plan of Record (POR section 9.3). The same algorithm is implemented identically in
// nvcf-cli and NVCA so the fingerprint advertised in the control-plane
// attachment can be validated unchanged on the worker side (POR R-7).
package trustbundle

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CanonicalVersion is the version tag that prefixes the canonical digest input.
const CanonicalVersion = "nvcf-trust-bundle-v1"

// ErrNoCertificates is returned when the PEM input contains no CERTIFICATE
// blocks. The worker trust bundle must contain at least one CA anchor.
var ErrNoCertificates = errors.New("trustbundle: no CERTIFICATE blocks in PEM input")

// Fingerprint computes the nvcf-trust-bundle-v1 canonical digest of a PEM
// bundle and returns it as "sha256:<lowercase-hex>".
//
// The algorithm (POR section 9.3):
//  1. Parse the input as PEM.
//  2. Accept only blocks of type CERTIFICATE.
//  3. DER-parse each certificate with the platform X.509 parser.
//  4. Compute sha256(cert.Raw) for each certificate (lowercase hex).
//  5. Deduplicate identical certificates by DER hash.
//  6. Sort the certificate hashes lexicographically by lowercase hex string.
//  7. Build canonical UTF-8 text with LF line endings and a trailing LF:
//     "nvcf-trust-bundle-v1\n<hash1>\n<hash2>\n".
//  8. Compute sha256 over that canonical text.
//  9. Emit "sha256:<lowercase-hex-digest>".
//
// The result is independent of PEM whitespace, line wrapping, comments,
// duplicate certificates, and certificate order.
func Fingerprint(pemBytes []byte) (string, error) {
	hashes, err := certHashes(pemBytes)
	if err != nil {
		return "", err
	}
	if len(hashes) == 0 {
		return "", ErrNoCertificates
	}
	sort.Strings(hashes)

	var b strings.Builder
	b.WriteString(CanonicalVersion)
	b.WriteByte('\n')
	for _, h := range hashes {
		b.WriteString(h)
		b.WriteByte('\n')
	}

	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// certHashes returns the deduplicated lowercase-hex sha256 digests of every
// CERTIFICATE block's DER bytes, preserving first-seen order (the caller sorts).
func certHashes(pemBytes []byte) ([]string, error) {
	seen := make(map[string]struct{})
	var hashes []string

	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("trustbundle: parse certificate: %w", err)
		}
		sum := sha256.Sum256(cert.Raw)
		h := hex.EncodeToString(sum[:])
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		hashes = append(hashes, h)
	}
	return hashes, nil
}
