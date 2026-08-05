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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cleanupScheduleSkipRule = "if: '$CI_PIPELINE_SOURCE == \"schedule\" && $NVCF_RELEASE_TAG_CLEANUP == \"true\"'\n      when: never"

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
		"workflow:\n  name: subproject-validations",
		"default:",
		"retry:",
		"runner_system_failure",
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

func TestRenderPipelineCanSkipSharedChangePaths(t *testing.T) {
	cfg := configFile{
		Version:           1,
		DefaultTags:       []string{"eks"},
		SharedChangePaths: []string{".gitlab-ci.yml", "tools/ci/subproject-validations.yaml"},
		Profiles: map[string]profile{
			"docs": {
				Stage: "validate",
				Image: "dockerhub.nvidia.com/node:24-alpine",
				Checks: []check{
					{ID: "lint", Type: "shell", Command: "./tools/ci/check-docs"},
				},
			},
		},
		Subprojects: []subproject{
			{
				ID:                    "docs",
				Path:                  ".",
				Profile:               "docs",
				SkipSharedChangePaths: true,
				SkipGlobalTriggers:    true,
				ChangePaths:           []string{"docs/**/*", "fern/**/*"},
			},
		},
	}

	rendered, err := renderPipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderPipeline failed: %v", err)
	}

	docsSection := extractJobBlock(t, rendered, "docs-lint")
	for _, needle := range []string{"docs/**/*", "fern/**/*"} {
		if !strings.Contains(docsSection, needle) {
			t.Fatalf("docs job should include docs change path %q, got:\n%s", needle, docsSection)
		}
	}
	for _, needle := range []string{".gitlab-ci.yml", "tools/ci/subproject-validations.yaml"} {
		if strings.Contains(docsSection, needle) {
			t.Fatalf("docs job should not include shared change path %q, got:\n%s", needle, docsSection)
		}
	}
	if strings.Contains(docsSection, "PARENT_PIPELINE_SOURCE") {
		t.Fatalf("docs job should not include global schedule/web trigger, got:\n%s", docsSection)
	}
}

func TestRepositoryCITriggersNVCFCLIChildPipeline(t *testing.T) {
	rootCI := readRepoFile(t, ".gitlab-ci.yml")
	cliCI := readRepoFile(t, "tools/ci/nvcf-cli.yml")

	for _, needle := range []string{
		"nvcf-cli-ci:",
		"local: tools/ci/nvcf-cli.yml",
		"default:\n  tags: [eks, nvcf-cds, prod]\n  retry:\n    max: 2\n    when:\n      - runner_system_failure",
		`BAZEL_CI_VERSION: &bazel_ci_version "0.8.0"`,
		"BAZEL_CI_VERSION: *bazel_ci_version",
		"src/clis/nvcf-cli/**/*",
		"ai-tooling/user/skills/nvcf-self-managed-cli/**/*",
		"ai-tooling/user/skills/nvcf-self-managed-installation/**/*",
	} {
		if !strings.Contains(rootCI, needle) {
			t.Fatalf("root CI missing %q", needle)
		}
	}
	if strings.Contains(rootCI, "BAZEL_CI_VERSION: $BAZEL_CI_VERSION") {
		t.Fatalf("root CI must pass a concrete BAZEL_CI_VERSION into child pipelines; forwarding a dollar expression reaches child image tags literally")
	}

	for _, needle := range []string{
		"workflow:\n  name: nvcf-cli-ci\n  rules:",
		`if: $CI_PIPELINE_SOURCE == "parent_pipeline"`,
		"default:\n  tags:\n    - prod\n    - eks\n    - nvcf-cds\n  retry:\n    max: 2\n    when:\n      - runner_system_failure",
		`CLI_DIR: "src/clis/nvcf-cli"`,
		`cd "$CI_PROJECT_DIR/$CLI_DIR"`,
		"src/clis/nvcf-cli/build/",
		"src/clis/nvcf-cli/archives/",
	} {
		if !strings.Contains(cliCI, needle) {
			t.Fatalf("CLI CI missing %q", needle)
		}
	}
	if strings.Contains(cliCI, `if: $CI_PIPELINE_SOURCE == "merge_request_event"`) {
		t.Fatalf("CLI release child pipeline should not run as a standalone MR pipeline; MR validation belongs in subproject-validations")
	}
}

func TestRepositoryNVCFCLIMRValidationExcludesReleaseBinaryBuild(t *testing.T) {
	cfg, err := loadConfig(filepath.Join("..", "..", "tools/ci/subproject-validations.yaml"))
	if err != nil {
		t.Fatalf("load subproject validations config: %v", err)
	}
	rendered, err := renderPipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("render subproject validations pipeline: %v", err)
	}

	for _, jobName := range []string{
		"nvcf-cli-check-agent-skill-generated",
		"nvcf-cli-verify-agent-skill-manifest",
		"nvcf-cli-test",
	} {
		extractJobBlock(t, rendered, jobName)
	}
	if strings.Contains(rendered, "\nnvcf-cli-build-release-binaries:\n") {
		t.Fatalf("nvcf-cli release binary build should stay out of generated MR validation\n%s", rendered)
	}
}

