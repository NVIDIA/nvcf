// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Only Helm's registry calls are stubbed. The actual Bash adapter invokes the
// compiled verifier against real source and downloaded tar/gzip archives.
func TestOCIAdapter(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "chart-artifact")
	if output, err := exec.Command("go", "build", "-o", tool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build verifier: %v\n%s", err, output)
	}
	cases := []struct {
		name, action, preflight, push, readback, version string
		wantCode                                         int
		wantPush                                         bool
	}{
		{name: "read-only absent", action: "check-oci", wantCode: 2},
		{name: "read-only matching", action: "check-oci", preflight: "matching"},
		{name: "read-only mismatch", action: "check-oci", preflight: "mismatch", wantCode: 1},
		{name: "existing identical retry", preflight: "matching"},
		{name: "existing changed", preflight: "mismatch", wantCode: 1},
		{name: "auth failure", preflight: "auth", wantCode: 1},
		{name: "auth with not found", preflight: "auth-not-found", wantCode: 1},
		{name: "transport failure", preflight: "transport", wantCode: 1},
		{name: "transport with not found", preflight: "transport-not-found", wantCode: 1},
		{name: "unknown with version 404", preflight: "unknown", version: "1.0.404", wantCode: 1},
		{name: "absent version 401", version: "1.0.401", wantPush: true},
		{name: "absent version 403", version: "1.0.403", wantPush: true},
		{name: "push then matching", wantPush: true},
		{name: "push then mismatch", readback: "mismatch", wantPush: true, wantCode: 1},
		{name: "conflict then matching", push: "conflict", wantPush: true},
		{name: "conflict then mismatch", push: "conflict", readback: "mismatch", wantPush: true, wantCode: 1},
		{name: "push failure", push: "failure", wantPush: true, wantCode: 1},
		{name: "download auth failure", readback: "auth", wantPush: true, wantCode: 1},
		{name: "download missing", readback: "missing", wantPush: true, wantCode: 1},
		{name: "download corrupt", readback: "corrupt", wantPush: true, wantCode: 1},
		{name: "download wrong version", readback: "wrong-version", wantPush: true, wantCode: 1},
		{name: "source wrong version", version: "2.0.0", preflight: "source-wrong-version", wantCode: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			version := tc.version
			if version == "" {
				version = "1.0.0"
			}
			content := entries()
			if tc.preflight != "source-wrong-version" {
				content[0].content = strings.ReplaceAll(content[0].content, "1.0.0", version)
			}
			source := archive(t, content, 100)
			matching := archive(t, content, 200)
			content[2].content = "replicas: 99\n"
			mismatch := archive(t, content, 200)
			content = entries()
			content[0].content = "name: demo\nversion: 9.9.9\n"
			wrongVersion := archive(t, content, 200)
			if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(helmStub), 0755); err != nil {
				t.Fatal(err)
			}
			action := tc.action
			if action == "" {
				action = "publish-oci"
			}
			// Invoke the sourced function in a conditional too: Bash disables
			// implicit errexit inside conditional functions, so guards must return
			// explicitly before a mismatch can be followed by a successful echo.
			script := `source "$1"; shift; action="$1"; shift; if "${action//-/_}_chart_archive" "$@"; then exit 0; else exit $?; fi`
			cmd := exec.Command("bash", "-c", script, "test", "../ci/chart-publication.sh", action, source, "demo", version, "oci://registry.example/charts")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "CHART_ARTIFACT_TOOL="+tool,
				"STUB_STATE="+dir, "STUB_PREFLIGHT="+tc.preflight, "STUB_PUSH="+tc.push, "STUB_READBACK="+tc.readback,
				"STUB_MATCHING="+matching, "STUB_MISMATCH="+mismatch, "STUB_WRONG_VERSION="+wrongVersion)
			output, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				code = cmd.ProcessState.ExitCode()
			}
			if code != tc.wantCode {
				t.Fatalf("got code %d, want %d: %s", code, tc.wantCode, output)
			}
			_, err = os.Stat(filepath.Join(dir, "pushed"))
			if pushed := err == nil; pushed != tc.wantPush {
				t.Fatalf("push happened=%v, want %v: %s", pushed, tc.wantPush, output)
			}
		})
	}
}

const helmStub = `#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  push)
    touch "${STUB_STATE}/pushed"
    case "${STUB_PUSH}" in
      conflict) echo '409 Conflict'; exit 1 ;;
      failure) echo 'push denied'; exit 1 ;;
    esac
    ;;
  pull)
    mode="${STUB_PREFLIGHT}"
    if [[ -f "${STUB_STATE}/pushed" ]]; then mode="${STUB_READBACK:-matching}"; fi
    case "${mode}" in
      matching) cp "${STUB_MATCHING}" "$6/demo-$4.tgz" ;;
      mismatch) cp "${STUB_MISMATCH}" "$6/demo-$4.tgz" ;;
      wrong-version) cp "${STUB_WRONG_VERSION}" "$6/demo-$4.tgz" ;;
      missing) : ;;
      corrupt) printf 'broken' >"$6/demo-$4.tgz" ;;
      auth) echo '401 Unauthorized'; exit 1 ;;
      auth-not-found) echo '404 not found: authentication token not found'; exit 1 ;;
      transport) echo 'connection refused'; exit 1 ;;
      transport-not-found) echo 'connection refused for mirror: not found'; exit 1 ;;
      unknown) echo "Error: $2:$4 lookup failed"; exit 1 ;;
      *) echo "Error: $2:$4: not found"; exit 1 ;;
    esac
    ;;
  *) echo "unexpected Helm command: $*"; exit 1 ;;
esac
`
