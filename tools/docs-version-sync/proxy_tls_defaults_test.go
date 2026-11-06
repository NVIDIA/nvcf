// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestProxyTLSHelperDefaultsMatch keeps cache signing and client CA injection on
// the same helper version while preserving their separate registry routes.
func TestProxyTLSHelperDefaultsMatch(t *testing.T) {
	root := docsVersionSyncRepoRoot(t)
	var cache struct {
		Images struct {
			Certificates string `yaml:"certificates"`
		} `yaml:"images"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "deploy/helm/container-cache/deploy/values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &cache); err != nil {
		t.Fatal(err)
	}
	expected := cache.Images.Certificates
	expectedImage, err := parseResolvedImageReference(expected)
	if err != nil {
		t.Fatal(err)
	}
	var unbound struct {
		Images struct {
			Certificates string `yaml:"certificates"`
		} `yaml:"images"`
		Webhook struct {
			CertificatesImage string `yaml:"certificatesImage"`
		} `yaml:"webhook"`
	}
	raw, err = os.ReadFile(filepath.Join(root, "deploy/helm/nvcf-unbound/values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &unbound); err != nil {
		t.Fatal(err)
	}
	for key, actual := range map[string]string{
		"images.certificates":       unbound.Images.Certificates,
		"webhook.certificatesImage": unbound.Webhook.CertificatesImage,
	} {
		image, err := parseResolvedImageReference(actual)
		if err != nil {
			t.Errorf("Unbound %s: %v", key, err)
			continue
		}
		// Mirrored copies of the same helper can use different registry prefixes.
		if image.Name != expectedImage.Name || image.Version != expectedImage.Version || image.Digest != expectedImage.Digest {
			t.Errorf("Unbound %s = %q, cache uses %q", key, actual, expected)
		}
	}
}
