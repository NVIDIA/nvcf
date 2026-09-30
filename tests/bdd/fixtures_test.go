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

package bdd_tmp

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSelfManagedOpenBaoWebhookDefaultsToIgnore(t *testing.T) {
	const baseConfigPath = "../../deploy/stacks/self-managed/environments/base.yaml"

	var config struct {
		OpenBao struct {
			Injector struct {
				Webhook map[string]any `yaml:"webhook"`
			} `yaml:"injector"`
		} `yaml:"openbao"`
	}

	baseConfig, err := os.ReadFile(baseConfigPath)
	if err != nil {
		t.Fatalf("read self-managed base config: %v", err)
	}
	if err := yaml.Unmarshal(baseConfig, &config); err != nil {
		t.Fatalf("parse self-managed base config: %v", err)
	}

	webhook := config.OpenBao.Injector.Webhook
	if got, want := webhook["failurePolicy"], "Ignore"; got != want {
		t.Fatalf("openbao injector failurePolicy = %q, want %q", got, want)
	}
	if selector, exists := webhook["namespaceSelector"]; exists {
		t.Fatalf("openbao injector namespaceSelector = %#v, want it omitted", selector)
	}
}

func TestEKSFeaturesReadAuthoritativeEnvoyGatewayVersion(t *testing.T) {
	command := exec.Command("bash", "scripts/read-envoy-gateway-version.sh")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("read authoritative Envoy Gateway version: %v: %s", err, output)
	}
	version := strings.TrimSpace(string(output))
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$`).MatchString(version) {
		t.Fatalf("authoritative Envoy Gateway version %q is invalid", version)
	}

	for _, featurePath := range []string{
		"features/single-cluster-eks-helmfile.feature",
		"features/multi-cluster-eks-helmfile.feature",
	} {
		featureBytes, readErr := os.ReadFile(featurePath)
		if readErr != nil {
			t.Fatalf("read %s: %v", featurePath, readErr)
		}
		feature := string(featureBytes)
		if regexp.MustCompile(`--version v[0-9]+\.[0-9]+\.[0-9]+`).MatchString(feature) {
			t.Fatalf("%s duplicates a released Envoy Gateway version", featurePath)
		}
		for _, required := range []string{
			"tests/bdd/scripts/read-envoy-gateway-version.sh",
			"--version ${ENVOY_GATEWAY_VERSION}",
		} {
			if !strings.Contains(feature, required) {
				t.Fatalf("%s missing authoritative version wiring %q", featurePath, required)
			}
		}
	}
}

func TestSelfManagedOpenBaoUIAppendRequiresCompatibleNamespaceExpression(t *testing.T) {
	const templatePath = "../../deploy/stacks/self-managed/global.yaml.gotmpl"

	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read self-managed values template: %v", err)
	}
	templateBody := string(templateBytes)

	for _, want := range []string{
		`range $expr := $matchExpressions`,
		`eq (dig "key" "" $expr) "kubernetes.io/metadata.name"`,
		`eq (dig "operator" "" $expr) "In"`,
		`not (has "nvcf-ui" $values)`,
	} {
		if !strings.Contains(templateBody, want) {
			t.Errorf("self-managed values template missing OpenBao selector guard %q", want)
		}
	}
	if strings.Contains(templateBody, `index $matchExpressions 0`) {
		t.Error("self-managed values template assumes the first OpenBao selector expression accepts namespace values")
	}
}

// TestNVCFCLINonlocalFixtureMatchesCLITemplate asserts every top-level
// key in tests/bdd/fixtures/nvcf-cli-nonlocal.yaml.template is also
// declared (active or commented documentation) in the canonical CLI
// template at src/clis/nvcf-cli/.nvcf-cli.yaml.template.
//
// The BDD fixture is intentionally a trimmed subset of the CLI
// template (chart-level constants only, no production URLs, no
// inline docs). If the CLI renames or removes a key the BDD fixture
// references, the runtime CLI config the feature builds at suite
// runtime would silently lose that key. This test catches the
// rename/remove at unit-test time so the wiring is forced to update
// in lockstep.
//
// The CLI template's commented-out documentation blocks (e.g.
// `# api_keys_host: api-keys.nvcf.example.com`) are sufficient to
// pass the assertion. The contract is "the CLI knows about this key",
// not "the CLI defaults it".
func TestNVCFCLINonlocalFixtureMatchesCLITemplate(t *testing.T) {
	const (
		fixturePath     = "fixtures/nvcf-cli-nonlocal.yaml.template"
		cliTemplatePath = "../../src/clis/nvcf-cli/.nvcf-cli.yaml.template"
	)

	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}
	var fixture map[string]any
	if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatalf("parse fixture %s: %v", fixturePath, err)
	}
	if len(fixture) == 0 {
		t.Fatalf("fixture %s has no top-level keys", fixturePath)
	}

	cliTemplateBytes, err := os.ReadFile(cliTemplatePath)
	if err != nil {
		t.Fatalf("read CLI template %s: %v", cliTemplatePath, err)
	}
	cliBody := string(cliTemplateBytes)

	for key := range fixture {
		// Match either an active key (`<key>:`) or a documentation
		// line that mentions the key explicitly (`# api_keys_host:`,
		// `# Config key: api_keys_host`). Anchor on word boundaries
		// so a substring of another key cannot satisfy the match.
		pattern := regexp.MustCompile(`(?m)(^|[^\w])` + regexp.QuoteMeta(key) + `(\s*:|\b)`)
		if !pattern.MatchString(cliBody) {
			t.Errorf("BDD fixture key %q not referenced in CLI template at %s; the CLI may have renamed or removed it", key, cliTemplatePath)
		}
	}
}