func TestRepositoryNativeSubprojectsDoNotCarryGitLabCI(t *testing.T) {
	for _, path := range []string{
		"deploy/helm/container-cache/.gitlab-ci.yml",
		"deploy/helm/nats/.gitlab-ci.yml",
		"src/compute-plane-services/ess-agent/.gitlab-ci.yml",
		"src/compute-plane-services/image-credential-helper/.gitlab-ci.yml",
		"src/compute-plane-services/nvcf-unbound/.gitlab-ci.yml",
		"src/control-plane-services/function-autoscaler/.gitlab-ci.yml",
		"src/control-plane-services/helm-reval/.gitlab-ci.yml",
		"src/control-plane-services/nats-auth-callout/.gitlab-ci.yml",
		"src/invocation-plane-services/grpc-proxy/.gitlab-ci.yml",
		"src/invocation-plane-services/http-invocation/.gitlab-ci.yml",
		"src/invocation-plane-services/llm-api-gateway/.gitlab-ci.yml",
		"src/invocation-plane-services/ratelimiter/.gitlab-ci.yml",
		"src/invocation-plane-services/vanity-gateway/.gitlab-ci.yml",
		"src/clis/nvcf-cli/.gitlab-ci.yml",
	} {
		if _, err := os.Stat(filepath.Join("..", "..", path)); err == nil {
			t.Fatalf("native subproject CI file %s should live in root CI tooling, not under the subproject", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
}

func TestRepositoryNativeBazelJobsAreGeneratedChildPipelineJobs(t *testing.T) {
	rootCI := readRepoFile(t, ".gitlab-ci.yml")
	config := readRepoFile(t, "tools/ci/subproject-validations.yaml")
	bazelIgnore := readRepoFile(t, ".bazelignore")
	goLibBuild := readRepoFile(t, "src/libraries/go/lib/BUILD.bazel")

	for _, unwanted := range []string{
		"go-lib-bazel-build-test:",
		"go-worker-bazel-build-test:",
		"worker-init-bazel-build-test:",
		"worker-utils-bazel-build-test:",
		"worker-llm-credentials-bazel-build-test:",
		"grpc-proxy-bazel-build-test:",
		"ratelimiter-bazel-build-test:",
		"function-autoscaler-bazel-build-test:",
		"http-invocation-bazel-build-test:",
		"nats-auth-callout-bazel-build-test:",
		"nvcf-unbound-bazel-build-test:",
		"helm-reval-bazel-build-test:",
		"llm-api-gateway-bazel-build-test:",
		"vanity-gateway-bazel-build-test:",
		"ess-agent-bazel-build-test:",
		"image-credential-helper-bazel-build-test:",
		"nvca-bazel-build-test:",
		"worker-task-bazel-build-test:",
	} {
		if strings.Contains(rootCI, unwanted) {
			t.Fatalf("root CI should not declare native subtree job %q; it belongs in the generated subproject-validations child pipeline", unwanted)
		}
	}

	for _, needle := range []string{
		"bazel_shared_change_paths:",
		"rules/**/*",
		"  - id: go-lib",
		"    bazel:",
		"      extra_test_targets:",
		"        - //src/libraries/go/lib:golangci_lint",
		"    needs_host_go: true",
		"Lint is intentionally omitted here because go-lib's Bazel lane owns",
		"  - id: go-worker",
		"    bazel:",
		"      targets:",
		"        - //src/libraries/go/worker/...",
		"  - id: worker-init",
		"    path: src/compute-plane-services/worker-init",
		"  - id: worker-utils",
		"    path: src/compute-plane-services/worker-utils",
		"  - id: worker-llm-credentials",
		"    path: src/compute-plane-services/worker-llm-credentials",
		"  - id: grpc-proxy",
		"    bazel:",
		"      dind: true",
		"  - id: nats-auth-callout",
		"src/control-plane-services/nats-auth-callout/**/*",
		"  - id: nvca",
		"    path: src/compute-plane-services/nvca",
		"    profile: nvca-toolbox",
		"    needs_host_go: true",
		"      - id: generated-files",
		"        type: go-codegen",
		"          make codegen-update &&",
		"          make dotenv-dependencies-update",
		"      setup_envtest: true",
		"      extra_build_targets:",
		"        - //cmd/nvca:image_index",
		"src/compute-plane-services/nvca/v<VERSION>-dev.N tags",
		"      version_file: VERSION",
		"      dev_prerelease: true",
		"          - name: nvca",
		"command: $CI_PROJECT_DIR/tools/ci/run-nvca-check go-update",
	} {
		if !strings.Contains(config, needle) {
			t.Fatalf("subproject validation config missing %q", needle)
		}
	}
	if strings.Contains(config, "command: make lint") {
		t.Fatalf("go-lib lint should run through Bazel, not the legacy shell make target")
	}
	if !strings.Contains(goLibBuild, "name = \"golangci_lint\"") {
		t.Fatalf("go-lib BUILD.bazel must expose the golangci-lint Bazel test target")
	}
	if !strings.Contains(goLibBuild, "env_inherit = [\"PATH\"]") {
		t.Fatalf("go-lib golangci-lint Bazel target must inherit PATH for CI's installed host Go")
	}
	if !strings.Contains(goLibBuild, "\"manual\"") {
		t.Fatalf("go-lib golangci-lint Bazel target must stay out of wildcard //... jobs")
	}

	cfg, err := loadConfig(filepath.Join("..", "..", "tools/ci/subproject-validations.yaml"))
	if err != nil {
		t.Fatalf("load subproject validations config: %v", err)
	}
	rendered, err := renderPipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("render subproject validations pipeline: %v", err)
	}
	if !strings.Contains(rendered, "go-lib-bazel-build-test:") {
		t.Fatalf("go-lib should use generated Bazel build/test validation\n%s", rendered)
	}
	goLibBazelSection := extractJobBlock(t, rendered, "go-lib-bazel-build-test")
	for _, needle := range []string{
		"src/libraries/go/lib/.goheader.tmpl",
		"src/libraries/go/lib/.golangci.yml",
		"src/libraries/go/lib/BUILD.bazel",
		"rules/**/*",
	} {
		if !strings.Contains(goLibBazelSection, needle) {
			t.Fatalf("go-lib Bazel job rules must include lint target/config path %q, got:\n%s", needle, goLibBazelSection)
		}
	}
	if !strings.Contains(goLibBazelSection, "BAZEL_SUBTREE_EXTRA_TEST_TARGETS: //src/libraries/go/lib:golangci_lint") {
		t.Fatalf("go-lib Bazel job must explicitly run the manual golangci-lint target, got:\n%s", goLibBazelSection)
	}
	if strings.Contains(rendered, "go-lib-lint:") {
		t.Fatalf("go-lib lint should be covered by the Bazel test lane, not emitted as a shell job\n%s", rendered)
	}
	if !strings.Contains(rendered, "go-worker-bazel-build-test:") {
		t.Fatalf("go-worker should use generated Bazel build/test validation\n%s", rendered)
	}
	if !strings.Contains(rendered, "nvca-bazel-build-test:") {
		t.Fatalf("nvca should use generated Bazel build/test validation\n%s", rendered)
	}
	if !strings.Contains(rendered, "nvca-generated-files:") {
		t.Fatalf("nvca should combine generated-file drift checks into one shared helper job\n%s", rendered)
	}
	nvcaGeneratedSection := extractJobBlock(t, rendered, "nvca-generated-files")
	for _, needle := range []string{
		"./tools/ci/check-go-codegen 'src/compute-plane-services/nvca'",
		"make codegen-update && make openapigen-update && make testdata-update && nv_components gen-gitlab-ci >./pipelines/components.yml && make docs-update && make dotenv-dependencies-update",
		"image: gitlab-master.nvidia.com:5005/egx/tools/toolbox:15",
	} {
		if !strings.Contains(nvcaGeneratedSection, needle) {
			t.Fatalf("nvca generated-files job missing %q, got:\n%s", needle, nvcaGeneratedSection)
		}
	}
	for _, unwanted := range []string{
		"nvca-codegen:",
		"nvca-openapi:",
		"nvca-testdata:",
		"nvca-generated-gitlab-ci:",
		"nvca-docs:",
		"nvca-dotenv-dependencies:",
	} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("nvca generated-file drift checks should be combined into nvca-generated-files; found %q\n%s", unwanted, rendered)
		}
	}
	nvcaBazelSection := extractJobBlock(t, rendered, "nvca-bazel-build-test")
	for _, needle := range []string{
		"NVCF_BAZEL_SETUP_ENVTEST: \"1\"",
		"INSTALL_GO: \"1\"",
		"BAZEL_SUBTREE_EXTRA_BUILD_TARGETS: //cmd/nvca:image_index //cmd/nvca-operator:image_index //cmd/cluster-validator:image_index //cmd/tools:image_index",
		"tools/ci/run-nvca-check",
		"tools/ci/setup-subtree-envtest",
	} {
		if !strings.Contains(nvcaBazelSection, needle) {
			t.Fatalf("nvca Bazel job missing %q, got:\n%s", needle, nvcaBazelSection)
		}
	}
	if !strings.Contains(rendered, `"$CI_PROJECT_DIR/tools/ci/setup-subtree-envtest" "$PWD/scripts/setup_envtest"`) {
		t.Fatalf("shared Bazel subtree template must source setup-subtree-envtest when enabled\n%s", rendered)
	}
	for _, needle := range []string{
		"bazel build //src/libraries/go/worker/... ${BAZEL_REMOTE_FLAGS}",
		"bazel test //src/libraries/go/worker/... ${BAZEL_REMOTE_FLAGS} --flaky_test_attempts=3",
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("go-worker Bazel job missing narrow target command %q\n%s", needle, rendered)
		}
	}
	if strings.Contains(rendered, "go-lib-unit-tests:") {
		t.Fatalf("go-lib unit-tests job overlaps with Bazel build/test and should not be emitted\n%s", rendered)
	}
	if strings.Contains(rendered, "go-worker-unit-tests:") || strings.Contains(rendered, "go-worker-build:") {
		t.Fatalf("go-worker shell build/test jobs overlap with Bazel build/test and should not be emitted\n%s", rendered)
	}
	for _, workerService := range []struct {
		id   string
		path string
	}{
		{id: "worker-init", path: "src/compute-plane-services/worker-init"},
		{id: "worker-utils", path: "src/compute-plane-services/worker-utils"},
		{id: "worker-llm-credentials", path: "src/compute-plane-services/worker-llm-credentials"},
		{id: "worker-task", path: "src/compute-plane-services/worker-task"},
	} {
		jobName := workerService.id + "-bazel-build-test"
		section := extractJobBlock(t, rendered, jobName)
		for _, needle := range []string{
			"extends: .bazel-subtree",
			"SUBTREE: " + workerService.path,
			workerService.path + "/**/*",
		} {
			if !strings.Contains(section, needle) {
				t.Fatalf("%s missing %q\n%s", jobName, needle, section)
			}
		}
		for _, legacyJobName := range []string{
			workerService.id + "-build:",
			workerService.id + "-unit-tests:",
		} {
			if strings.Contains(rendered, legacyJobName) {
				t.Fatalf("%s should use the Bazel build/test lane only; found legacy job %q\n%s", workerService.id, legacyJobName, rendered)
			}
		}
	}
	if !strings.Contains(bazelIgnore, "src/libraries/go/lib/vendor") {
		t.Fatalf("root .bazelignore must exclude vendored BUILD files from go-lib's //... Bazel job")
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

func TestRenderPipelineGeneratesNativeBazelJobs(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		BazelSharedChangePaths: []string{
			".bazelrc",
			"MODULE.bazel",
			"rules/**/*",
		},
		Subprojects: []subproject{
			{
				ID:          "nats-auth-callout",
				Path:        "src/control-plane-services/nats-auth-callout",
				ChangePaths: []string{"src/control-plane-services/nats-auth-callout/**/*"},
				Bazel:       &bazelConfig{ExtraTestTargets: []string{"//src/control-plane-services/nats-auth-callout:lint"}},
			},
			{
				ID:          "grpc-proxy",
				Path:        "src/invocation-plane-services/grpc-proxy",
				ChangePaths: []string{"src/invocation-plane-services/grpc-proxy/**/*"},
				Bazel:       &bazelConfig{Dind: true},
			},
			{
				ID:          "go-worker",
				Path:        "src/libraries/go/worker",
				ChangePaths: []string{"src/libraries/go/worker/**/*"},
				Bazel:       &bazelConfig{Targets: []string{"//src/libraries/go/worker/..."}},
			},
			{
				ID:          "nvca",
				Path:        "src/compute-plane-services/nvca",
				ChangePaths: []string{"src/compute-plane-services/nvca/**/*", "tools/ci/run-nvca-check", "tools/ci/setup-subtree-envtest"},
				NeedsHostGo: true,
				Bazel: &bazelConfig{
					SetupEnvtest: true,
					ExtraBuildTargets: []string{
						"//cmd/nvca:image_index",
						"//cmd/nvca-operator:image_index",
						"//cmd/cluster-validator:image_index",
						"//cmd/tools:image_index",
					},
				},
			},
		},
	}

	rendered, err := renderPipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderPipeline failed: %v", err)
	}

	for _, needle := range []string{
		".bazel-remote-probe:",
		".bazel-subtree:",
		"image: $CI_REGISTRY_IMAGE/bazel-ci:$BAZEL_CI_VERSION",
		"runner_system_failure",
		"BAZEL_SUBTREE_TEST_FLAGS=\"--test_env=PATH=${GO_DIR}:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin --test_env=GOROOT=${GO_ROOT}\"",
		"bazel build //... ${BAZEL_REMOTE_FLAGS}",
		"bazel test //... ${BAZEL_REMOTE_FLAGS} ${BAZEL_SUBTREE_EXTRA_TEST_TARGETS:-} ${BAZEL_SUBTREE_TEST_FLAGS:-} --flaky_test_attempts=3",
		"nats-auth-callout-bazel-build-test:",
		"extends: .bazel-subtree",
		"BAZEL_SUBTREE_EXTRA_TEST_TARGETS: //src/control-plane-services/nats-auth-callout:lint",
		"SUBTREE: src/control-plane-services/nats-auth-callout",
		"src/control-plane-services/nats-auth-callout/**/*",
		".bazelrc",
		"MODULE.bazel",
		"grpc-proxy-bazel-build-test:",
		"- dind",
		"name: docker:dind",
		"NVCF_BAZEL_DIND: \"1\"",
		"TESTCONTAINERS_RYUK_DISABLED: \"true\"",
		"go-worker-bazel-build-test:",
		"bazel build //src/libraries/go/worker/... ${BAZEL_REMOTE_FLAGS}",
		"bazel test //src/libraries/go/worker/... ${BAZEL_REMOTE_FLAGS} --flaky_test_attempts=3",
		"nvca-bazel-build-test:",
		"INSTALL_GO: \"1\"",
		"NVCF_BAZEL_SETUP_ENVTEST: \"1\"",
		"BAZEL_SUBTREE_EXTRA_BUILD_TARGETS: //cmd/nvca:image_index //cmd/nvca-operator:image_index //cmd/cluster-validator:image_index //cmd/tools:image_index",
		`"$CI_PROJECT_DIR/tools/ci/setup-subtree-envtest" "$PWD/scripts/setup_envtest"`,
		"bazel build ${BAZEL_SUBTREE_EXTRA_BUILD_TARGETS} ${BAZEL_REMOTE_FLAGS}",
	} {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered pipeline missing %q\n%s", needle, rendered)
		}
	}
}

