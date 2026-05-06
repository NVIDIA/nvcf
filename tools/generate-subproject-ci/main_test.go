// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"strings"
	"testing"
)

func TestRenderPipelineGeneratesSubprojectJobs(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		GoWork: &goWorkConfig{
			Go:  "1.26",
			Use: []string{"tools/generate-subproject-ci"},
		},
		SharedChangePaths: []string{
			".gitlab-ci.yml",
			"tools/ci/**/*",
		},
		Profiles: map[string]profile{
			"go-library": {
				Stage: "validate",
				Image: "golang:1.26-bookworm",
				Variables: map[string]string{
					"GOTOOLCHAIN": "local",
					"GOWORK":      "$CI_PROJECT_DIR/go.work",
				},
				Checks: []check{
					{ID: "vendor", Type: "go-vendor"},
					{
						ID:      "codegen",
						Type:    "go-codegen",
						Command: "./scripts/codegen_update",
						Install: []string{"k8s.io/code-generator/cmd/deepcopy-gen@v0.34.2"},
					},
					{ID: "license", Type: "shell", Command: "./scripts/ci_check_license"},
					{
						ID:         "unit-tests",
						Type:       "go-unit-tests",
						ResultsDir: "public/{{ .ID }}",
						Coverage:   `/total:[ \ta-z()]*\d+\.\d+/`,
						Artifacts: []string{
							"public/{{ .ID }}/report.json",
							"public/{{ .ID }}/cover.txt",
						},
					},
				},
			},
		},
		Subprojects: []subproject{
			{
				ID:      "nvcf-go",
				Path:    "src/libraries/go/lib",
				Profile: "go-library",
				GoWork:  true,
			},
		},
	}

	rendered, err := renderPipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderPipeline failed: %v", err)
	}

	for _, needle := range []string{
		"default:",
		"stages:",
		"nvcf-go-vendor:",
		"nvcf-go-codegen:",
		"nvcf-go-license:",
		"nvcf-go-unit-tests:",
		"./tools/scripts/update-go-work",
		"./tools/ci/check-go-vendor 'src/libraries/go/lib'",
		"./tools/ci/check-go-codegen 'src/libraries/go/lib' --command './scripts/codegen_update' --install 'k8s.io/code-generator/cmd/deepcopy-gen@v0.34.2'",
		`cd "$CI_PROJECT_DIR/src/libraries/go/lib" && ./scripts/ci_check_license`,
		"./tools/ci/run-go-unit-tests 'src/libraries/go/lib' --results-dir 'public/nvcf-go'",
		`GOWORK: $CI_PROJECT_DIR/go.work`,
		"PARENT_PIPELINE_SOURCE",
		"src/libraries/go/lib/**/*",
		"public/nvcf-go/report.json",
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered pipeline missing %q\n%s", needle, rendered)
		}
	}
}

func TestRenderPipelineAlwaysEmitsSentinel(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Profiles: map[string]profile{
			"go-library": {
				Stage: "validate",
				Image: "golang:1.26-bookworm",
				Checks: []check{
					{ID: "vendor", Type: "go-vendor"},
				},
			},
		},
		Subprojects: []subproject{
			{ID: "nvcf-go", Path: "src/libraries/go/lib", Profile: "go-library"},
		},
	}

	rendered, err := renderPipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderPipeline failed: %v", err)
	}

	if !strings.Contains(rendered, "subproject-validations-sentinel:") {
		t.Fatalf("rendered pipeline missing sentinel job\n%s", rendered)
	}

	sentinelIdx := strings.Index(rendered, "subproject-validations-sentinel:")
	sentinelBlock := rendered[sentinelIdx:]
	if !strings.Contains(sentinelBlock, "- when: always") {
		t.Fatalf("sentinel job must use `when: always` rules\n%s", sentinelBlock)
	}
	if strings.Contains(sentinelBlock, "PARENT_PIPELINE_SOURCE") {
		t.Fatalf("sentinel job must not use path-gated rules\n%s", sentinelBlock)
	}

	if !strings.Contains(rendered, "nvcf-go-vendor:") {
		t.Fatalf("rendered pipeline missing real subproject job\n%s", rendered)
	}
}

func TestRenderPipelineGeneratesWorkspaceShellJobs(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		GoWork: &goWorkConfig{
			Go:  "1.26",
			Use: []string{"tools/generate-subproject-ci"},
		},
		SharedChangePaths: []string{
			".gitlab-ci.yml",
			"tools/ci/**/*",
		},
		Profiles: map[string]profile{
			"go-integration": {
				Stage: "validate",
				Image: "golang:1.26-bookworm",
				Checks: []check{
					{ID: "integration", Type: "go-workspace-shell", Command: "go test ./..."},
				},
			},
		},
		Subprojects: []subproject{
			{
				ID:      "nvcf-go",
				Path:    "src/libraries/go/lib",
				Profile: "go-integration",
				GoWork:  true,
			},
		},
	}

	rendered, err := renderPipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderPipeline failed: %v", err)
	}

	for _, needle := range []string{
		"nvcf-go-integration:",
		"./tools/scripts/update-go-work",
		`cd "$CI_PROJECT_DIR/src/libraries/go/lib" && GOWORK="$CI_PROJECT_DIR/go.work" go test ./...`,
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered pipeline missing %q\n%s", needle, rendered)
		}
	}
}

func TestRenderGoWorkIncludesConfiguredModulesAndSubprojects(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		GoWork: &goWorkConfig{
			Go:  "1.26",
			Use: []string{"tools/byoo", "tools/sync-synthetic-imports", "tools/generate-subproject-ci"},
		},
		Profiles: map[string]profile{
			"go-library": {
				Image: "golang:1.26-bookworm",
				Checks: []check{
					{ID: "vendor", Type: "go-vendor"},
				},
			},
		},
		Subprojects: []subproject{
			{ID: "nvcf-go", Path: "src/libraries/go/lib", Profile: "go-library", GoWork: true},
			{ID: "ignored", Path: "src/control-plane-services/helm-reval", Profile: "go-library"},
		},
	}

	rendered, err := renderGoWork(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderGoWork failed: %v", err)
	}

	for _, needle := range []string{
		"// Generated by go run -C tools/generate-subproject-ci . --config tools/ci/subproject-validations.yaml --go-work-output go.work.",
		"go 1.26",
		"./src/libraries/go/lib",
		"./tools/byoo",
		"./tools/generate-subproject-ci",
		"./tools/sync-synthetic-imports",
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered go.work missing %q\n%s", needle, rendered)
		}
	}

	if strings.Contains(rendered, "./src/control-plane-services/helm-reval") {
		t.Fatalf("rendered go.work should not include roots without go_work enabled\n%s", rendered)
	}
}