func TestNVCFCLILocalFixtureTargetsLocalGRPCGateway(t *testing.T) {
	fixtureBytes, err := os.ReadFile("fixtures/nvcf-cli-local.yaml")
	if err != nil {
		t.Fatalf("read local CLI fixture: %v", err)
	}
	var fixture map[string]any
	if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatalf("parse local CLI fixture: %v", err)
	}
	if got, want := fixture["base_grpc_url"], "grpc.localhost:10081"; got != want {
		t.Fatalf("base_grpc_url = %v, want %q", got, want)
	}
}

func TestComputePlaneLocalBDDFixturesUseDefaultHelmResourceEnforcement(t *testing.T) {
	want := []string{
		"-InfraResourceOverhead",
		"-EnforceContainerFunctionResourceLimits",
		"-EnforceContainerTaskResourceLimits",
	}

	for _, fixturePath := range []string{
		"fixtures/nvcf-compute-plane-local-bdd.yaml",
		"fixtures/nvcf-compute-plane-local-bdd-multi.yaml",
	} {
		t.Run(filepath.Base(fixturePath), func(t *testing.T) {
			fixtureBytes, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatalf("read compute-plane fixture %s: %v", fixturePath, err)
			}
			var fixture struct {
				Global struct {
					NVCAOperator struct {
						SelfManaged struct {
							FeatureGateValues []string `yaml:"featureGateValues"`
						} `yaml:"selfManaged"`
					} `yaml:"nvcaOperator"`
				} `yaml:"global"`
			}
			if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
				t.Fatalf("parse compute-plane fixture %s: %v", fixturePath, err)
			}

			got := fixture.Global.NVCAOperator.SelfManaged.FeatureGateValues
			if slices.Contains(got, "-EnforceHelmFunctionResourceLimits") {
				t.Fatal("local BDD fixture must not disable default Helm function resource enforcement")
			}
			if slices.Contains(got, "-EnforceHelmTaskResourceLimits") {
				t.Fatal("local BDD fixture must not disable default Helm task resource enforcement")
			}
			if !slices.Equal(got, want) {
				t.Fatalf("featureGateValues = %q, want %q", got, want)
			}
		})
	}
}

func TestComputePlaneLocalBDDFixturesDeferNVCAChartDefaults(t *testing.T) {
	for _, fixturePath := range []string{
		"fixtures/nvcf-compute-plane-local-bdd.yaml",
		"fixtures/nvcf-compute-plane-local-bdd-multi.yaml",
	} {
		t.Run(filepath.Base(fixturePath), func(t *testing.T) {
			fixtureBytes, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatalf("read compute-plane fixture %s: %v", fixturePath, err)
			}
			var fixture struct {
				Global struct {
					NodeSelectors map[string]any `yaml:"nodeSelectors"`
				} `yaml:"global"`
				AgentConfig struct {
					MergeConfig string `yaml:"mergeConfig"`
				} `yaml:"agentConfig"`
			}
			if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
				t.Fatalf("parse compute-plane fixture %s: %v", fixturePath, err)
			}
			if len(fixture.Global.NodeSelectors) != 0 {
				t.Fatalf("%s contains inert global.nodeSelectors values", fixturePath)
			}
			if fixture.AgentConfig.MergeConfig != "" {
				t.Fatalf("%s shadows NVCA chart defaults with agentConfig.mergeConfig", fixturePath)
			}
		})
	}
}

