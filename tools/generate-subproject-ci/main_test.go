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
	"os"
	"path/filepath"
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
						Command: "make codegen-update",
						Install: []string{"k8s.io/code-generator/cmd/deepcopy-gen@v0.34.2"},
					},
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
				ID:      "go-lib",
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
		"go-lib-vendor:",
		"go-lib-codegen:",
		"go-lib-unit-tests:",
		"./tools/scripts/update-go-work",
		"./tools/ci/check-go-vendor 'src/libraries/go/lib'",
		"./tools/ci/check-go-codegen 'src/libraries/go/lib' --command 'make codegen-update' --install 'k8s.io/code-generator/cmd/deepcopy-gen@v0.34.2'",
		"./tools/ci/run-go-unit-tests 'src/libraries/go/lib' --results-dir 'public/go-lib'",
		`GOWORK: $CI_PROJECT_DIR/go.work`,
		"PARENT_PIPELINE_SOURCE",
		"src/libraries/go/lib/**/*",
		"public/go-lib/report.json",
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered pipeline missing %q\n%s", needle, rendered)
		}
	}
}

func TestRenderPipelineHonorsPerCheckImageOverride(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles: map[string]profile{
			"go-library": {
				Stage: "validate",
				Image: "golang:1.26-bookworm",
				Checks: []check{
					{ID: "license", Type: "shell", Command: "./scripts/ci_check_license"},
					{
						ID:      "lint",
						Type:    "shell",
						Image:   "golangci/golangci-lint:v2.3.0",
						Command: "golangci-lint run",
					},
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

	licenseSection := extractJobBlock(t, rendered, "nvcf-go-license")
	if !strings.Contains(licenseSection, "image: golang:1.26-bookworm") {
		t.Fatalf("license job should use the profile image, got:\n%s", licenseSection)
	}

	lintSection := extractJobBlock(t, rendered, "nvcf-go-lint")
	if !strings.Contains(lintSection, "image: golangci/golangci-lint:v2.3.0") {
		t.Fatalf("lint job should use the per-check image override, got:\n%s", lintSection)
	}
	if strings.Contains(lintSection, "image: golang:1.26-bookworm") {
		t.Fatalf("lint job should not inherit the profile image when overridden, got:\n%s", lintSection)
	}
}

func TestRenderPipelineSkipsWorkspaceSetupWhenChecked(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles: map[string]profile{
			"go-library": {
				Stage: "validate",
				Image: "golang:1.26-bookworm",
				Variables: map[string]string{
					"GOWORK": "$CI_PROJECT_DIR/go.work",
				},
				Checks: []check{
					{ID: "vendor", Type: "go-vendor"},
					{
						ID:                 "lint",
						Type:               "shell",
						Image:              "golangci/golangci-lint:v2.3.0",
						SkipWorkspaceSetup: true,
						Command:            "golangci-lint run",
					},
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

	vendorSection := extractJobBlock(t, rendered, "nvcf-go-vendor")
	if !strings.Contains(vendorSection, "./tools/scripts/update-go-work") {
		t.Fatalf("vendor job should keep workspace setup (profile sets GOWORK), got:\n%s", vendorSection)
	}

	lintSection := extractJobBlock(t, rendered, "nvcf-go-lint")
	if strings.Contains(lintSection, "./tools/scripts/update-go-work") {
		t.Fatalf("lint job opted out via skip_workspace_setup; setup script must not appear, got:\n%s", lintSection)
	}
	if !strings.Contains(lintSection, "golangci-lint run") {
		t.Fatalf("lint job should still emit the check command, got:\n%s", lintSection)
	}
}

func extractJobBlock(t *testing.T, rendered, jobName string) string {
	t.Helper()
	marker := "\n" + jobName + ":\n"
	idx := strings.Index(rendered, marker)
	if idx < 0 {
		t.Fatalf("job %q not found in:\n%s", jobName, rendered)
	}
	rest := rendered[idx+1:]
	// A job block ends at the next top-level key (line starting with a
	// non-whitespace character) or at end of file.
	end := len(rest)
	for i := 0; i < len(rest); i++ {
		if rest[i] != '\n' {
			continue
		}
		if i+1 < len(rest) && rest[i+1] != ' ' && rest[i+1] != '\n' && rest[i+1] != '#' {
			end = i
			break
		}
	}
	return rest[:end]
}

func TestRepositoryCITriggersNVCFCLIChildPipeline(t *testing.T) {
	rootCI := readRepoFile(t, ".gitlab-ci.yml")
	cliCI := readRepoFile(t, "src/clis/nvcf-cli/.gitlab-ci.yml")

	for _, needle := range []string{
		"nvcf-cli-ci:",
		"local: src/clis/nvcf-cli/.gitlab-ci.yml",
		"src/clis/nvcf-cli/**/*",
		"ai-tooling/user/skills/nvcf-self-managed-cli/**/*",
		"ai-tooling/user/skills/nvcf-self-managed-installation/**/*",
	} {
		if !strings.Contains(rootCI, needle) {
			t.Fatalf("root CI missing %q", needle)
		}
	}

	for _, needle := range []string{
		`if: $CI_PIPELINE_SOURCE == "parent_pipeline"`,
		`CLI_DIR: "src/clis/nvcf-cli"`,
		`cd "$CI_PROJECT_DIR/$CLI_DIR"`,
		"src/clis/nvcf-cli/build/",
		"src/clis/nvcf-cli/archives/",
	} {
		if !strings.Contains(cliCI, needle) {
			t.Fatalf("CLI CI missing %q", needle)
		}
	}
}

func readRepoFile(t *testing.T, repoRelPath string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", repoRelPath))
	if err != nil {
		t.Fatalf("read %s: %v", repoRelPath, err)
	}
	return string(body)
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
			{ID: "go-lib", Path: "src/libraries/go/lib", Profile: "go-library"},
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

	if !strings.Contains(rendered, "go-lib-vendor:") {
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
				ID:      "go-lib",
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
		"go-lib-integration:",
		"./tools/scripts/update-go-work",
		`cd "$CI_PROJECT_DIR/src/libraries/go/lib" && GOWORK="$CI_PROJECT_DIR/go.work" go test ./...`,
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered pipeline missing %q\n%s", needle, rendered)
		}
	}
}

func TestRenderPipelineGeneratesGoToolJobsWithoutWorkspaceSetup(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		SharedChangePaths: []string{
			".gitlab-ci.yml",
			"tools/ci/**/*",
		},
		Profiles: map[string]profile{
			"go-tool": {
				Stage: "validate",
				Image: "golang:1.26-bookworm",
				Variables: map[string]string{
					"GOTOOLCHAIN": "local",
					"GOWORK":      "off",
				},
				Checks: []check{
					{ID: "unit-tests", Type: "shell", Command: "go test ./..."},
					{ID: "build", Type: "shell", Command: "go build ./..."},
				},
			},
		},
		Subprojects: []subproject{
			{
				ID:      "ncp-local-credential-provider",
				Path:    "tools/ncp-local-cluster/credential-provider-go",
				Profile: "go-tool",
				GoWork:  false,
				ChangePaths: []string{
					"tools/ncp-local-cluster/credential-provider-go/go.mod",
					"tools/ncp-local-cluster/credential-provider-go/go.sum",
					"tools/ncp-local-cluster/credential-provider-go/cmd/**/*",
					"tools/ncp-local-cluster/credential-provider-go/internal/**/*",
				},
			},
		},
	}

	rendered, err := renderPipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderPipeline failed: %v", err)
	}

	for _, needle := range []string{
		"ncp-local-credential-provider-unit-tests:",
		"ncp-local-credential-provider-build:",
		`GOWORK: "off"`,
		`cd "$CI_PROJECT_DIR/tools/ncp-local-cluster/credential-provider-go" && go test ./...`,
		`cd "$CI_PROJECT_DIR/tools/ncp-local-cluster/credential-provider-go" && go build ./...`,
		"tools/ncp-local-cluster/credential-provider-go/go.mod",
		"tools/ncp-local-cluster/credential-provider-go/internal/**/*",
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered pipeline missing %q\n%s", needle, rendered)
		}
	}

	if strings.Contains(rendered, "./tools/scripts/update-go-work") {
		t.Fatalf("standalone Go tool jobs should not update go.work\n%s", rendered)
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
			{ID: "go-lib", Path: "src/libraries/go/lib", Profile: "go-library", GoWork: true},
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

func TestRenderReleasePipelineEmitsPerServiceJobs(t *testing.T) {
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
			{
				ID:          "grpc-proxy",
				Path:        "src/invocation-plane-services/grpc-proxy",
				ChangePaths: []string{"src/invocation-plane-services/grpc-proxy/**/*"},
				Release: &releaseConfig{
					ServiceName: "nvcf-grpc-proxy",
					ImagePushTargets: []releaseImagePushTarget{
						{
							Name:        "kaze",
							BazelTarget: "//nvidia-internal:image_push_kaze",
							Auth: releaseImagePushAuth{
								Type:     "vault",
								VaultKey: "nvcf-grpc-proxy",
							},
						},
						{
							Name:        "ncp_dev",
							BazelTarget: "//nvidia-internal:image_push_ncp_dev",
							Auth: releaseImagePushAuth{
								Type:  "ci_var",
								CIVar: "NGC_DEVOPS_API_KEY",
							},
						},
					},
				},
			},
		},
	}

	rendered, err := renderReleasePipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderReleasePipeline: %v", err)
	}

	wants := []string{
		".compute-next-release-version-service:",
		".semantic-release-service:",
		"compute-next-release-version-grpc-proxy:",
		"semantic-release-grpc-proxy:",
		"grpc-proxy-image-push:",
		"SERVICE_NAME: nvcf-grpc-proxy",
		"SUBTREE: src/invocation-plane-services/grpc-proxy",
		"NGC_REGISTRY_VAULT_KEY: nvcf-grpc-proxy",
		"//nvidia-internal:image_push_kaze",
		"//nvidia-internal:image_push_ncp_dev",
		"NGC_DEVOPS_API_KEY",
		"NVCF_VERSION=\"${CI_COMMIT_TAG#nvcf-grpc-proxy-v}\"",
		"&grpc_proxy_release_paths",
		"*grpc_proxy_release_paths",
		"if: $CI_COMMIT_TAG =~ /^nvcf-grpc-proxy-v/",
	}
	for _, w := range wants {
		if !strings.Contains(rendered, w) {
			t.Errorf("rendered release pipeline missing %q\n---\n%s\n---", w, rendered)
		}
	}
}

func TestValidateReleaseRequiresServiceName(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles: map[string]profile{
			"p": {Image: "i", Checks: []check{{ID: "c", Type: "shell", Command: "true"}}},
		},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: "p",
				Release: &releaseConfig{
					ImagePushTargets: []releaseImagePushTarget{
						{Name: "k", BazelTarget: "//k", Auth: releaseImagePushAuth{Type: "vault", VaultKey: "k"}},
					},
				},
			},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "release.service_name") {
		t.Fatalf("expected service_name error, got: %v", err)
	}
}

func TestValidateReleaseRequiresImagePushTargets(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:      "svc",
				Path:    "p",
				Release: &releaseConfig{ServiceName: "nvcf-svc"},
			},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "image_push_targets must not be empty") {
		t.Fatalf("expected image_push_targets error, got: %v", err)
	}
}

func TestValidateReleaseRejectsUnknownAuthType(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: "p",
				Release: &releaseConfig{
					ServiceName: "nvcf-svc",
					ImagePushTargets: []releaseImagePushTarget{
						{Name: "k", BazelTarget: "//k", Auth: releaseImagePushAuth{Type: "kerberos"}},
					},
				},
			},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "unsupported auth type") {
		t.Fatalf("expected unsupported auth type, got: %v", err)
	}
}

func TestSubprojectMustHaveProfileOrRelease(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{ID: "svc", Path: "p"},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "must set profile or release") {
		t.Fatalf("expected profile-or-release error, got: %v", err)
	}
}