func TestRenderPipelineGeneratesHelmValidateJobs(t *testing.T) {
	cfg := configFile{
		Version:           1,
		DefaultTags:       []string{"eks", "prod"},
		SharedChangePaths: []string{".gitlab-ci.yml", "tools/ci/subproject-validations.yaml"},
		Subprojects: []subproject{
			{
				ID:          "gateway-routes",
				Path:        "deploy/helm/gateway-routes",
				ChangePaths: []string{"deploy/helm/gateway-routes/**/*", "tools/ci/helm-validate-values/gateway-routes.yaml"},
				HelmValidate: &helmValidateConfig{
					ValuesFile: "tools/ci/helm-validate-values/gateway-routes.yaml",
				},
				Release: &releaseConfig{
					ServiceName: "nvcf-gateway-routes",
					Helm: &helmConfig{
						ChartPath: "chart",
						PushTargets: []helmPushTarget{
							{Name: "ncp-dev", NGCPath: "0651155215864979/ncp-dev", Auth: &releaseImagePushAuth{Type: "vault_ncp_dev"}},
						},
					},
				},
			},
			{
				ID:          "standalone-chart",
				Path:        "deploy/helm/standalone-chart",
				ChangePaths: []string{"deploy/helm/standalone-chart/**/*"},
				HelmValidate: &helmValidateConfig{
					ChartPath: "helm",
				},
			},
		},
	}

	rendered, err := renderPipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderPipeline failed: %v", err)
	}

	gatewaySection := extractJobBlock(t, rendered, "gateway-routes-helm-lint-template")
	for _, needle := range []string{
		"image: alpine:3.20",
		`cd "$CI_PROJECT_DIR/deploy/helm/gateway-routes" && apk add --no-cache helm && "$CI_PROJECT_DIR/tools/ci/validate-helm-chart.sh" 'chart' -f "$CI_PROJECT_DIR/tools/ci/helm-validate-values/gateway-routes.yaml"`,
		"tools/ci/subproject-validations.yaml",
		"tools/ci/helm-validate-values/gateway-routes.yaml",
	} {
		if !strings.Contains(gatewaySection, needle) {
			t.Fatalf("gateway helm validate job missing %q\n%s", needle, gatewaySection)
		}
	}

	standaloneSection := extractJobBlock(t, rendered, "standalone-chart-helm-lint-template")
	if !strings.Contains(standaloneSection, `"$CI_PROJECT_DIR/tools/ci/validate-helm-chart.sh" 'helm'`) {
		t.Fatalf("standalone helm validate job should use explicit chart_path, got:\n%s", standaloneSection)
	}
}

func TestValidateHelmValidateRequiresChartPathWithoutReleaseHelm(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Subprojects: []subproject{
			{
				ID:           "chart",
				Path:         "deploy/helm/chart",
				HelmValidate: &helmValidateConfig{},
			},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "helm_validate.chart_path") {
		t.Fatalf("expected helm_validate chart path error, got: %v", err)
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
		"job: subproject-validations",
		"runner_system_failure",
		"--remote_upload_local_results=false",
		"//nvidia-internal:image_push_kaze",
		"//nvidia-internal:image_push_ncp_dev",
		"NGC_DEVOPS_API_KEY",
		"NVCF_VERSION=\"${CI_COMMIT_TAG#src/invocation-plane-services/grpc-proxy/v}\"",
		"&grpc_proxy_release_paths",
		"*grpc_proxy_release_paths",
		"if: $CI_COMMIT_TAG =~ /^src\\/invocation-plane-services\\/grpc-proxy\\/v/",
	}
	for _, w := range wants {
		if !strings.Contains(rendered, w) {
			t.Errorf("rendered release pipeline missing %q\n---\n%s\n---", w, rendered)
		}
	}
	computeSection := extractJobBlock(t, rendered, "compute-next-release-version-grpc-proxy")
	if !strings.Contains(computeSection, "    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH\n    # Tag pipelines need NEXT_VERSION too:") {
		t.Errorf("compute-next should sweep release services on every default-branch pipeline\n---\n%s\n---", computeSection)
	}
	if strings.Contains(computeSection, "      changes: *grpc_proxy_release_paths") {
		t.Errorf("compute-next default-branch rule should not be path-gated\n---\n%s\n---", computeSection)
	}
	semanticSection := extractJobBlock(t, rendered, "semantic-release-grpc-proxy")
	if !strings.Contains(semanticSection, cleanupScheduleSkipRule) {
		t.Errorf("semantic-release should skip cleanup-only schedules\n---\n%s\n---", semanticSection)
	}
	if !strings.Contains(semanticSection, "    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH\n") {
		t.Errorf("semantic-release should sweep release services on every default-branch pipeline\n---\n%s\n---", semanticSection)
	}
	if strings.Contains(semanticSection, "      changes: *grpc_proxy_release_paths") {
		t.Errorf("semantic-release default-branch rule should not be path-gated\n---\n%s\n---", semanticSection)
	}
	for _, w := range []string{
		`git -C "${CI_PROJECT_DIR}" log "${GUARD_RANGE}"`,
		"no commits under ${SUBTREE}",
		`if [ "${SKIP_SEMANTIC_RELEASE:-}" = "true" ]; then`,
	} {
		if !strings.Contains(rendered, w) {
			t.Errorf("rendered release pipeline missing sweep guard marker %q\n---\n%s\n---", w, rendered)
		}
	}
	if strings.Contains(rendered, "job: grpc-proxy-bazel-build-test") {
		t.Errorf("release pipeline should depend on the subproject-validations bridge, not the child-only grpc-proxy-bazel-build-test job\n---\n%s\n---", rendered)
	}
}