func TestNCPRegistrationFixtureDefersNVCAChartAndStackDefaults(t *testing.T) {
	fixtureBytes, err := os.ReadFile("fixtures/ncp-local-register-values.yaml")
	if err != nil {
		t.Fatalf("read registration fixture: %v", err)
	}
	var fixture struct {
		NCAID       string `yaml:"ncaID"`
		Region      string `yaml:"region"`
		SelfManaged struct {
			Region         string `yaml:"region"`
			IdentitySource string `yaml:"identitySource"`
			ICMSServiceURL string `yaml:"icmsServiceURL"`
		} `yaml:"selfManaged"`
	}
	if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatalf("parse registration fixture: %v", err)
	}
	if fixture.NCAID != "" {
		t.Fatalf("registration fixture masks the stack ncaID mapping with %q", fixture.NCAID)
	}
	if fixture.Region != "" || fixture.SelfManaged.Region != "" {
		t.Fatal("registration fixture overrides the chart selfManaged.region default")
	}
	if fixture.SelfManaged.IdentitySource != "" {
		t.Fatal("render-only registration fixture contains CLI lifecycle metadata")
	}
	if fixture.SelfManaged.ICMSServiceURL == "" {
		t.Fatal("registration fixture must retain its topology-specific ICMS endpoint")
	}
}

func TestSelfManagedLocalBDDMultiFixtureWiresComputeReachableWorkerEndpoints(t *testing.T) {
	fixtureBytes, err := os.ReadFile("fixtures/self-managed-local-bdd-multi.yaml")
	if err != nil {
		t.Fatalf("read multi-cluster stack fixture: %v", err)
	}
	fixture := string(fixtureBytes)
	for _, want := range []string{
		"workerConnectBaseURL: http://grpc.nvcf.svc.cluster.local:10086",
		"llmRequestRouterAddress: https://llm-request-router.nvcf.svc.cluster.local:50071",
		"chartPath: ../../../helm/gateway-routes/chart",
		"chartPath: ../../../helm/llm-request-router/llm-request-router",
		"pylonGrpcDialAddress: https://llm-request-router.nvcf.svc.cluster.local:50071",
		"pylonReverseTunnelDialAddress: llm-request-router.nvcf.svc.cluster.local:50072",
		"grpcWorker:",
		"llmWorker:",
		"enabled: true",
	} {
		if !strings.Contains(fixture, want) {
			t.Fatalf("multi-cluster stack fixture missing %q", want)
		}
	}
	if strings.Contains(fixture, "listenerName:") {
		t.Fatal("multi-cluster stack fixture must rely on gateway-routes chart listener defaults")
	}
	var values struct {
		Addons struct {
			LLM struct {
				RequestRouter struct {
					GRPCTLS map[string]any `yaml:"grpcTls"`
				} `yaml:"requestRouter"`
			} `yaml:"llm"`
		} `yaml:"addons"`
		Ingress struct {
			GatewayAPI map[string]any `yaml:"gatewayApi"`
		} `yaml:"ingress"`
	}
	if err := yaml.Unmarshal(fixtureBytes, &values); err != nil {
		t.Fatalf("parse multi-cluster stack fixture: %v", err)
	}
	if _, configured := values.Addons.LLM.RequestRouter.GRPCTLS["dnsNames"]; configured {
		t.Fatal("multi-cluster stack fixture must derive the gRPC TLS DNS name from the advertised endpoint")
	}
	if _, configured := values.Ingress.GatewayAPI["enabled"]; configured {
		t.Fatal("multi-cluster stack fixture must rely on the base environment to enable Gateway API")
	}
}

