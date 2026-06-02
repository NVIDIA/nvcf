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

package dsl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestUpdateYAMLKeysCreatesMissingMapsAndLists(t *testing.T) {
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")

	dir := t.TempDir()
	path := filepath.Join(dir, "env.yaml")
	writeFile(t, path, "global:\n  storageClass: local-path\n")

	keys := [][2]string{
		{"global.imagePullSecrets[0].name", "nvcr-pull-secret"},
		{"global.image.repository", "${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}"},
	}
	if err := UpdateYAMLKeys(path, keys); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := os.ReadFile(path)
	out := string(got)
	for _, want := range []string{
		"storageClass: local-path",
		"imagePullSecrets:",
		"name: nvcr-pull-secret",
		"repository: test-org/test-team",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestUpdateYAMLKeysRejectsScalarInPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.yaml")
	writeFile(t, path, "global: scalar\n")

	err := UpdateYAMLKeys(path, [][2]string{{"global.image.registry", "nvcr.io"}})
	if err == nil {
		t.Fatal("expected error when scalar blocks descent")
	}
}

func TestReadYAMLKeyFoundAndMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.yaml")
	writeFile(t, path, `global:
  image:
    registry: nvcr.io
list:
  - first
  - second
`)

	cases := []struct {
		name    string
		key     string
		want    string
		found   bool
	}{
		{"map leaf", "global.image.registry", "nvcr.io", true},
		{"list index", "list[1]", "second", true},
		{"missing top key", "absent", "", false},
		{"missing nested", "global.image.repository", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := ReadYAMLKey(path, tc.key)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if ok != tc.found {
				t.Fatalf("found = %v, want %v", ok, tc.found)
			}
			if got != tc.want {
				t.Fatalf("value = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMatchYAMLSubtreeExactAndSubset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.yaml")
	writeFile(t, path, `apiVersion: nvcf.nvidia.com/v1alpha1
kind: ControlPlaneProfile
controlPlane:
  clusterName: ncp-local
  region: us-west-1
  endpoints:
    inCluster:
      icmsURL: http://api.sis.svc.cluster.local:8080
      revalURL: http://reval.nvcf.svc.cluster.local:8080
`)

	subsetExpected := `controlPlane:
  clusterName: ncp-local
  endpoints:
    inCluster:
      icmsURL: http://api.sis.svc.cluster.local:8080
`
	if err := MatchYAMLSubtree(path, "", subsetExpected, MatchSubset); err != nil {
		t.Fatalf("subset whole-file: %v", err)
	}

	exactSubtree := `icmsURL: http://api.sis.svc.cluster.local:8080
revalURL: http://reval.nvcf.svc.cluster.local:8080
`
	if err := MatchYAMLSubtree(path, "controlPlane.endpoints.inCluster", exactSubtree, MatchExact); err != nil {
		t.Fatalf("exact subtree: %v", err)
	}

	missingKeyExpected := `controlPlane:
  clusterName: ncp-local
  missingKey: nope
`
	if err := MatchYAMLSubtree(path, "", missingKeyExpected, MatchSubset); err == nil {
		t.Fatal("expected error for missing key in actual")
	}

	exactWithExtraExpected := `icmsURL: http://api.sis.svc.cluster.local:8080
`
	if err := MatchYAMLSubtree(path, "controlPlane.endpoints.inCluster", exactWithExtraExpected, MatchExact); err == nil {
		t.Fatal("expected error for extra key in actual under exact mode")
	}
}

func TestMatchYAMLSubtreeInterpolatesExpected(t *testing.T) {
	t.Setenv("SAMPLE_NGC_ORG", "test-org")

	dir := t.TempDir()
	path := filepath.Join(dir, "env.yaml")
	writeFile(t, path, "global:\n  image:\n    repository: test-org/foo\n")

	expected := `global:
  image:
    repository: ${SAMPLE_NGC_ORG}/foo
`
	if err := MatchYAMLSubtree(path, "", expected, MatchSubset); err != nil {
		t.Fatalf("interpolated match: %v", err)
	}
}

func TestSubstituteFileReplacesPlaceholder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.yaml")
	writeFile(t, path, "token: PLACEHOLDER\nother: PLACEHOLDER\n")

	if err := SubstituteFile(path, "PLACEHOLDER", "real-value"); err != nil {
		t.Fatalf("substitute: %v", err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "PLACEHOLDER") {
		t.Fatalf("placeholder still present:\n%s", got)
	}
	if !strings.Contains(string(got), "real-value") {
		t.Fatalf("replacement missing:\n%s", got)
	}
}

func TestParsePathInvalidShapes(t *testing.T) {
	bads := []string{
		"a..b",
		"a[1",
		"a[]",
		"a[abc]",
		"a]b",
	}
	for _, p := range bads {
		t.Run(p, func(t *testing.T) {
			if _, err := parsePath(p); err == nil {
				t.Fatalf("expected error for %q", p)
			}
		})
	}
}