func TestRenderReleasePipelineUsesPathTagDefaultWithLegacyPrefix(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:          "image-credential-helper",
				Path:        "src/compute-plane-services/image-credential-helper",
				ChangePaths: []string{"src/compute-plane-services/image-credential-helper/**/*"},
				Release: &releaseConfig{
					ServiceName:     "nvcf-image-credential-helper",
					LegacyTagPrefix: "nvcf-image-credential-helper-v",
					Staging: &releaseStagingConfig{
						Images: []releaseStagingImage{
							{Name: "nvcf-image-credential-helper", BazelImageTarget: "//cmd/image-credential-helper:image_index"},
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

	computeSection := extractJobBlock(t, rendered, "compute-next-release-version-image-credential-helper")
	for _, want := range []string{
		"RELEASE_TAG_PREFIX: src/compute-plane-services/image-credential-helper/v",
		"RELEASE_LEGACY_TAG_PREFIX: nvcf-image-credential-helper-v",
		"if: $CI_COMMIT_TAG =~ /^(src\\/compute-plane-services\\/image-credential-helper\\/v|nvcf-image-credential-helper-v)/",
	} {
		if !strings.Contains(computeSection, want) {
			t.Fatalf("compute job missing %q\n%s", want, computeSection)
		}
	}
	if strings.Contains(computeSection, "RELEASE_TAG_FORMAT:") {
		t.Fatalf("compute job should pass only the release tag prefix to avoid GitLab expanding ${version}\n%s", computeSection)
	}

	semanticSection := extractJobBlock(t, rendered, "semantic-release-image-credential-helper")
	if !strings.Contains(semanticSection, "RELEASE_TAG_PREFIX: src/compute-plane-services/image-credential-helper/v") {
		t.Fatalf("semantic-release job missing path tag prefix\n%s", semanticSection)
	}
	if !strings.Contains(semanticSection, "RELEASE_LEGACY_TAG_PREFIX: nvcf-image-credential-helper-v") {
		t.Fatalf("semantic-release job missing legacy tag prefix\n%s", semanticSection)
	}
	if strings.Contains(semanticSection, "RELEASE_TAG_FORMAT:") {
		t.Fatalf("semantic-release job should derive tagFormat from RELEASE_TAG_PREFIX in before_script\n%s", semanticSection)
	}

	stageSection := extractJobBlock(t, rendered, "image-credential-helper-stage-nvcf-image-credential-helper")
	if !strings.Contains(stageSection, `NVCF_VERSION="${CI_COMMIT_TAG#src/compute-plane-services/image-credential-helper/v}"`) {
		t.Fatalf("stage job should strip custom release tag prefix\n%s", stageSection)
	}
}

func TestRenderReleasePipelineEmitsStagingManifestJobs(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:          "byoo-otel-collector",
				Path:        "src/compute-plane-services/byoo-otel-collector",
				ChangePaths: []string{"src/compute-plane-services/byoo-otel-collector/**/*"},
				NeedsHostGo: true,
				Release: &releaseConfig{
					ServiceName:                 "byoo-otel-collector",
					VersionFile:                 "VERSION",
					VersionMajorMinorSourceFile: "otel-collector-build.yaml",
					Staging: &releaseStagingConfig{
						Images: []releaseStagingImage{
							{Name: "byoo-otel-collector", BazelImageTarget: "//:byoo-otel-collector-image_index"},
							{Name: "nvcf-otel-collector", BazelImageTarget: "//:nvcf-otel-collector-image_index"},
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

	for _, want := range []string{
		"compute-next-release-version-byoo-otel-collector:",
		"RELEASE_VERSION_FILE: VERSION",
		"RELEASE_VERSION_MAJOR_MINOR_SOURCE_FILE: otel-collector-build.yaml",
		"release.version_file=${RELEASE_VERSION_FILE}",
		"skipping semantic-release dry-run because the version file is authoritative",
		"using ${RELEASE_VERSION_FILE} as NEXT_VERSION=${NEXT_VERSION}",
		"release.version_major_minor_source_file=${RELEASE_VERSION_MAJOR_MINOR_SOURCE_FILE}",
		"major/minor matches ${RELEASE_VERSION_FILE}",
		"is authoritative for NEXT_VERSION=${NEXT_VERSION}",
		"release-version-file",
		"semantic-release-byoo-otel-collector:",
		"byoo-otel-collector-stage-byoo-otel-collector:",
		"byoo-otel-collector-stage-nvcf-otel-collector:",
		// Manual pre-merge push buttons, one per image, gated to MR
		// pipelines as when: manual. Named <image>-image-push-mr-manual so
		// the UI button reads as an image push; they extend the tag-time
		// <id>-stage-<image> job plus the vault templates and push to the
		// ncp-dev NGC registry.
		"byoo-otel-collector-image-push-mr-manual:",
		"nvcf-otel-collector-image-push-mr-manual:",
		"- byoo-otel-collector-stage-byoo-otel-collector",
		"- .nv-vault",
		"- .vault-reader-settings",
		"PUSH_MR_IMAGE_TO_REGISTRY: \"true\"",
		"if [ \"${PUSH_MR_IMAGE_TO_REGISTRY:-}\" = \"true\" ]; then",
		"NVCF_VERSION=\"mr.${CI_MERGE_REQUEST_IID:-${CI_COMMIT_REF_SLUG}}-${CI_COMMIT_SHORT_SHA}\"",
		"NVCF_STAGING_REGISTRY=\"nvcr.io/0651155215864979/ncp-dev\"",
		"NVCR_0651155215864979_NCP_DEV_SERVICE_KEY_RW",
		"!reference [.vault-reader-before-script, before_script]",
		"when: manual",
		"byoo-otel-collector-release-manifest:",
		"byoo-otel-collector-trigger-internal-release:",
		"--remote_upload_local_results=false",
		"NVCF_STAGING_REGISTRY",
		"release-staging-byoo-otel-collector",
		"release-manifest.json",
		"release-images/push_byoo_otel_collector.json",
		"release-images/push_nvcf_otel_collector.json",
		"//:byoo-otel-collector-image_index",
		"//:nvcf-otel-collector-image_index",
		"project: nvcf/nvcf-internal",
		"NVCF_SERVICE_ID: byoo-otel-collector",
		"NVCF_SOURCE_MANIFEST_JOB: \"byoo-otel-collector-release-manifest\"",
		"if: $CI_COMMIT_TAG =~ /^src\\/compute-plane-services\\/byoo-otel-collector\\/v/",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered staging release pipeline missing %q\n---\n%s\n---", want, rendered)
		}
	}
	for _, unwanted := range []string{
		"byoo-otel-collector-image-push:",
		"NGC_API_KEY_PRD_nv_cf",
		"NGC_DEVOPS_API_KEY",
		"nvcr.io/qtfpt1h0bieu",
		"byoo-otel-collector-slack-notify:",
		"release-images.jsonl",
	} {
		if strings.Contains(rendered, unwanted) {
			t.Errorf("staging release pipeline should not include %q\n---\n%s\n---", unwanted, rendered)
		}
	}
}

func TestRenderReleasePipelineEmitsDevPrereleaseStagingJobs(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:          "nvca",
				Path:        "src/compute-plane-services/nvca",
				ChangePaths: []string{"src/compute-plane-services/nvca/**/*"},
				Release: &releaseConfig{
					ServiceName:   "nvca",
					VersionFile:   "VERSION",
					DevPrerelease: true,
					RCPrerelease:  true,
					Staging: &releaseStagingConfig{
						Images: []releaseStagingImage{
							{Name: "nvca", BazelImageTarget: "//cmd/nvca:image_index"},
							{Name: "nvca-operator", BazelImageTarget: "//cmd/nvca-operator:image_index"},
							{Name: "cluster-validator", BazelImageTarget: "//cmd/cluster-validator:image_index"},
							{Name: "tools", BazelImageTarget: "//cmd/tools:image_index"},
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

	for _, want := range []string{
		"compute-next-release-version-nvca:",
		"release-tag-cleanup:",
		"stage: Release-Maintenance",
		"release-branch-nvca:",
		"RELEASE_VERSION_FILE: VERSION",
		"RELEASE_VERSION_FILE_PATH: src/compute-plane-services/nvca/VERSION",
		"RELEASE_TAG_PREFIX: src/compute-plane-services/nvca/v",
		"RELEASE_BRANCH_PREFIX: release-src/compute-plane-services/nvca/v",
		"RELEASE_DEV_PRERELEASE: \"true\"",
		"release.dev_prerelease=true",
		"NEXT_VERSION=\"${RELEASE_DEV_PRERELEASE_BASE_VERSION}-dev.${NEXT_DEV}\"",
		"next release-branch version is ${GIT_TAG}",
		"RELEASE_RC_PRERELEASE: \"true\"",
		"promote-stable-nvca:",
		"python3 tools/ci/release-rc-promote",
		"next RC prerelease is",
		"semantic-release-nvca:",
		"dev-prerelease",
		"release-branch",
		"nvca-stage-nvca:",
		"nvca-stage-nvca-operator:",
		"nvca-stage-cluster-validator:",
		"nvca-stage-tools:",
		"nvca-release-manifest:",
		"nvca-trigger-internal-release:",
		"NVCF_SERVICE_ID: nvca",
		"NVCF_SOURCE_REPO_REF: \"$CI_COMMIT_TAG\"",
		"if: $CI_COMMIT_BRANCH =~ /^release-src\\/compute-plane-services\\/nvca\\/v[0-9]+\\.[0-9]+$/",
		"if: $CI_COMMIT_TAG =~ /^src\\/compute-plane-services\\/nvca\\/v/",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered dev prerelease staging pipeline missing %q\n---\n%s\n---", want, rendered)
		}
	}

	for _, job := range []string{
		"compute-next-release-version-nvca",
		"semantic-release-nvca",
		"release-branch-nvca",
	} {
		section := extractJobBlock(t, rendered, job)
		if !strings.Contains(section, cleanupScheduleSkipRule) {
			t.Errorf("%s should skip cleanup-only schedules\n---\n%s\n---", job, section)
		}
	}

	computeTemplate := extractJobBlock(t, rendered, ".compute-next-release-version-service")
	for _, want := range []string{
		`if [ -z "${CI_COMMIT_TAG:-}" ] && \`,
		`[ "${CI_COMMIT_BRANCH:-}" = "${CI_DEFAULT_BRANCH:-}" ]; then`,
		"guard also applies to VERSION-file dev prerelease services",
		"no commits under ${SUBTREE}",
	} {
		if !strings.Contains(computeTemplate, want) {
			t.Errorf("dev prerelease compute template missing guard marker %q\n---\n%s\n---", want, computeTemplate)
		}
	}
	for _, unwanted := range []string{
		`if [ -z "${RELEASE_VERSION_FILE:-}" ] && \`,
		`[ "${RELEASE_DEV_PRERELEASE:-}" != "true" ] && \`,
		"version_file services, are left untouched",
	} {
		if strings.Contains(computeTemplate, unwanted) {
			t.Errorf("dev prerelease compute template should not exclude version-file services via %q\n---\n%s\n---", unwanted, computeTemplate)
		}
	}

	releaseBranch := extractJobBlock(t, rendered, "release-branch-nvca")
	if !strings.Contains(releaseBranch, "stage: Release-Branch") {
		t.Errorf("nvca release-branch manual job should live outside publish\n---\n%s\n---", releaseBranch)
	}
	if !strings.Contains(releaseBranch, "when: manual\n      allow_failure: true") {
		t.Errorf("nvca release-branch manual job should be optional\n---\n%s\n---", releaseBranch)
	}
}

func TestRenderReleasePipelineEmitsComputeStackReleaseBranchButton(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:          "nvcf-compute-plane-stack",
				Path:        "deploy/stacks/nvcf-compute-plane",
				ChangePaths: []string{"deploy/stacks/nvcf-compute-plane/**/*"},
				Release: &releaseConfig{
					ServiceName:   "nvcf-compute-plane-stack",
					VersionFile:   "VERSION",
					DevPrerelease: true,
					ArchiveRelease: &archiveReleaseConfig{
						Subtree: "deploy/stacks/nvcf-compute-plane",
					},
				},
			},
		},
	}

	rendered, err := renderReleasePipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderReleasePipeline: %v", err)
	}

	for _, want := range []string{
		"compute-next-release-version-nvcf-compute-plane-stack:",
		"extends: .compute-next-release-version-service",
		"release-branch-compute-stack:",
		"RELEASE_SERVICE_ID: nvcf-compute-plane-stack",
		"RELEASE_TAG_PREFIX: deploy/stacks/nvcf-compute-plane/v",
		"RELEASE_BRANCH_PREFIX: release-deploy/stacks/nvcf-compute-plane/v",
		"RELEASE_VERSION_FILE_PATH: deploy/stacks/nvcf-compute-plane/VERSION",
		"RELEASE_DEV_PRERELEASE: \"true\"",
		"if: $CI_COMMIT_BRANCH =~ /^release-deploy\\/stacks\\/nvcf-compute-plane\\/v[0-9]+\\.[0-9]+$/",
		"if: $CI_COMMIT_TAG =~ /^deploy\\/stacks\\/nvcf-compute-plane\\/v/",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered compute stack release pipeline missing %q\n---\n%s\n---", want, rendered)
		}
	}
	if strings.Contains(rendered, "release-branch-nvcf-compute-plane-stack:") {
		t.Errorf("compute stack release button should use the product-facing short name\n---\n%s\n---", rendered)
	}

	releaseBranch := extractJobBlock(t, rendered, "release-branch-compute-stack")
	if !strings.Contains(releaseBranch, cleanupScheduleSkipRule) {
		t.Errorf("compute stack release-branch manual job should skip cleanup-only schedules\n---\n%s\n---", releaseBranch)
	}
	if !strings.Contains(releaseBranch, "stage: Release-Branch") {
		t.Errorf("compute stack release-branch manual job should live outside publish\n---\n%s\n---", releaseBranch)
	}
	if !strings.Contains(releaseBranch, "when: manual\n      allow_failure: true") {
		t.Errorf("compute stack release-branch manual job should be optional\n---\n%s\n---", releaseBranch)
	}
}

func TestRenderReleasePipelineEmitsInternalReleaseTriggerJobs(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:          "stargate",
				Path:        "src/libraries/rust/stargate",
				ChangePaths: []string{"src/libraries/rust/stargate/**/*"},
				Release: &releaseConfig{
					ServiceName:     "stargate",
					LegacyTagPrefix: "stargate-v",
					InternalRelease: &internalReleaseConfig{},
					SlackChannel:    "C08S6KLCEJH",
				},
			},
		},
	}

	rendered, err := renderReleasePipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderReleasePipeline: %v", err)
	}

	for _, want := range []string{
		"compute-next-release-version-stargate:",
		"semantic-release-stargate:",
		"stargate-trigger-internal-release:",
		"project: nvcf/nvcf-internal",
		"NVCF_SERVICE_ID: stargate",
		"NVCF_RELEASE_BACKEND: \"build\"",
		"NVCF_SOURCE_REPO_REF: \"$CI_COMMIT_TAG\"",
		"NVCF_SOURCE_PROJECT_PATH: \"$CI_PROJECT_PATH\"",
		"NVCF_SOURCE_COMMIT_SHA: \"$CI_COMMIT_SHA\"",
		"if: $CI_COMMIT_TAG =~ /^(src\\/libraries\\/rust\\/stargate\\/v|stargate-v)/",
		"stargate-slack-notify:",
		"job: stargate-trigger-internal-release",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered internal release pipeline missing %q\n---\n%s\n---", want, rendered)
		}
	}
	for _, unwanted := range []string{
		"stargate-image-push:",
		"stargate-release-manifest:",
		"nvidia-internal:image_push",
		"NGC_DEVOPS_API_KEY",
		"nvcr.io/0651155215864979/ncp-dev/stargate",
	} {
		if strings.Contains(rendered, unwanted) {
			t.Errorf("internal release pipeline should not include %q\n---\n%s\n---", unwanted, rendered)
		}
	}
}

func TestRenderReleasePipelineHelmPushUsesOCISurfaceWithReadback(t *testing.T) {
	// Regression guard for issue #9: helm-push-ngc@0.16.6 wrote only
	// to NGC's chart-registry API surface; self-managed-stack Helmfile
	// reads via `helm pull oci://`, which 404'd on the same tag. The
	// generator must emit a direct `helm push oci://` job with a
	// mandatory `helm show chart oci://` readback as a regression
	// guard against the same drift recurring silently.
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:          "openbao",
				Path:        "migrations/openbao",
				ChangePaths: []string{"migrations/openbao/**/*"},
				Release: &releaseConfig{
					ServiceName:     "helm-nvcf-openbao-server",
					LegacyTagPrefix: "helm-nvcf-openbao-server-v",
					Helm: &helmConfig{
						ChartPath: "helm",
						PushTargets: []helmPushTarget{
							{
								Name:    "ncp-dev",
								NGCPath: "0651155215864979/ncp-dev",
								Auth:    &releaseImagePushAuth{Type: "vault_ncp_dev"},
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

	for _, want := range []string{
		"helm-push-openbao-ncp-dev:",
		"image: $CI_REGISTRY_IMAGE/bazel-ci:$BAZEL_CI_VERSION",
		"helm registry login nvcr.io",
		`helm push "$chart_tgz" "oci://nvcr.io/${NGC_PATH}"`,
		`helm show chart "oci://nvcr.io/${NGC_PATH}/$name" --version "$version"`,
		"NVCR_0651155215864979_NCP_DEV_SERVICE_KEY_RW",
		"if: $CI_COMMIT_TAG =~ /^(migrations\\/openbao\\/v|helm-nvcf-openbao-server-v)/",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("helm push job missing %q\n---\n%s\n---", want, rendered)
		}
	}
	for _, unwanted := range []string{
		"cds-components/helm-push-ngc",
		"CC_HELM_NGC_KEY",
	} {
		if strings.Contains(rendered, unwanted) {
			t.Errorf("helm push job must not retain legacy %q (issue #9)\n---\n%s\n---", unwanted, rendered)
		}
	}
}

func TestRenderReleasePipelineEmitsSlackWhenConfigured(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:          "nats-auth-callout",
				Path:        "src/control-plane-services/nats-auth-callout",
				ChangePaths: []string{"src/control-plane-services/nats-auth-callout/**/*"},
				Release: &releaseConfig{
					ServiceName:     "nvcf-nats-auth-callout-service",
					LegacyTagPrefix: "nvcf-nats-auth-callout-service-v",
					ImagePushTargets: []releaseImagePushTarget{
						{
							Name:        "nvcf_internal",
							BazelTarget: "//nvidia-internal:image_push_nvcf_internal",
							Auth: releaseImagePushAuth{
								Type:     "vault",
								VaultKey: "nvcf-components",
							},
						},
					},
					SlackChannel: "C08S6KLCEJH",
				},
			},
		},
	}

	rendered, err := renderReleasePipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderReleasePipeline: %v", err)
	}

	for _, want := range []string{
		"nats-auth-callout-slack-notify:",
		"SLACK_CHANNEL_ID: C08S6KLCEJH",
		"backstage-helper.service.odp.nvidia.com/notify_channel",
		"src/control-plane-services/nats-auth-callout/v*) TAG_DISPLAY=\"${CI_COMMIT_TAG#src/control-plane-services/nats-auth-callout/v}\" ;;",
		"nvcf-nats-auth-callout-service-v*) TAG_DISPLAY=\"${CI_COMMIT_TAG#nvcf-nats-auth-callout-service-v}\" ;;",
		"--match='src/control-plane-services/nats-auth-callout/v*' --match='nvcf-nats-auth-callout-service-v*'",
		"nvcf-nats-auth-callout-service-v*) TAG_DISPLAY=\"${LAST_RELEASE_TAG#nvcf-nats-auth-callout-service-v}\" ;;",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("release pipeline missing %q\n---\n%s\n---", want, rendered)
		}
	}

}

func TestRenderReleasePipelineSemanticReleaseHeredocTerminatorIsBare(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:          "grpc-proxy",
				Path:        "src/invocation-plane-services/grpc-proxy",
				ChangePaths: []string{"src/invocation-plane-services/grpc-proxy/**/*"},
				Release: &releaseConfig{
					ServiceName:     "nvcf-grpc-proxy",
					LegacyTagPrefix: "nvcf-grpc-proxy-v",
					ImagePushTargets: []releaseImagePushTarget{
						{
							Name:        "kaze",
							BazelTarget: "//nvidia-internal:image_push_kaze",
							Auth: releaseImagePushAuth{
								Type:     "vault",
								VaultKey: "nvcf-grpc-proxy",
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

	want := "\n      SCRIPT_EOF\n      fi\n      . \"${CI_PROJECT_DIR}/release-tag-compat.sh\""
	if !strings.Contains(rendered, want) {
		t.Fatalf("semantic-release heredoc terminator should be bare after YAML block indentation is stripped; missing %q\n---\n%s\n---", want, rendered)
	}
	unwanted := "\n        SCRIPT_EOF\n      fi\n      . \"${CI_PROJECT_DIR}/release-tag-compat.sh\""
	if strings.Contains(rendered, unwanted) {
		t.Fatalf("semantic-release heredoc terminator is over-indented and would not close in bash\n---\n%s\n---", rendered)
	}
}

func TestRenderReleasePipelineSkipsSlackWhenEmpty(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
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
					},
				},
			},
		},
	}

	rendered, err := renderReleasePipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderReleasePipeline: %v", err)
	}

	for _, unwanted := range []string{
		"grpc-proxy-slack-notify",
		"backstage-helper.service.odp.nvidia.com",
	} {
		if strings.Contains(rendered, unwanted) {
			t.Errorf("rendered pipeline should not include %q when SlackChannel is empty\n---\n%s\n---", unwanted, rendered)
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

func TestValidateReleaseRequiresReleaseMode(t *testing.T) {
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
	if err == nil || !strings.Contains(err.Error(), "must declare at least one of image_push_targets, staging, helm, dockerfile, internal_release, or archive_release") {
		t.Fatalf("expected at-least-one-release-mode error, got: %v", err)
	}
}

func TestValidateReleaseAllowsPathTagFormat(t *testing.T) {
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
					TagFormat:   "src/compute-plane-services/svc/v${version}",
					Staging: &releaseStagingConfig{
						Images: []releaseStagingImage{
							{Name: "svc", BazelImageTarget: "//:image_index"},
						},
					},
				},
			},
		},
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("path tag format should validate, got: %v", err)
	}
}

func TestValidateReleaseRejectsInvalidTagFormat(t *testing.T) {
	tests := map[string]string{
		"no-placeholder":        "src/svc/v",
		"duplicate-placeholder": "src/svc/v${version}-${version}",
		"empty-prefix":          "${version}",
		"whitespace":            "src/svc/v ${version}",
		"absolute":              "/src/svc/v${version}",
		"refs":                  "refs/tags/src/svc/v${version}",
	}

	for name, tagFormat := range tests {
		t.Run(name, func(t *testing.T) {
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
							TagFormat:   tagFormat,
							Staging: &releaseStagingConfig{
								Images: []releaseStagingImage{
									{Name: "svc", BazelImageTarget: "//:image_index"},
								},
							},
						},
					},
				},
			}
			err := validateConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), "release.tag_format") {
				t.Fatalf("expected release.tag_format error for %q, got: %v", tagFormat, err)
			}
		})
	}
}

