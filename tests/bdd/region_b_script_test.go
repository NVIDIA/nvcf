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

func TestInstallLLMRegionBCreatesWatchAliasInBothClusters(t *testing.T) {
	binDir := t.TempDir()
	applyDir := t.TempDir()

	helmScript := `#!/usr/bin/env bash
set -euo pipefail
case " $* " in
  *" get values "*) printf '{"llmRequestRouter":{}}\n' ;;
  *) cat >/dev/null ;;
esac
`
	kubectlScript := `#!/usr/bin/env bash
set -euo pipefail
context=""
previous=""
for argument in "$@"; do
  if [[ "${previous}" == "--context" ]]; then
    context="${argument}"
  fi
  previous="${argument}"
done
case " $* " in
  *" get endpoints llm-request-router "*) printf '192.0.2.10' ;;
  *" apply -f - "*)
    cat >>"${FAKE_APPLY_DIR}/${context}.yaml"
    printf '\n---\n' >>"${FAKE_APPLY_DIR}/${context}.yaml"
    ;;
esac
`
	jqScript := `#!/usr/bin/env bash
set -euo pipefail
cat
`
	for name, body := range map[string]string{
		"helm":    helmScript,
		"jq":      jqScript,
		"kubectl": kubectlScript,
	} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}

	cmd := exec.Command("bash", "scripts/install-llm-region-b.sh")
	cmd.Env = append(os.Environ(),
		"CONTROL_CONTEXT=bdd-control",
		"COMPUTE_CONTEXT=bdd-compute",
		"FAKE_APPLY_DIR="+applyDir,
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"REPO_ROOT="+t.TempDir(),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install region B: %v\n%s", err, output)
	}

	for _, context := range []string{"bdd-control", "bdd-compute"} {
		manifestPath := filepath.Join(applyDir, context+".yaml")
		manifest, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatalf("read %s aliases: %v", context, err)
		}
		for _, want := range []string{
			"kind: Service\nmetadata:\n  name: region-b-watch",
			"kind: Endpoints\nmetadata:\n  name: region-b-watch",
			"- ip: 192.0.2.10",
			"name: llm-grpc",
			"name: llm-quic",
		} {
			if !strings.Contains(string(manifest), want) {
				t.Fatalf("%s aliases missing %q:\n%s", context, want, manifest)
			}
		}
	}
}
