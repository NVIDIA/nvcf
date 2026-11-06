// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"sort"
	"strings"
	"testing"
)

const (
	testPylonTemplatePath = "deploy/stacks/self-managed/global.yaml.gotmpl"
	testPylonValuesPath   = "deploy/stacks/self-managed/environments/base.yaml"

	testLiteralPylonTemplate = `{{- else if $llmEnabled }}
{{- $effectivePylonImage = printf "%s/%s/pylon:0.25.0" $sidecarsHost $sidecarsRepo }}
{{- end }}
`
	testRouterDerivedPylonTemplate = `{{- $routerImageTag := "" }}
{{- if $llmEnabled }}
{{- $routerImageTag = dig "addons" "llm" "requestRouter" "image" "tag" "" .Values | required "addons.llm.requestRouter.image.tag must be set" }}
{{- end }}
{{- if $configuredPylonImagePresent }}
{{- $effectivePylonImage = $configuredPylonImage }}
{{- else if $llmEnabled }}
{{- $effectivePylonImage = printf "%s/%s/pylon:%s" $sidecarsHost $sidecarsRepo $routerImageTag }}
{{- end }}
`
	testDefaultedDerivedPylonTemplate = `{{- $pylonImageTag := dig "addons" "llm" "requestRouter" "image" "tag" "" .Values | default "0.18.0" }}
{{- $effectivePylonImage = printf "%s/%s/pylon:%s" $sidecarsHost $sidecarsRepo $pylonImageTag }}
`
	testRouterValues = `addons:
  llm:
    enabled: false
    requestRouter:
      # Keep router and Pylon on the same release.
      image:
        tag: "0.29.2"
      backendRouter:
        image:
          tag: "9.9.9"
`
)

func TestEffectiveStackPinResolvesLiteralPylonTag(t *testing.T) {
	pin := effectiveStackPinForArtifact(t, "pylon")
	sources := map[string][]byte{testPylonTemplatePath: []byte(testLiteralPylonTemplate)}

	versions, err := extractEffectiveStackPins(sources, []effectiveStackPin{pin})
	if err != nil {
		t.Fatal(err)
	}
	if versions["pylon"] != "0.25.0" {
		t.Fatalf("pylon version = %q, want literal 0.25.0", versions["pylon"])
	}
	paths, err := effectiveStackPinSourcesUsed(sources, []effectiveStackPin{pin})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != testPylonTemplatePath {
		t.Fatalf("pin sources = %v, want only the template", paths)
	}
	if required := effectiveStackPinRequiredSourcePaths(sources, []effectiveStackPin{pin}); strings.Join(required, ",") != testPylonTemplatePath {
		t.Fatalf("required pin sources = %v, want only the template", required)
	}

	// Released catalogs recorded literal pins before derived tags existed.
	literalOnly := pin
	literalOnly.derivedTag = nil
	want, err := pinSourceDigest(sources, []effectiveStackPin{literalOnly})
	if err != nil {
		t.Fatal(err)
	}
	got, err := pinSourceDigest(sources, []effectiveStackPin{pin})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("literal pylon digest = %s, want released literal digest %s", got, want)
	}

	sources[testPylonValuesPath] = []byte(testRouterValues)
	_, err = extractEffectiveStackPins(sources, []effectiveStackPin{pin})
	if err == nil || !strings.Contains(err.Error(), "declared pin source "+testPylonValuesPath+" has no matcher") {
		t.Fatalf("extractEffectiveStackPins error = %v, want unused values source rejection", err)
	}
}

func TestEffectiveStackPinResolvesRouterDerivedPylonTag(t *testing.T) {
	tests := []struct {
		name     string
		template string
		values   string
		want     string
	}{
		{name: "router image tag", template: testRouterDerivedPylonTemplate, values: testRouterValues, want: "0.29.2"},
		{
			name:     "defaulted pylon image tag",
			template: testDefaultedDerivedPylonTemplate,
			values:   "addons:\n  llm:\n    requestRouter:\n      image:\n        tag: 0.18.3\n",
			want:     "0.18.3",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pin := effectiveStackPinForArtifact(t, "pylon")
			sources := map[string][]byte{
				testPylonTemplatePath: []byte(test.template),
				testPylonValuesPath:   []byte(test.values),
			}

			versions, err := extractEffectiveStackPins(sources, []effectiveStackPin{pin})
			if err != nil {
				t.Fatal(err)
			}
			if versions["pylon"] != test.want {
				t.Fatalf("pylon version = %q, want %s", versions["pylon"], test.want)
			}
			wantPaths := []string{testPylonValuesPath, testPylonTemplatePath}
			paths, err := effectiveStackPinSourcesUsed(sources, []effectiveStackPin{pin})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(paths, ",") != strings.Join(wantPaths, ",") {
				t.Fatalf("pin sources = %v, want %v", paths, wantPaths)
			}
			templateOnly := map[string][]byte{testPylonTemplatePath: []byte(test.template)}
			if required := effectiveStackPinRequiredSourcePaths(templateOnly, []effectiveStackPin{pin}); strings.Join(required, ",") != strings.Join(wantPaths, ",") {
				t.Fatalf("required pin sources = %v, want %v", required, wantPaths)
			}

			_, err = extractEffectiveStackPins(templateOnly, []effectiveStackPin{pin})
			if err == nil || !strings.Contains(err.Error(), "pin source "+testPylonValuesPath+" for pylon is not declared") {
				t.Fatalf("extractEffectiveStackPins error = %v, want undeclared values source rejection", err)
			}
		})
	}
}