func TestValidateReleaseRejectsInvalidVersionFile(t *testing.T) {
	for _, versionFile := range []string{"/VERSION", "../VERSION", "chart/../VERSION"} {
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
						VersionFile: versionFile,
						Staging: &releaseStagingConfig{
							Images: []releaseStagingImage{
								{Name: "svc", BazelImageTarget: "//:image_index"},
							},
						},
					},
				},
			},
		}
		err := validateConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "release.version_file") {
			t.Fatalf("expected release.version_file error for %q, got: %v", versionFile, err)
		}
	}
}

func TestValidateReleaseRejectsInvalidVersionMajorMinorSourceFile(t *testing.T) {
	cfgWithoutVersionFile := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: "p",
				Release: &releaseConfig{
					ServiceName:                 "nvcf-svc",
					VersionMajorMinorSourceFile: "otel-collector-build.yaml",
					Staging: &releaseStagingConfig{
						Images: []releaseStagingImage{
							{Name: "svc", BazelImageTarget: "//:image_index"},
						},
					},
				},
			},
		},
	}
	err := validateConfig(cfgWithoutVersionFile)
	if err == nil || !strings.Contains(err.Error(), "requires release.version_file") {
		t.Fatalf("expected source file requires version_file error, got: %v", err)
	}

	for _, sourceFile := range []string{"/otel-collector-build.yaml", "../otel-collector-build.yaml", "config/../otel-collector-build.yaml"} {
		cfg := configFile{
			Version:     1,
			DefaultTags: []string{"eks"},
			Profiles:    map[string]profile{},
			Subprojects: []subproject{
				{
					ID:   "svc",
					Path: "p",
					Release: &releaseConfig{
						ServiceName:                 "nvcf-svc",
						VersionFile:                 "VERSION",
						VersionMajorMinorSourceFile: sourceFile,
						Staging: &releaseStagingConfig{
							Images: []releaseStagingImage{
								{Name: "svc", BazelImageTarget: "//:image_index"},
							},
						},
					},
				},
			},
		}
		err := validateConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "release.version_major_minor_source_file") {
			t.Fatalf("expected release.version_major_minor_source_file error for %q, got: %v", sourceFile, err)
		}
	}
}