func TestSelfManagedLocalBDDMultiFixtureUsesStackDefaultForNVCFGRPC(t *testing.T) {
	const fixturePath = "fixtures/self-managed-local-bdd-multi.yaml"

	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read multi-cluster stack fixture: %v", err)
	}
	var fixture map[string]any
	if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatalf("parse multi-cluster stack fixture: %v", err)
	}
	fixtureYAML := string(fixtureBytes)
	if strings.Contains(fixtureYAML, "grpcInsecure") {
		t.Fatal("multi-cluster stack fixture must exercise the stack default for NVCF API gRPC")
	}
}

func TestLocalBDDFixturesLeaveSharedDefaultsToTheirStacks(t *testing.T) {
	tests := []struct {
		name        string
		fixturePath string
		basePath    string
		selfManaged bool
		single      bool
	}{
		{
			name:        "self-managed-single",
			fixturePath: "fixtures/self-managed-local-bdd.yaml",
			basePath:    "../../deploy/stacks/self-managed/environments/base.yaml",
			selfManaged: true,
			single:      true,
		},
		{
			name:        "self-managed-multi",
			fixturePath: "fixtures/self-managed-local-bdd-multi.yaml",
			basePath:    "../../deploy/stacks/self-managed/environments/base.yaml",
			selfManaged: true,
		},
		{
			name:        "compute-plane-single",
			fixturePath: "fixtures/nvcf-compute-plane-local-bdd.yaml",
			basePath:    "../../deploy/stacks/nvcf-compute-plane/environments/base.yaml",
		},
		{
			name:        "compute-plane-multi",
			fixturePath: "fixtures/nvcf-compute-plane-local-bdd-multi.yaml",
			basePath:    "../../deploy/stacks/nvcf-compute-plane/environments/base.yaml",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := loadYAMLMap(t, test.fixturePath)
			base := loadYAMLMap(t, test.basePath)

			for _, path := range [][]string{
				{"global", "helm", "sources", "registry"},
				{"global", "image", "registry"},
			} {
				if value, exists := nestedYAMLValue(fixture, path...); exists {
					t.Errorf("fixture owns %s = %v; want stack default", strings.Join(path, "."), value)
				}
				if value, exists := nestedYAMLValue(base, path...); !exists || value != "nvcr.io" {
					t.Errorf("base %s = %v (exists %t), want nvcr.io", strings.Join(path, "."), value, exists)
				}
			}
			for _, path := range [][]string{
				{"global", "helm", "sources", "repository"},
				{"global", "image", "repository"},
			} {
				if value, exists := nestedYAMLValue(fixture, path...); !exists || value != "REPLACE_WITH_SAMPLE_NGC_ORG/REPLACE_WITH_SAMPLE_NGC_TEAM" {
					t.Errorf("fixture %s = %v (exists %t), want local substitution placeholder", strings.Join(path, "."), value, exists)
				}
			}

			if value, exists := nestedYAMLValue(fixture, "observability", "profile"); !exists || value != "disabled" {
				t.Errorf("fixture observability.profile = %v (exists %t), want disabled", value, exists)
			}

			if test.selfManaged {
				if value, exists := nestedYAMLValue(fixture, "global", "nodeSelectors", "enabled"); exists {
					t.Errorf("fixture owns global.nodeSelectors.enabled = %v; want stack default", value)
				}
				if value, exists := nestedYAMLValue(base, "global", "nodeSelectors", "enabled"); !exists || value != false {
					t.Errorf("base global.nodeSelectors.enabled = %v (exists %t), want false", value, exists)
				}
			}

			if test.single {
				for _, path := range [][]string{
					{"ingress", "gatewayApi", "enabled"},
					{"ingress", "gatewayApi", "gateways", "nats", "listenerName"},
				} {
					if value, exists := nestedYAMLValue(fixture, path...); exists {
						t.Errorf("fixture owns %s = %v; want stack default", strings.Join(path, "."), value)
					}
				}
				if value, exists := nestedYAMLValue(base, "ingress", "gatewayApi", "enabled"); !exists || value != true {
					t.Errorf("base ingress.gatewayApi.enabled = %v (exists %t), want true", value, exists)
				}
				if value, exists := nestedYAMLValue(base, "ingress", "gatewayApi", "gateways", "nats", "listenerName"); exists {
					t.Errorf("base owns ingress.gatewayApi.gateways.nats.listenerName = %v; want chart default", value)
				}
			}
		})
	}
}