func TestEffectiveStackPinRejectsUnresolvedPylonTag(t *testing.T) {
	tests := []struct {
		name     string
		template string
		values   string
		want     string
	}{
		{
			name:     "neither shape",
			template: "{{- $effectivePylonImage = $configuredPylonImage }}\n",
			want:     "resolved 0 literal and 0 derived definitions for pylon from " + testPylonTemplatePath + "; want exactly one literal tag or one tag derived from addons.llm.requestRouter.image.tag in " + testPylonValuesPath,
		},
		{
			name:     "both shapes",
			template: testLiteralPylonTemplate + testRouterDerivedPylonTemplate,
			values:   testRouterValues,
			want:     "resolved 1 literal and 1 derived definitions for pylon",
		},
		{
			name:     "variable bound to another key",
			template: strings.Replace(testRouterDerivedPylonTemplate, `"requestRouter"`, `"pki"`, 1),
			values:   testRouterValues,
			want:     "resolved 0 bindings of $routerImageTag to addons.llm.requestRouter.image.tag for pylon",
		},
		{
			name:     "values key missing",
			template: testRouterDerivedPylonTemplate,
			values:   "addons:\n  llm:\n    requestRouter:\n      chartPath: \"\"\n",
			want:     "addons.llm.requestRouter.image.tag is not set",
		},
		{
			name:     "duplicate values key",
			template: testRouterDerivedPylonTemplate,
			values:   testRouterValues + "  llm:\n    requestRouter:\n      image:\n        tag: \"0.30.0\"\n",
			want:     "resolved 2 definitions of addons.llm; want exactly one",
		},
		{
			name:     "empty values tag",
			template: testRouterDerivedPylonTemplate,
			values:   strings.Replace(testRouterValues, `"0.29.2"`, `""`, 1),
			want:     "addons.llm.requestRouter.image.tag must be a non-empty tag",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pin := effectiveStackPinForArtifact(t, "pylon")
			sources := map[string][]byte{testPylonTemplatePath: []byte(test.template)}
			if test.values != "" {
				sources[testPylonValuesPath] = []byte(test.values)
			}

			_, err := extractEffectiveStackPins(sources, []effectiveStackPin{pin})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("extractEffectiveStackPins error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestStackSourceSnapshotRecordsDerivedPylonValuesSource(t *testing.T) {
	files := testCatalogPinSources()
	files[testPylonTemplatePath] = testRouterDerivedPylonTemplate
	files[testPylonValuesPath] = strings.Replace(testRouterValues, `"0.29.2"`, `"3.4.5"`, 1)
	repo := initTestGitRepo(t)
	release := commitTestStackSource(t, repo, "1.2.3", files)

	snapshot, err := loadEffectiveStackPinSnapshot(repo, release, effectiveStackPins)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		testPylonValuesPath,
		testPylonTemplatePath,
		"deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl",
	}
	gotPaths := make([]string, 0, len(snapshot.Files))
	for sourcePath := range snapshot.Files {
		gotPaths = append(gotPaths, sourcePath)
	}
	sort.Strings(gotPaths)
	if strings.Join(gotPaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("snapshot files = %v, want %v", gotPaths, wantPaths)
	}

	catalog, err := buildCatalogFromResolvedStackInventory(testCatalogResolvedInventory(t, release), snapshot, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(catalog.Stack.PinSources, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("catalog pin sources = %v, want %v", catalog.Stack.PinSources, wantPaths)
	}
	if err := validateStackSourceSnapshotWithPins(repo, catalog, effectiveStackPins); err != nil {
		t.Fatalf("recorded derived pin snapshot did not validate: %v", err)
	}

	literal := testCatalogPinSources()
	literal[testPylonValuesPath] = testRouterValues
	literalRelease := commitTestStackSource(t, repo, "1.2.4", literal)
	snapshot, err = loadEffectiveStackPinSnapshot(repo, literalRelease, effectiveStackPins)
	if err != nil {
		t.Fatal(err)
	}
	if _, loaded := snapshot.Files[testPylonValuesPath]; loaded || len(snapshot.Files) != 2 {
		t.Fatalf("literal pylon snapshot loaded %d files including the unused values source", len(snapshot.Files))
	}
}
