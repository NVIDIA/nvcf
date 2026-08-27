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

	"nvcf-bdd/dsl"
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

func TestComputePlaneLocalBDDFixturesDisableResourceSizingFeatureGates(t *testing.T) {
	want := []string{
		"-InfraResourceOverhead",
		"-EnforceHelmFunctionResourceLimits",
		"-EnforceContainerFunctionResourceLimits",
		"-EnforceHelmTaskResourceLimits",
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
			if !slices.Equal(got, want) {
				t.Fatalf("featureGateValues = %q, want %q", got, want)
			}
		})
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
		"llmRequestRouterAddress: llm-request-router.nvcf.svc.cluster.local:50071",
		"chartPath: ../../../helm/gateway-routes/chart",
		"chartPath: ../../../helm/llm-request-router/llm-request-router",
		"pylonGrpcDialAddress: llm-request-router.nvcf.svc.cluster.local:50071",
		"pylonReverseTunnelDialAddress: llm-request-router.nvcf.svc.cluster.local:50072",
		"*.llm-request-router-headless.nvcf.svc.cluster.local",
		"grpcWorker:",
		"llmWorker:",
		"enabled: true",
		"listenerName: worker-tcp",
		"listenerName: llm-grpc",
		"listenerName: llm-quic",
	} {
		if !strings.Contains(fixture, want) {
			t.Fatalf("multi-cluster stack fixture missing %q", want)
		}
	}
}

func TestSelfManagedLocalBDDMultiFixtureUsesPlaintextNVCFGRPC(t *testing.T) {
	const fixturePath = "fixtures/self-managed-local-bdd-multi.yaml"

	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read multi-cluster stack fixture: %v", err)
	}
	var fixture struct {
		Addons struct {
			LLM struct {
				Gateway struct {
					Auth struct {
						GRPCInsecure bool `yaml:"grpcInsecure"`
					} `yaml:"auth"`
				} `yaml:"gateway"`
			} `yaml:"llm"`
		} `yaml:"addons"`
	}
	if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatalf("parse multi-cluster stack fixture: %v", err)
	}
	if !fixture.Addons.LLM.Gateway.Auth.GRPCInsecure {
		t.Fatal("multi-cluster stack fixture must use plaintext NVCF API gRPC")
	}
}