func TestSelfManagedLocalBDDFixturesDeferRequestRouterDefaults(t *testing.T) {
	for _, fixturePath := range []string{
		"fixtures/self-managed-local-bdd.yaml",
		"fixtures/self-managed-local-bdd-multi.yaml",
	} {
		t.Run(filepath.Base(fixturePath), func(t *testing.T) {
			fixtureBytes, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatalf("read self-managed fixture %s: %v", fixturePath, err)
			}
			var fixture struct {
				Global struct {
					WorkerEndpoints map[string]any `yaml:"workerEndpoints"`
				} `yaml:"global"`
				Addons struct {
					LLM struct {
						RequestRouter struct {
							ReplicaCount *int           `yaml:"replicaCount"`
							Workload     map[string]any `yaml:"workload"`
							LoadBalancer map[string]any `yaml:"loadBalancer"`
							GRPCTLS      struct {
								Mode       string `yaml:"mode"`
								SecretName string `yaml:"secretName"`
							} `yaml:"grpcTls"`
						} `yaml:"requestRouter"`
						PKI map[string]any `yaml:"pki"`
					} `yaml:"llm"`
				} `yaml:"addons"`
			}
			if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
				t.Fatalf("parse self-managed fixture %s: %v", fixturePath, err)
			}

			router := fixture.Addons.LLM.RequestRouter
			if strings.HasSuffix(fixturePath, "-multi.yaml") && router.ReplicaCount != nil {
				t.Fatalf("%s overrides chart replicaCount with %d", fixturePath, *router.ReplicaCount)
			}
			if len(router.Workload) != 0 {
				t.Fatalf("%s overrides the chart workload defaults: %#v", fixturePath, router.Workload)
			}
			if len(router.LoadBalancer) != 0 {
				t.Fatalf("%s narrows Stargate's built-in load-balancer defaults: %#v", fixturePath, router.LoadBalancer)
			}
			if router.GRPCTLS.Mode != "" || router.GRPCTLS.SecretName != "" {
				t.Fatalf("%s repeats stack gRPC TLS defaults", fixturePath)
			}
			if len(fixture.Addons.LLM.PKI) != 0 {
				t.Fatalf("%s repeats the stack's managed PKI defaults: %#v", fixturePath, fixture.Addons.LLM.PKI)
			}
			for _, key := range []string{"essServiceURL", "invocationServiceURL"} {
				if _, exists := fixture.Global.WorkerEndpoints[key]; exists {
					t.Fatalf("%s repeats the stack default for global.workerEndpoints.%s", fixturePath, key)
				}
			}
		})
	}
}

func TestLocalBDDFeaturesDoNotRepeatDisabledObservabilityProfile(t *testing.T) {
	featurePaths, err := filepath.Glob("features/*.feature")
	if err != nil {
		t.Fatalf("list BDD feature files: %v", err)
	}
	duplicate := regexp.MustCompile(`(?m)^\s*\|\s*observability\.profile\s*\|\s*disabled\s*\|`)
	for _, featurePath := range featurePaths {
		feature, err := os.ReadFile(featurePath)
		if err != nil {
			t.Fatalf("read feature %s: %v", featurePath, err)
		}
		usesLocalFixture := false
		for _, fixtureName := range []string{
			"self-managed-local-bdd.yaml",
			"self-managed-local-bdd-multi.yaml",
			"nvcf-compute-plane-local-bdd.yaml",
			"nvcf-compute-plane-local-bdd-multi.yaml",
		} {
			if bytes.Contains(feature, []byte(fixtureName)) {
				usesLocalFixture = true
				break
			}
		}
		if usesLocalFixture && duplicate.Match(feature) {
			t.Errorf("%s repeats observability.profile=disabled already owned by its local BDD fixture", featurePath)
		}
	}
}

func loadYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read YAML %s: %v", path, err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(body, &values); err != nil {
		t.Fatalf("parse YAML %s: %v", path, err)
	}
	return values
}