func TestValidateReleaseRejectsDevPrereleaseWithoutVersionFile(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: "p",
				Release: &releaseConfig{
					ServiceName:   "nvcf-svc",
					DevPrerelease: true,
					Staging: &releaseStagingConfig{
						Images: []releaseStagingImage{
							{Name: "svc", BazelImageTarget: "//:image_index"},
						},
					},
				},
			},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "release.dev_prerelease requires release.version_file") {
		t.Fatalf("expected dev_prerelease/version_file error, got: %v", err)
	}
}

func TestValidateReleaseAllowsStagingOnly(t *testing.T) {
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
					Staging: &releaseStagingConfig{
						Images: []releaseStagingImage{
							{Name: "svc", BazelImageTarget: "//:image_index"},
						},
					},
				},
			},
		},
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("staging-only release should validate, got: %v", err)
	}
}

func TestValidateReleaseAllowsHelmOnly(t *testing.T) {
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
					Helm: &helmConfig{
						ChartPath: "deploy",
						PushTargets: []helmPushTarget{
							{Name: "ncp-dev", NGCPath: "0/x", NGCKeyVar: "NGC_DEVOPS_API_KEY"},
						},
					},
				},
			},
		},
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("helm-only release should validate, got: %v", err)
	}
}

func TestValidateReleaseAllowsArchiveReleaseOnly(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: ".",
				Release: &releaseConfig{
					ServiceName:    "nvcf-svc",
					ArchiveRelease: &archiveReleaseConfig{Subtree: "src/clis/svc"},
				},
			},
		},
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("archive_release-only release should validate, got: %v", err)
	}
}