func TestNVCTTaskSmokeUsesTaskSimpleSample(t *testing.T) {
	for _, path := range []string{
		"../../examples/task-samples/task-simple-sample/Dockerfile",
		"../../examples/task-samples/task-simple-sample/main.py",
		"../../examples/task-samples/task-simple-sample/requirements.txt",
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

func TestWriteTransportTrustEnvPreservesExistingAgentConfig(t *testing.T) {
	const (
		certificatePEM = `-----BEGIN CERTIFICATE-----
MIIEMjCCAxqgAwIBAgIUIZMDB4PqZ1PUCS8TIX7uqZXSDZ4wDQYJKoZIhvcNAQEL
BQAwgZAxCzAJBgNVBAYTAlVTMRIwEAYDVQQIDAlUZXN0U3RhdGUxETAPBgNVBAcM
CFRlc3RDaXR5MTQwMgYDVQQKDCtURVNUIENFUlRJRklDQVRFIC0gRE8gTk9UIFVT
RSBJTiBQUk9EVUNUSU9OMRAwDgYDVQQLDAdUZXN0aW5nMRIwEAYDVQQDDAlsb2Nh
bGhvc3QwHhcNMjYwMzA5MTMwMDM1WhcNMzYwMzA2MTMwMDM1WjCBkDELMAkGA1UE
BhMCVVMxEjAQBgNVBAgMCVRlc3RTdGF0ZTERMA8GA1UEBwwIVGVzdENpdHkxNDAy
BgNVBAoMK1RFU1QgQ0VSVElGSUNBVEUgLSBETyBOT1QgVVNFIElOIFBST0RVQ1RJ
T04xEDAOBgNVBAsMB1Rlc3RpbmcxEjAQBgNVBAMMCWxvY2FsaG9zdDCCASIwDQYJ
KoZIhvcNAQEBBQADggEPADCCAQoCggEBAJk2OOKYkbEwWWTcfk+ovgS5gMpXQq3U
83ySD3tFczbT6bjXsBzJWyLa1s5/+IRbYTR0N2spmeNc4yrWRAUIzmRVIMADGHI5
LlN8emM8PzskrYK+MPh3kqZlNCVbjcdXoanFbPz8PJrbE4ohOcFj/Sb5dixjAvu+
26wDFdm06pw2cggumbpyKuKgFEFR1UHucUPfCv+X8TkKqRaDDIAf3FBQThBzJYzJ
XJJ3RxAAQzz6XJsqHit4Oc1tu3Rv6rKI7h6c2jYYToZHl1IhenQZX6k/rQnHg2Li
p2kOnIemtA9ccLYT7Eek3I5i1wjviujTbuKHA0qFOAkMIH5QZGdAh3kCAwEAAaOB
gTB/MB0GA1UdDgQWBBRmkXwkBK2g5adgUXQbtGf5Y9Vo5jAfBgNVHSMEGDAWgBRm
kXwkBK2g5adgUXQbtGf5Y9Vo5jAPBgNVHRMBAf8EBTADAQH/MCwGA1UdEQQlMCOC
CWxvY2FsaG9zdIcEfwAAAYcQAAAAAAAAAAAAAAAAAAAAATANBgkqhkiG9w0BAQsF
AAOCAQEAOxTBY4FzafF0QlfKyeRDgcJS+xSVUMBHW5Lnc/mWwtHEUvuhFSvGqEim
irnaoqW9ijUs70ful+VIUfucTLhQSxIEBiD6Wewxxe91I3sClW4hbrcVL7kzxLK5
N1UmMOeGIpB+DY/sy6HNF3vNEqqoHU6sZQasZGE/HbFPG8xvwwZlxk0hvZNTAQ9G
C3thGnRlMMjPOsnxnKeledidtW+NZXrKbcikgbBp/kQhwkqlHxpPkKdrUAn88YkP
+/zy1JTpb0w+5JzVTpRlf/B4BU8f/GWrnszCXIm+sOzn5JwDlqPDu61YsmA3kE9S
0rTPj/1o1rFdK8CuxpWA6lqM31zqQg==
-----END CERTIFICATE-----
`
		wantFingerprint = "sha256:01b7ff2c19b3d8475e2228874aed3da996c66e8c0b32812998c39c68e0c59afb"
	)

	testDir := t.TempDir()
	binDir := filepath.Join(testDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("create fake binary directory: %v", err)
	}
	envPath := filepath.Join(testDir, "compute.yaml")
	envYAML := `global:
  image:
    repository: example
agentConfig:
  mergeConfig: |
    agent:
      logLevel: debug
    cluster:
      validationPolicy:
        name: PreserveThisPolicy
    workload:
      stargateQUICInsecure: true
`
	if err := os.WriteFile(envPath, []byte(envYAML), 0o600); err != nil {
		t.Fatalf("write compute environment: %v", err)
	}
	// Feature setup updates unrelated dotted keys through the repository YAML
	// editor, which reserializes the whole document before this script runs.
	if err := dsl.UpdateYAMLKeys(envPath, [][2]string{{"global.image.repository", "feature-updated"}}); err != nil {
		t.Fatalf("apply feature-style compute environment update: %v", err)
	}
	certificatePath := filepath.Join(testDir, "ca.pem")
	if err := os.WriteFile(certificatePath, []byte(certificatePEM), 0o600); err != nil {
		t.Fatalf("write test certificate: %v", err)
	}

	kubectlScript := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "get" && "$2" == "secret" ]]; then
  echo "root token must not be read for the public CA endpoint" >&2
  exit 91
elif [[ "$1" == "port-forward" ]]; then
  if [[ " $* " != *" --context k3d-test "* ]] || [[ " $* " != *" :8200 "* ]]; then
    exit 2
  fi
  printf 'Forwarding from 127.0.0.1:43123 -> 8200\n'
  exec /bin/sleep 300
else
  exit 2
fi
`
	curlScript := `#!/usr/bin/env bash
set -euo pipefail
if [[ " $* " == *" --config "* ]] || [[ " $* " == *"test-token"* ]]; then
  exit 2
fi
if [[ " $* " != *" --connect-timeout "* ]] || [[ " $* " != *" --max-time "* ]]; then
  exit 2
fi
if [[ " $* " != *" http://127.0.0.1:43123/"* ]]; then
  exit 2
fi
python3 - "$FAKE_CA_FILE" <<'PYEOF'
import json
import pathlib
import sys

print(json.dumps({"data": {"certificate": pathlib.Path(sys.argv[1]).read_text()}}))
PYEOF
`
	for name, body := range map[string]string{"kubectl": kubectlScript, "curl": curlScript} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}

	cmd := exec.Command("bash", "scripts/write-transport-trust-env.sh", envPath, "k3d-test")
	cmd.Env = append(os.Environ(), "FAKE_CA_FILE="+certificatePath, "PATH="+binDir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("write transport trust environment: %v\n%s", err, out)
	}

	updated, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read updated compute environment: %v", err)
	}
	var environment struct {
		AgentConfig struct {
			MergeConfig string `yaml:"mergeConfig"`
		} `yaml:"agentConfig"`
	}
	if err := yaml.Unmarshal(updated, &environment); err != nil {
		t.Fatalf("parse updated compute environment: %v", err)
	}
	var mergeConfig struct {
		Agent struct {
			LogLevel string `yaml:"logLevel"`
		} `yaml:"agent"`
		Cluster struct {
			ValidationPolicy struct {
				Name string `yaml:"name"`
			} `yaml:"validationPolicy"`
		} `yaml:"cluster"`
		Workload struct {
			StargateQUICInsecure bool `yaml:"stargateQUICInsecure"`
			TransportTLS         struct {
				TrustMode              string `yaml:"trustMode"`
				TrustBundleFingerprint string `yaml:"trustBundleFingerprint"`
				TrustBundlePEM         string `yaml:"trustBundlePem"`
			} `yaml:"transportTLS"`
		} `yaml:"workload"`
	}
	if err := yaml.Unmarshal([]byte(environment.AgentConfig.MergeConfig), &mergeConfig); err != nil {
		t.Fatalf("parse merged agent configuration: %v", err)
	}
	if got, want := mergeConfig.Agent.LogLevel, "debug"; got != want {
		t.Fatalf("agent.logLevel = %q, want preserved value %q", got, want)
	}
	if got, want := mergeConfig.Cluster.ValidationPolicy.Name, "PreserveThisPolicy"; got != want {
		t.Fatalf("cluster.validationPolicy.name = %q, want preserved value %q", got, want)
	}
	if mergeConfig.Workload.StargateQUICInsecure {
		t.Fatal("workload.stargateQUICInsecure remained enabled with bundle trust")
	}
	if got, want := mergeConfig.Workload.TransportTLS.TrustMode, "bundle"; got != want {
		t.Fatalf("workload.transportTLS.trustMode = %q, want %q", got, want)
	}
	if got := mergeConfig.Workload.TransportTLS.TrustBundleFingerprint; got != wantFingerprint {
		t.Fatalf("workload.transportTLS.trustBundleFingerprint = %q, want %q", got, wantFingerprint)
	}
	if got := mergeConfig.Workload.TransportTLS.TrustBundlePEM; got != certificatePEM {
		t.Fatalf("workload.transportTLS.trustBundlePem was not preserved exactly")
	}
}

func TestWriteTransportTrustEnvDoesNotSendTokenWhenPortForwardFails(t *testing.T) {
	testDir := t.TempDir()
	binDir := filepath.Join(testDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("create fake binary directory: %v", err)
	}
	envPath := filepath.Join(testDir, "compute.yaml")
	if err := os.WriteFile(envPath, []byte("agentConfig:\n  mergeConfig: |\n    cluster: {}\n"), 0o600); err != nil {
		t.Fatalf("write compute environment: %v", err)
	}
	curlLog := filepath.Join(testDir, "curl.log")
	kubectlScript := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "get" && "$2" == "secret" ]]; then
  echo "root token must not be read for the public CA endpoint" >&2
  exit 91
elif [[ "$1" == "port-forward" ]]; then
  printf 'unable to listen on any requested port\n' >&2
  exit 42
else
  exit 2
fi
`
	curlScript := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_CURL_LOG"
exit 7
`
	sleepScript := `#!/usr/bin/env bash
exit 0
`
	for name, body := range map[string]string{
		"kubectl": kubectlScript,
		"curl":    curlScript,
		"sleep":   sleepScript,
	} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}

	cmd := exec.Command("bash", "scripts/write-transport-trust-env.sh", envPath, "k3d-test")
	cmd.Env = append(os.Environ(), "FAKE_CURL_LOG="+curlLog, "PATH="+binDir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("write transport trust environment unexpectedly succeeded\n%s", out)
	}
	if !strings.Contains(string(out), "port-forward exited before becoming ready") {
		t.Fatalf("failure did not identify the port-forward readiness error:\n%s", out)
	}
	if body, readErr := os.ReadFile(curlLog); readErr == nil && len(body) > 0 {
		t.Fatalf("curl was invoked after port-forward failure: %s", body)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read curl log: %v", readErr)
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
	countPath := filepath.Join(binDir, "resolver-count")
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

	cmd := exec.Command("bash", "scripts/wait-for-dns.sh", "gateway.example.invalid", "30")
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
