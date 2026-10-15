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

package trustbundle

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type InstallOptions struct {
	SystemBundlePath    string
	TrustBundlePath     string
	OutputBundlePath    string
	ExpectedFingerprint string
}

// MergeFiles validates the mounted NVCF trust bundle, checks it against the
// control-plane supplied fingerprint, then writes a merged CA bundle atomically.
func MergeFiles(opts InstallOptions) error {
	if strings.TrimSpace(opts.TrustBundlePath) == "" {
		return errors.New("trust bundle path is required")
	}
	if strings.TrimSpace(opts.OutputBundlePath) == "" {
		return errors.New("output bundle path is required")
	}
	expectedFingerprint := strings.ToLower(strings.TrimSpace(opts.ExpectedFingerprint))
	if expectedFingerprint == "" {
		return errors.New("expected fingerprint is required")
	}

	trustBundle, err := os.ReadFile(opts.TrustBundlePath)
	if err != nil {
		return fmt.Errorf("read trust bundle: %w", err)
	}
	if err := ValidatePEM(string(trustBundle)); err != nil {
		return fmt.Errorf("validate trust bundle: %w", err)
	}
	got, err := FingerprintPEM(string(trustBundle))
	if err != nil {
		return fmt.Errorf("fingerprint trust bundle: %w", err)
	}
	if got != expectedFingerprint {
		return fmt.Errorf("trust bundle fingerprint mismatch: got %s want %s", got, expectedFingerprint)
	}

	merged := make([]byte, 0, len(trustBundle)+4096)
	if strings.TrimSpace(opts.SystemBundlePath) != "" {
		// Minimal/distroless images can omit the system bundle path. In that
		// case the pinned NVCF bundle remains usable by itself.
		systemBundle, err := os.ReadFile(opts.SystemBundlePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read system bundle: %w", err)
		}
		if err == nil {
			merged = append(merged, systemBundle...)
			if len(merged) > 0 && merged[len(merged)-1] != '\n' {
				merged = append(merged, '\n')
			}
			merged = append(merged, '\n')
		}
	}
	merged = append(merged, trustBundle...)
	if len(merged) == 0 || merged[len(merged)-1] != '\n' {
		merged = append(merged, '\n')
	}

	return writeFileAtomically(opts.OutputBundlePath, merged, 0644)
}

// ValidatePEM accepts only parseable CERTIFICATE blocks with no trailing data.
func ValidatePEM(trustBundlePEM string) error {
	_, err := certificateHashes(trustBundlePEM)
	return err
}

// FingerprintPEM computes the reviewed nvcf-trust-bundle-v1 digest. It hashes
// each certificate DER, sorts and deduplicates those hashes, then hashes the
// canonical list so root CA rotation bundles produce stable fingerprints.
func FingerprintPEM(trustBundlePEM string) (string, error) {
	hashes, err := certificateHashes(trustBundlePEM)
	if err != nil {
		return "", err
	}
	sort.Strings(hashes)

	canonical := "nvcf-trust-bundle-v1\n" + strings.Join(hashes, "\n") + "\n"
	digest := sha256.Sum256([]byte(canonical))
	return "sha256:" + fmt.Sprintf("%x", digest[:]), nil
}

func certificateHashes(trustBundlePEM string) ([]string, error) {
	certHashes := map[string]struct{}{}
	remaining := []byte(trustBundlePEM)
	for {
		remaining = bytes.TrimSpace(remaining)
		if len(remaining) == 0 {
			break
		}
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN ")) {
			return nil, errors.New("unexpected non-whitespace data in trust bundle")
		}
		block, rest := pem.Decode(remaining)
		if block == nil {
			return nil, errors.New("unexpected non-whitespace data in trust bundle")
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("PEM block type %q is not supported", block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(cert.Raw)
		certHashes[fmt.Sprintf("%x", hash[:])] = struct{}{}
		remaining = rest
	}
	if len(certHashes) == 0 {
		return nil, errors.New("no CERTIFICATE PEM blocks found")
	}

	hashes := make([]string, 0, len(certHashes))
	for hash := range certHashes {
		hashes = append(hashes, hash)
	}
	return hashes, nil
}

func writeFileAtomically(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".ca-certificates-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary output: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temporary output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary output: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temporary output: %w", err)
	}
	return nil
}