func nestedYAMLValue(values map[string]any, path ...string) (any, bool) {
	var current any = values
	for _, key := range path {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = mapping[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func TestSelfManagedLocalBDDFixtureUsesStackOwnedLLMAPIGatewayDefaults(t *testing.T) {
	const fixturePath = "fixtures/self-managed-local-bdd.yaml"

	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read single-cluster stack fixture: %v", err)
	}
	var values struct {
		Addons struct {
			LLM struct {
				Gateway struct {
					ReplicaCount int            `yaml:"replicaCount"`
					Auth         map[string]any `yaml:"auth"`
					Metrics      map[string]any `yaml:"metrics"`
				} `yaml:"gateway"`
			} `yaml:"llm"`
		} `yaml:"addons"`
	}
	if err := yaml.Unmarshal(fixtureBytes, &values); err != nil {
		t.Fatalf("parse single-cluster stack fixture: %v", err)
	}
	if values.Addons.LLM.Gateway.ReplicaCount != 1 {
		t.Fatalf("local schedulability override replicaCount = %d; want 1", values.Addons.LLM.Gateway.ReplicaCount)
	}
	if _, configured := values.Addons.LLM.Gateway.Auth["grpcInsecure"]; configured {
		t.Fatal("single-cluster fixture must rely on the self-managed gRPC transport default")
	}
	if _, configured := values.Addons.LLM.Gateway.Metrics["serviceMonitor"]; configured {
		t.Fatal("single-cluster fixture must derive ServiceMonitor enablement from the observability profile")
	}
}

func TestNVCTTaskSmokeSupportsContainerAndHelmSamples(t *testing.T) {
	for _, path := range []string{
		"../../examples/task-samples/task-simple-sample/Dockerfile",
		"../../examples/task-samples/task-simple-sample/main.py",
		"../../examples/task-samples/task-simple-sample/requirements.txt",
		"../../examples/task-samples/task-helmchart-sample/task-helmchart-test/Chart.yaml",
		"../../examples/task-samples/task-helmchart-sample/task-helmchart-test/templates/job.yaml",
		"../../examples/task-samples/task-helmchart-sample/task-helmchart-test/values.yaml",
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("task-simple-sample fixture missing %s: %v", path, err)
		}
	}

	scriptBytes, err := os.ReadFile("scripts/run-nvct-task-smoke.sh")
	if err != nil {
		t.Fatalf("read NVCT task smoke script: %v", err)
	}
	script := string(scriptBytes)
	for _, want := range []string{
		"task-simple-sample",
		"NVCT_BDD_TASK_IMAGE_TAG:-local",
		"NVCT_BDD_TASK_MODE",
		"NVCT_BDD_TASK_HELM_CHART must be set in helm mode",
		"helmChart",
		"containerEnvironment",
		"NUM_OF_RESULTS",
		"DELAY_BETWEEN_RESULTS_IN_MINUTES",
		".token // empty",
		"audience_service_ids",
		"account-tasks",
		"Key-Issuer-Service",
		"NVCT_BDD_STATE_PATH",
		"NVCT_BDD_TASKS_HOST",
		"NVCT_BDD_TASK_INSTANCE_TYPE must be set",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("NVCT task smoke script does not reference %q", want)
		}
	}
	if strings.Contains(script, ".apiKey // empty") {
		t.Fatal("NVCT task smoke script reads the function API key from nvcf-cli state")
	}
	if strings.Contains(script, "task_simple_sample") {
		t.Fatal("NVCT task smoke script uses the unpublished underscore image name")
	}
	if strings.Contains(script, "docker.io/library/busybox") {
		t.Fatal("NVCT task smoke script still uses the synthetic busybox sample")
	}
	if strings.Contains(script, "NVCT_BDD_TASK_INSTANCE_TYPE:-") {
		t.Fatal("NVCT task smoke script has a topology-dependent instance type default")
	}
}

func TestResolveGatewayDomainUsesResolvedIPv4(t *testing.T) {
	cmd := exec.Command("bash", "scripts/resolve-gateway-domain.sh", "gateway.example.invalid")
	cmd.Env = append(os.Environ(), "EKS_GATEWAY_IPV4=192.0.2.10")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve gateway domain: %v\n%s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), "192-0-2-10.nip.io"; got != want {
		t.Fatalf("resolved gateway domain = %q, want %q", got, want)
	}
}

