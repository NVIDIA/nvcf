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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDestroyNonlocalStackForceDeletesLingeringEnvoyPods(t *testing.T) {
	log := runDestroyNonlocalStack(t, true)

	for _, want := range []string{
		"delete pod/envoy-default-1 --force --grace-period=0 --wait=false",
		"wait --for=delete namespace/envoy-gateway-system --timeout=60s",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("cleanup command log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "/api/v1/namespaces/envoy-gateway-system/finalize") {
		t.Fatalf("cleanup finalized a namespace that terminated after pod deletion:\n%s", log)
	}
}

func TestDestroyNonlocalStackFinalizesEmptyEnvoyNamespace(t *testing.T) {
	log := runDestroyNonlocalStack(t, false)

	if got, want := strings.Count(log, "-n envoy-gateway-system get pods -o name"), 2; got != want {
		t.Fatalf("Envoy pod checks = %d, want %d before finalization:\n%s", got, want, log)
	}
	const finalize = "replace --raw /api/v1/namespaces/envoy-gateway-system/finalize -f -"
	if !strings.Contains(log, finalize) {
		t.Fatalf("cleanup command log missing %q:\n%s", finalize, log)
	}
}

func runDestroyNonlocalStack(t *testing.T, namespaceWaitSucceeds bool) string {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "commands.log")
	podCountPath := filepath.Join(binDir, "pod-get-count")
	waitResult := "fail"
	if namespaceWaitSucceeds {
		waitResult = "success"
	}

	kubectlScript := `#!/usr/bin/env bash
set -euo pipefail
printf 'kubectl %s\n' "$*" >>"$FAKE_COMMAND_LOG"
case "$*" in
  *"delete namespace envoy-gateway-system"*) exit 1 ;;
  *"-n envoy-gateway-system get pods -o name"*)
    count=0
    if [[ -f "$FAKE_POD_GET_COUNT" ]]; then
      count="$(<"$FAKE_POD_GET_COUNT")"
    fi
    count=$((count + 1))
    printf '%s\n' "$count" >"$FAKE_POD_GET_COUNT"
    if [[ "$count" -eq 1 ]]; then
      printf 'pod/envoy-default-1\n'
    fi
    ;;
  *"wait --for=delete namespace/envoy-gateway-system"*)
    [[ "$FAKE_NAMESPACE_WAIT" == "success" ]]
    ;;
  *"get namespace envoy-gateway-system -o json"*)
    printf '{"spec":{"finalizers":["kubernetes"]}}\n'
    ;;
  *"replace --raw /api/v1/namespaces/envoy-gateway-system/finalize"*)
    cat >/dev/null
    ;;
  *"get gateway nvcf-gateway"*|*"get gatewayclass eg"*) exit 1 ;;
esac
`
	helmScript := `#!/usr/bin/env bash
set -euo pipefail
printf 'helm %s\n' "$*" >>"$FAKE_COMMAND_LOG"
case " $* " in
  *" status "*) exit 1 ;;
esac
`
	jqScript := `#!/usr/bin/env bash
set -euo pipefail
cat
`
	for name, body := range map[string]string{
		"kubectl": kubectlScript,
		"helm":    helmScript,
		"jq":      jqScript,
	} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}

	cmd := exec.Command(
		"bash", "scripts/destroy-nonlocal-stack.sh",
		"--control-plane-context", "bdd-cp",
		"--compute-context", "bdd-compute",
	)
	cmd.Env = append(os.Environ(),
		"BDD_REPO_ROOT="+t.TempDir(),
		"FAKE_COMMAND_LOG="+logPath,
		"FAKE_NAMESPACE_WAIT="+waitResult,
		"FAKE_POD_GET_COUNT="+podCountPath,
		"PATH="+binDir+":"+os.Getenv("PATH"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run nonlocal cleanup: %v\n%s", err, output)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read cleanup command log: %v", err)
	}
	return string(log)
}

func TestDestroyStackMultiCleansWorkerNamespacesOnControlPlane(t *testing.T) {
	log := runDestroyStackMulti(t)

	for _, want := range []string{
		"helm --kube-context k3d-ncp-local-cp uninstall nvca-operator -n nvca-operator",
		"kubectl --context k3d-ncp-local-cp delete namespace nvca-operator",
		"kubectl --context k3d-ncp-local-cp delete namespace nvca-system",
		"kubectl --context k3d-ncp-local-cp delete namespace nvcf-backend",
		"helm --kube-context k3d-ncp-local-compute-1 uninstall nvca-operator -n nvca-operator",
		"helm --kube-context k3d-ncp-local-cp uninstall nats -n nats-system",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("cleanup command log missing %q:\n%s", want, log)
		}
	}

	for _, forbidden := range []string{
		"delete namespace envoy-gateway-system",
		"delete namespace cert-manager",
		"uninstall eg ",
		"uninstall cert-manager",
	} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("cleanup touched topology infrastructure %q:\n%s", forbidden, log)
		}
	}
}

