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