func TestResolveGatewayDomainRetriesTransientDNSFailures(t *testing.T) {
	binDir := t.TempDir()
	countPath := filepath.Join(binDir, "host-count")
	hostPath := filepath.Join(binDir, "host")
	hostScript := `#!/usr/bin/env bash
set -euo pipefail
count=0
if [[ -f "$FAKE_HOST_COUNT" ]]; then
  count="$(<"$FAKE_HOST_COUNT")"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$FAKE_HOST_COUNT"
if [[ "$count" -lt 3 ]]; then
  exit 1
fi
printf '%s has address 192.0.2.10\n' "$1"
`
	if err := os.WriteFile(hostPath, []byte(hostScript), 0o755); err != nil {
		t.Fatalf("write fake host: %v", err)
	}
	sleepPath := filepath.Join(binDir, "sleep")
	if err := os.WriteFile(sleepPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake sleep: %v", err)
	}

	cmd := exec.Command("bash", "scripts/resolve-gateway-domain.sh", "gateway.example.invalid")
	cmd.Env = append(os.Environ(), "FAKE_HOST_COUNT="+countPath, "PATH="+binDir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve gateway domain after transient DNS failures: %v\n%s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), "192-0-2-10.nip.io"; got != want {
		t.Fatalf("resolved gateway domain = %q, want %q", got, want)
	}
	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatalf("read host attempt count: %v", err)
	}
	if got, want := strings.TrimSpace(string(count)), "3"; got != want {
		t.Fatalf("host attempts = %s, want %s", got, want)
	}
}

func TestWaitForDNSRequiresStableSystemResolution(t *testing.T) {
	binDir := t.TempDir()
	resolverScript := `#!/usr/bin/env bash
set -euo pipefail
count=0
if [[ -f "$FAKE_RESOLVER_COUNT" ]]; then
  count="$(<"$FAKE_RESOLVER_COUNT")"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$FAKE_RESOLVER_COUNT"
if [[ "$count" -eq 2 ]]; then
  exit 1
fi
`
	for _, name := range []string{"host", "python3"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(resolverScript), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(binDir, "sleep"),
		[]byte("#!/usr/bin/env bash\nexit 0\n"),
		0o755,
	); err != nil {
		t.Fatalf("write fake sleep: %v", err)
	}

	tests := []struct {
		name    string
		timeout string
	}{
		{name: "normal timeout", timeout: "30"},
		{name: "maximum int64 timeout", timeout: "9223372036854775807"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			countPath := filepath.Join(t.TempDir(), "resolver-count")
			cmd := exec.Command("bash", "scripts/wait-for-dns.sh", "gateway.example.invalid", tc.timeout)
			cmd.Env = append(os.Environ(), "FAKE_RESOLVER_COUNT="+countPath, "PATH="+binDir+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("wait for stable DNS: %v\n%s", err, out)
			}
			if got := string(out); !strings.Contains(got, "3 consecutive system-resolver checks after 5 attempts") {
				t.Fatalf("wait output did not report stable resolution: %q", got)
			}
			count, err := os.ReadFile(countPath)
			if err != nil {
				t.Fatalf("read resolver attempt count: %v", err)
			}
			if got, want := strings.TrimSpace(string(count)), "5"; got != want {
				t.Fatalf("resolver attempts = %s, want %s", got, want)
			}
		})
	}
}

func TestNVCFGatewayFixtureDefinesReferencedGatewayClass(t *testing.T) {
	const fixturePath = "fixtures/nvcf-gateway.yaml"

	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(fixtureBytes))
	gatewayClasses := map[string]bool{}
	var gatewayClassName string
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("parse fixture %s: %v", fixturePath, err)
		}
		if len(doc) == 0 {
			continue
		}

		kind, _ := doc["kind"].(string)
		metadata, _ := doc["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		switch kind {
		case "GatewayClass":
			gatewayClasses[name] = true
		case "Gateway":
			if name != "nvcf-gateway" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			gatewayClassName, _ = spec["gatewayClassName"].(string)
		}
	}

	if gatewayClassName == "" {
		t.Fatalf("fixture %s does not define gateway/nvcf-gateway with spec.gatewayClassName", fixturePath)
	}
	if !gatewayClasses[gatewayClassName] {
		t.Fatalf("fixture %s references GatewayClass %q but does not define it", fixturePath, gatewayClassName)
	}
}