func TestDestroyStackCleanOutRemovesHelmfileTreesAndRegistration(t *testing.T) {
	repo := t.TempDir()
	selfOut := filepath.Join(repo, "deploy/stacks/self-managed/out")
	computeOut := filepath.Join(repo, "deploy/stacks/nvcf-compute-plane/out")
	registration := filepath.Join(repo, "deploy/stacks/nvcf-compute-plane/registration")
	helmfileTree := filepath.Join(selfOut, "helmfile.yaml-abc123-nats")
	computeTree := filepath.Join(computeOut, "helmfile.yaml-def456-nvca-operator")

	for _, dir := range []string{helmfileTree, computeTree, registration} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	files := map[string]string{
		filepath.Join(selfOut, "control-plane-profile.yaml"):                    "profile: leftover\n",
		filepath.Join(selfOut, "notes.txt"):                                     "keep me\n",
		filepath.Join(helmfileTree, "nats.yaml"):                                "kind: ConfigMap\n",
		filepath.Join(computeOut, "ncp-local-register-values.yaml"):             "clusterName: leftover\n",
		filepath.Join(computeTree, "nvca-operator.yaml"):                        "kind: Deployment\n",
		filepath.Join(registration, "ncp-local-register-values.yaml"):           "clusterName: leftover\n",
		filepath.Join(registration, "ncp-local-compute-1-register-values.yaml"): "clusterName: leftover-compute\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	binDir := t.TempDir()
	writeFakeBin(t, binDir, "kubectl", `#!/usr/bin/env bash
set -euo pipefail
exit 1
`)
	writeFakeBin(t, binDir, "helm", `#!/usr/bin/env bash
set -euo pipefail
exit 0
`)

	cmd := exec.Command("bash", "scripts/destroy-stack.sh", "single")
	cmd.Env = append(os.Environ(),
		"BDD_REPO_ROOT="+repo,
		"PATH="+binDir+":"+os.Getenv("PATH"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run destroy-stack.sh single: %v\n%s", err, output)
	}

	gone := []string{
		filepath.Join(selfOut, "control-plane-profile.yaml"),
		helmfileTree,
		filepath.Join(computeOut, "ncp-local-register-values.yaml"),
		computeTree,
		filepath.Join(registration, "ncp-local-register-values.yaml"),
		filepath.Join(registration, "ncp-local-compute-1-register-values.yaml"),
	}
	for _, path := range gone {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("generated artifact still present: %s (err=%v)", path, err)
		}
	}

	kept := filepath.Join(selfOut, "notes.txt")
	body, err := os.ReadFile(kept)
	if err != nil {
		t.Fatalf("ad-hoc out/ note was removed: %v", err)
	}
	if got := string(body); got != "keep me\n" {
		t.Fatalf("ad-hoc out/ note contents = %q", got)
	}
}

func runDestroyStackMulti(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "commands.log")

	writeFakeBin(t, binDir, "k3d", `#!/usr/bin/env bash
set -euo pipefail
printf 'k3d %s\n' "$*" >>"$FAKE_COMMAND_LOG"
case "$*" in
  "cluster list -o json")
    printf '[{"name":"ncp-local-compute-1"},{"name":"ncp-local-cp"}]\n'
    ;;
  "cluster get ncp-local-cp")
    exit 0
    ;;
esac
`)
	writeFakeBin(t, binDir, "kubectl", `#!/usr/bin/env bash
set -euo pipefail
printf 'kubectl %s\n' "$*" >>"$FAKE_COMMAND_LOG"
case "$*" in
  *"cluster-info"*) exit 0 ;;
  *"get namespace"*) exit 1 ;;
  *"get nvcfbackend"*) exit 1 ;;
esac
`)
	writeFakeBin(t, binDir, "helm", `#!/usr/bin/env bash
set -euo pipefail
printf 'helm %s\n' "$*" >>"$FAKE_COMMAND_LOG"
`)

	cmd := exec.Command("bash", "scripts/destroy-stack.sh", "multi")
	cmd.Env = append(os.Environ(),
		"BDD_REPO_ROOT="+t.TempDir(),
		"FAKE_COMMAND_LOG="+logPath,
		"PATH="+binDir+":"+os.Getenv("PATH"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run destroy-stack.sh multi: %v\n%s", err, output)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read cleanup command log: %v", err)
	}
	return string(log)
}

func writeFakeBin(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}