func TestValidateReleaseAllowsInternalReleaseOnly(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: "p",
				Release: &releaseConfig{
					ServiceName:     "nvcf-svc",
					InternalRelease: &internalReleaseConfig{},
				},
			},
		},
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("internal_release-only release should validate, got: %v", err)
	}
}

func TestValidateReleaseRejectsInternalReleaseWithImagePushTargets(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: ".",
				Release: &releaseConfig{
					ServiceName: "nvcf-svc",
					ImagePushTargets: []releaseImagePushTarget{
						{Name: "ncp-dev", BazelTarget: "//:push", Auth: releaseImagePushAuth{Type: "ci_var", CIVar: "X"}, DockerAuthPath: "nvcr.io/x/y"},
					},
					InternalRelease: &internalReleaseConfig{},
				},
			},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "release.internal_release is mutually exclusive") {
		t.Fatalf("expected internal_release mutual-exclusion error, got: %v", err)
	}
}

func TestValidateReleaseRejectsArchiveReleaseWithImagePushTargets(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: ".",
				Release: &releaseConfig{
					ServiceName: "nvcf-svc",
					ImagePushTargets: []releaseImagePushTarget{
						{Name: "kaze", BazelTarget: "//:push", Auth: releaseImagePushAuth{Type: "ci_var", CIVar: "X"}, DockerAuthPath: "nvcr.io/x/y"},
					},
					ArchiveRelease: &archiveReleaseConfig{},
				},
			},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "archive_release is mutually exclusive") {
		t.Fatalf("expected archive_release/image_push_targets conflict, got: %v", err)
	}
}

func TestValidateReleaseRejectsArchiveReleaseSubtreeEscape(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: ".",
				Release: &releaseConfig{
					ServiceName:    "nvcf-svc",
					ArchiveRelease: &archiveReleaseConfig{Subtree: "../escape"},
				},
			},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "archive_release.subtree must be a clean relative path") {
		t.Fatalf("expected subtree escape rejection, got: %v", err)
	}
}

func TestValidateReleaseAllowsDockerfileOnly(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: "migrations/svc",
				Release: &releaseConfig{
					ServiceName: "nvcf-svc-migrations",
					Dockerfile: &dockerfileConfig{
						ImageName: "nvcf-svc-migrations",
					},
				},
			},
		},
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("dockerfile-only release should validate, got: %v", err)
	}
}

func TestRenderReleasePipelinePreseedsNativeVaultForDockerfileJobs(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Subprojects: []subproject{
			{
				ID:          "svc-migrations",
				Path:        "migrations/svc",
				ChangePaths: []string{"migrations/svc/**/*"},
				Release: &releaseConfig{
					ServiceName: "nvcf-svc-migrations",
					Dockerfile: &dockerfileConfig{
						Dockerfile: "Dockerfile.internal",
						ImageName:  "nvcf-svc-migrations",
					},
				},
			},
		},
	}

	rendered, err := renderReleasePipeline(cfg, "tools/ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderReleasePipeline: %v", err)
	}

	mustContain := []string{
		`case "$(uname -m)" in`,
		`vault_arch=arm64`,
		`vault_arch=amd64`,
		`nvault_agent_v${NV_VAULT_VERSION}_linux_${vault_arch}.zip`,
		`rm -f vault/bin/vault`,
		`unzip -oq "vault/${vault_zip}" -d vault/bin`,
		`vault/bin/vault version`,
	}
	for _, needle := range mustContain {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered Dockerfile release jobs missing native vault setup %q\n---\n%s\n---", needle, rendered)
		}
	}

	preseedIdx := strings.Index(rendered, `nvault_agent_v${NV_VAULT_VERSION}_linux_${vault_arch}.zip`)
	referenceIdx := strings.Index(rendered, `!reference [.nv-vault, before_script]`)
	if preseedIdx == -1 || referenceIdx == -1 || preseedIdx > referenceIdx {
		t.Fatalf("native vault pre-seed must render before .nv-vault reference\n---\n%s\n---", rendered)
	}
}

func TestRenderReleasePipelineDockerfileMultiTarget(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Subprojects: []subproject{
			{
				ID:          "stargate",
				Path:        "src/libraries/rust/stargate",
				ChangePaths: []string{"src/libraries/rust/stargate/**/*"},
				Release: &releaseConfig{
					ServiceName: "stargate",
					Dockerfile: &dockerfileConfig{
						ContextPath: ".",
						Dockerfile:  "Dockerfile",
						Images: []dockerfileImageConfig{
							{Name: "stargate", Target: "stargate-runtime"},
							{Name: "pylon", Target: "pylon-runtime"},
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

	mustContain := []string{
		// Each image builds from its own --target stage, per arch.
		`--target stargate-runtime -t $CI_REGISTRY_IMAGE/stargate:$VERSION-amd64 -f Dockerfile .`,
		`--target pylon-runtime -t $CI_REGISTRY_IMAGE/pylon:$VERSION-arm64 -f Dockerfile .`,
		// Each image pushes its own multi-arch manifest to ncp-dev.
		`buildah manifest push --all stargate:$VERSION docker://nvcr.io/0651155215864979/ncp-dev/stargate:$VERSION`,
		`buildah manifest push --all pylon:$VERSION docker://nvcr.io/0651155215864979/ncp-dev/pylon:$VERSION`,
	}
	for _, needle := range mustContain {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("multi-target Dockerfile render missing %q\n---\n%s\n---", needle, rendered)
		}
	}
}

func TestValidateReleaseRejectsDockerfileWithoutImageName(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{
				ID:   "svc",
				Path: "migrations/svc",
				Release: &releaseConfig{
					ServiceName: "nvcf-svc-migrations",
					Dockerfile:  &dockerfileConfig{},
				},
			},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "release.dockerfile must set exactly one of image_name or images") {
		t.Fatalf("expected dockerfile image_name/images error, got: %v", err)
	}
}

func TestValidateReleaseRejectsDockerfileWithImagePushTargets(t *testing.T) {
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
						{Name: "ncp_dev", BazelTarget: "//k", Auth: releaseImagePushAuth{Type: "vault_ncp_dev"}},
					},
					Dockerfile: &dockerfileConfig{
						ImageName: "img",
					},
				},
			},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "cannot declare both image_push_targets and dockerfile") {
		t.Fatalf("expected image_push_targets-vs-dockerfile conflict, got: %v", err)
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

// TestRenderReleasePipelineUsesExecPluginNotGitlabPlugin guards the pivot
// from @semantic-release/gitlab to @semantic-release/exec + release-cli.
// The legacy plugin's verifyConditions step did `git push HEAD:main` which
// only worked with a long-lived Maintainer PAT at
// kv/gitlab/semantic-release/gl-token; regressing to it would silently
// reintroduce the protected-branch-push 403 class. Functional references
// (plugin entries, vault env_var assignments, vault path values) must NOT
// appear; documentation comments naming the old plugin are allowed.
func TestRenderReleasePipelineUsesExecPluginNotGitlabPlugin(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:          "function-autoscaler",
				Path:        "src/control-plane-services/function-autoscaler",
				ChangePaths: []string{"src/control-plane-services/function-autoscaler/**/*"},
				Release: &releaseConfig{
					ServiceName: "nvcf-function-autoscaler",
					ImagePushTargets: []releaseImagePushTarget{
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

	// Functional references to the legacy flow must be gone. Comments that
	// only mention the old plugin name are allowed (they explain the
	// historical context); the check looks for the JSON-quoted plugin
	// entries and vault assignments that would re-wire it back.
	mustNotContain := []string{
		`"@semantic-release/gitlab"`,
		`"env_var": "GL_TOKEN"`,
		`"path": "kv/gitlab/semantic-release/gl-token"`,
	}
	for _, needle := range mustNotContain {
		if strings.Contains(rendered, needle) {
			t.Errorf("rendered release pipeline still contains legacy reference %q\n---\n%s\n---", needle, rendered)
		}
	}

	// Required new pattern markers: @semantic-release/exec plugin entry,
	// release-cli usage, all four embedded scripts, and the preserved
	// Lodash substitution tokens in the exec command strings.
	mustContain := []string{
		`"@semantic-release/exec"`,
		`release-cli create-from-file`,
		`release-verify.sh`,
		`release-prepare.sh`,
		`release-publish.sh`,
		`release-fail.sh`,
		`\${nextRelease.gitTag}`,
		`\${nextRelease.version}`,
		`CI_JOB_TOKEN`,
	}
	for _, needle := range mustContain {
		if !strings.Contains(rendered, needle) {
			t.Errorf("rendered release pipeline missing required pivot marker %q\n---\n%s\n---", needle, rendered)
		}
	}
}

func TestRenderGitHubReleaseMetadataIsPublicSafe(t *testing.T) {
	cfg := configFile{
		Version:               1,
		DefaultTags:           []string{"eks", "prod"},
		ReleaseGeneratedPaths: []string{"MODULE.bazel.lock", ".oss-allowlist"},
		Subprojects: []subproject{
			{
				ID:   "grpc-proxy",
				Path: "src/invocation-plane-services/grpc-proxy",
				Release: &releaseConfig{
					ServiceName: "nvcf-grpc-proxy",
					ImagePushTargets: []releaseImagePushTarget{
						{
							Name:        "ncp_dev",
							BazelTarget: "//nvidia-internal:image_push_ncp_dev",
							Auth: releaseImagePushAuth{
								Type:  "ci_var",
								CIVar: "NGC_DEVOPS_API_KEY",
							},
							DockerAuthPath: "nvcr.io/0651155215864979/ncp-dev/nvcf-grpc-proxy",
						},
					},
					SlackChannel: "C08S6KLCEJH",
				},
			},
			{
				ID:   "nvcf-cli",
				Path: ".",
				Release: &releaseConfig{
					ServiceName:     "nvcf-cli",
					LegacyTagPrefix: "nvcf-cli-v",
					ArchiveRelease: &archiveReleaseConfig{
						Subtree: "src/clis/nvcf-cli",
					},
				},
			},
		},
	}

	rendered, err := renderGitHubReleaseMetadata(cfg, "../ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderGitHubReleaseMetadata: %v", err)
	}

	var metadata githubReleaseMetadata
	if err := json.Unmarshal(rendered, &metadata); err != nil {
		t.Fatalf("metadata json should be valid: %v\n%s", err, rendered)
	}
	if len(metadata.Services) != 2 {
		t.Fatalf("services len = %d, want 2", len(metadata.Services))
	}
	if metadata.Services[0].Path != "src/invocation-plane-services/grpc-proxy" {
		t.Fatalf("unexpected release path: %+v", metadata.Services[0])
	}
	if metadata.Services[0].TagFormat != "" {
		t.Fatalf("default tag format should be omitted from metadata: %+v", metadata.Services[0])
	}
	if metadata.Services[1].Path != "src/clis/nvcf-cli" {
		t.Fatalf("archive release subtree override was not applied: %+v", metadata.Services[1])
	}
	if metadata.Services[1].TagFormat != "" {
		t.Fatalf("archive release default tag format should be omitted from metadata: %+v", metadata.Services[1])
	}
	if metadata.Services[1].LegacyTagPrefix != "nvcf-cli-v" {
		t.Fatalf("archive release should include legacy tag prefix: %+v", metadata.Services[1])
	}
	if strings.Contains(string(rendered), `"tag_format"`) {
		t.Fatalf("metadata should not repeat default tag formats:\n%s", rendered)
	}

	for _, forbidden := range []string{
		"NGC_DEVOPS_API_KEY",
		"nvcr.io",
		"nvidia-internal",
		"C08S6KLCEJH",
		"nvcf-internal",
		"Vault",
	} {
		if strings.Contains(string(rendered), forbidden) {
			t.Fatalf("metadata contains internal-only value %q\n%s", forbidden, rendered)
		}
	}
}

func TestRenderGitHubReleaseMetadataEmitsExplicitTagFormatOverride(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:   "custom-release",
				Path: "src/services/custom-release",
				Release: &releaseConfig{
					ServiceName: "custom-release",
					TagFormat:   "release-overrides/custom-release/v${version}",
				},
			},
		},
	}

	rendered, err := renderGitHubReleaseMetadata(cfg, "../ci/subproject-validations.yaml")
	if err != nil {
		t.Fatalf("renderGitHubReleaseMetadata: %v", err)
	}

	var metadata githubReleaseMetadata
	if err := json.Unmarshal(rendered, &metadata); err != nil {
		t.Fatalf("metadata json should be valid: %v\n%s", err, rendered)
	}
	if len(metadata.Services) != 1 {
		t.Fatalf("services len = %d, want 1", len(metadata.Services))
	}
	if metadata.Services[0].TagFormat != "release-overrides/custom-release/v${version}" {
		t.Fatalf("explicit tag format override was not emitted: %+v", metadata.Services[0])
	}
}

func TestSubprojectMustHaveProfileBazelOrRelease(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks"},
		Profiles:    map[string]profile{},
		Subprojects: []subproject{
			{ID: "svc", Path: "p"},
		},
	}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "must set profile, bazel, release, checks, or helm_validate") {
		t.Fatalf("expected profile-bazel-release-checks-or-helm-validate error, got: %v", err)
	}
}

func TestGeneratedPathsRegex(t *testing.T) {
	// Unset -> default set, regex-escaped and anchored to a basename.
	got := generatedPathsRegex(nil)
	want := `(^|/)(MODULE\.bazel\.lock|\.oss-allowlist)$`
	if got != want {
		t.Fatalf("default regex = %q, want %q", got, want)
	}
	// Custom list escapes metacharacters and preserves order.
	got = generatedPathsRegex([]string{"Cargo.lock", "foo.pb.go"})
	want = `(^|/)(Cargo\.lock|foo\.pb\.go)$`
	if got != want {
		t.Fatalf("custom regex = %q, want %q", got, want)
	}
}

func TestRenderReleasePipelineEmitsMrPushTargets(t *testing.T) {
	cfg := configFile{
		Version:     1,
		DefaultTags: []string{"eks", "prod"},
		Subprojects: []subproject{
			{
				ID:          "function-autoscaler",
				Path:        "src/control-plane-services/function-autoscaler",
				ChangePaths: []string{"src/control-plane-services/function-autoscaler/**/*"},
				Release: &releaseConfig{
					ServiceName: "nvcf-function-autoscaler",
					Staging: &releaseStagingConfig{
						Images: []releaseStagingImage{
							{
								Name:             "nvcf-function-autoscaler",
								BazelImageTarget: "//crates/server:image_index",
								MrPushTargets: []releaseStagingMrPushTarget{
									{
										Repository: "nvcr.io/ema5hzr4ziav/nvcf_autoscaling",
										Auth:       releaseImagePushAuth{Type: "vault", VaultKey: "rs-autoscaler"},
									},
								},
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

	for _, want := range []string{
		// vault fetch for the extra registry token on the mr-manual job
		`"path": "kv/gitlab/ngc-registry-auth/rs-autoscaler"`,
		`"env_var": "RS_AUTOSCALER_TOKEN"`,
		// path-scoped auth merge, never a bare-host nvcr.io login
		`jq --arg r "nvcr.io/ema5hzr4ziav/nvcf_autoscaling"`,
		// push target appended via printf so BUILD.bazel stays column-0 Starlark
		`//ci-release:push_mr_ema5hzr4ziav_nvcf_autoscaling`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("release pipeline missing %q", want)
		}
	}

	// The mr-push block must not host-wide login to nvcr.io: that shadows the
	// anonymous distroless base pull (NVCF-10337).
	if strings.Contains(rendered, "crane auth login nvcr.io") {
		t.Errorf("mr push target uses a host-wide nvcr.io login")
	}
}
