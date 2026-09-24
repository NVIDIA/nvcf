/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envFn builds a getenv closure over a map for hermetic tests.
func envFn(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseConfig_HappyPath(t *testing.T) {
	cfg, err := parseConfig(envFn(map[string]string{
		"NVSNAP_CAPTURED_DIR":   "/nvsnap-captured",
		"NVSNAP_SCRATCH_DIR":    "/nvsnap-scratch",
		"NVSNAP_ORIG_COMMAND":   `["/bin/bash","-lc","vllm serve"]`,
		"NVSNAP_ORIG_CWD":       "/opt/nim",
		"NVSNAP_ROOTFS_VOLUMES": `[{"name":"model-data","mountPath":"/config/models"}]`,
	}))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.capturedDir != "/nvsnap-captured" || cfg.scratchDir != "/nvsnap-scratch" {
		t.Errorf("dirs wrong: %+v", cfg)
	}
	if len(cfg.argv) != 3 || cfg.argv[0] != "/bin/bash" {
		t.Errorf("argv wrong: %v", cfg.argv)
	}
	if cfg.cwd != "/opt/nim" {
		t.Errorf("cwd = %q, want /opt/nim", cfg.cwd)
	}
	if len(cfg.volumes) != 1 || cfg.volumes[0].Name != "model-data" || cfg.volumes[0].MountPath != "/config/models" {
		t.Errorf("volumes wrong: %+v", cfg.volumes)
	}
}

func TestParseConfig_CwdDefaultsToRoot(t *testing.T) {
	cfg, err := parseConfig(envFn(map[string]string{
		"NVSNAP_CAPTURED_DIR": "/c",
		"NVSNAP_SCRATCH_DIR":  "/s",
		"NVSNAP_ORIG_COMMAND": `["/start"]`,
	}))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.cwd != "/" {
		t.Errorf("cwd = %q, want / (default)", cfg.cwd)
	}
	if len(cfg.volumes) != 0 {
		t.Errorf("volumes should be empty, got %+v", cfg.volumes)
	}
}

func TestParseConfig_Errors(t *testing.T) {
	base := map[string]string{
		"NVSNAP_CAPTURED_DIR": "/c",
		"NVSNAP_SCRATCH_DIR":  "/s",
		"NVSNAP_ORIG_COMMAND": `["/start"]`,
	}
	cases := []struct {
		name string
		mut  func(map[string]string)
	}{
		{"missing captured dir", func(m map[string]string) { delete(m, "NVSNAP_CAPTURED_DIR") }},
		{"missing scratch dir", func(m map[string]string) { delete(m, "NVSNAP_SCRATCH_DIR") }},
		{"missing command", func(m map[string]string) { delete(m, "NVSNAP_ORIG_COMMAND") }},
		{"command not json", func(m map[string]string) { m["NVSNAP_ORIG_COMMAND"] = "vllm serve" }},
		{"command empty array", func(m map[string]string) { m["NVSNAP_ORIG_COMMAND"] = `[]` }},
		{"command empty arg0", func(m map[string]string) { m["NVSNAP_ORIG_COMMAND"] = `["",""]` }},
		{"volumes not json", func(m map[string]string) { m["NVSNAP_ROOTFS_VOLUMES"] = "nope" }},
		{"volume missing path", func(m map[string]string) { m["NVSNAP_ROOTFS_VOLUMES"] = `[{"name":"x"}]` }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			tc.mut(m)
			if _, err := parseConfig(envFn(m)); err == nil {
				t.Errorf("expected error for %q, got nil", tc.name)
			}
		})
	}
}

func TestParseMountpoints(t *testing.T) {
	// Real-ish mountinfo: root, a deep nvidia driver-lib file bind, /dev,
	// /dev/shm, and a path with an escaped space.
	data := `1 0 8:1 / / rw - ext4 /dev/sda1 rw
22 1 0:5 / /dev rw - devtmpfs udev rw
23 22 0:6 / /dev/shm rw - tmpfs shm rw
99 1 8:1 /lib/x/libcuda.so.1 /usr/lib/x86_64-linux-gnu/libcuda.so.1 ro - ext4 /dev/sda1 ro
77 1 0:7 / /var/with\040space rw - tmpfs t rw`
	got := parseMountpoints(data)
	// Shallowest-first: "/" then depth-1, then deeper.
	if got[0] != "/" {
		t.Errorf("first should be /, got %q (%v)", got[0], got)
	}
	want := map[string]bool{"/": true, "/dev": true, "/dev/shm": true,
		"/usr/lib/x86_64-linux-gnu/libcuda.so.1": true, "/var/with space": true}
	for _, m := range got {
		delete(want, m)
	}
	if len(want) != 0 {
		t.Errorf("missing mountpoints: %v (got %v)", want, got)
	}
	// Verify shallow-before-deep ordering by slash count.
	for i := 1; i < len(got); i++ {
		if strings.Count(got[i-1], "/") > strings.Count(got[i], "/") {
			t.Errorf("not sorted shallow-first: %v", got)
		}
	}
}

// TestRecreateRuntimeDirs covers the vLLM ENOENT case: the source container's
// entrypoint created /var/run/vllm before starting, bash exec'd itself away so
// the mkdir is absent from the recorded argv, and restore must recreate the
// directory or the engine's unix socket bind fails.
func TestRecreateRuntimeDirs(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "vllm")

	recreateRuntimeDirs(envFunc(`[{"path":"`+target+`","mode":493,"uid":0,"gid":0}]`), []string{sandbox})

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("runtime dir not recreated: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s is not a directory", target)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %o, want 755 (umask must not narrow it)", got)
	}
}

// A recorded 0000 directory must come back as 0000, not widened to 0755.
func TestRecreateRuntimeDirsPreservesZeroMode(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "locked")

	recreateRuntimeDirs(envFunc(`[{"path":"`+target+`","mode":0,"uid":0,"gid":0}]`), []string{sandbox})

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("dir not created: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0 {
		t.Errorf("mode = %o, want 0 (an explicit 0000 must not be widened)", got)
	}
}

// Malformed or hostile input must not abort a restore, and must not create
// anything. The traversal string is built by concatenation, NOT filepath.Join,
// because Join normalizes ".." away and would silently make this a clean path
// that legitimately gets created -- which is exactly how an earlier version of
// this test passed against a guard that did not actually block traversal.
func TestRecreateRuntimeDirsIgnoresBadInput(t *testing.T) {
	sandbox := t.TempDir()
	escapeTarget := filepath.Join(sandbox, "etc", "pwn")
	traversal := sandbox + "/run/../etc/pwn"
	outside := filepath.Join(t.TempDir(), "elsewhere")

	for name, val := range map[string]string{
		"empty":          "",
		"not json":       "{{{",
		"wrong type":     `{"path":"/x"}`,
		"relative":       `[{"path":"var/run/x","mode":493}]`,
		"parent escape":  `[{"path":"` + traversal + `","mode":493}]`,
		"outside root":   `[{"path":"` + outside + `","mode":493}]`,
		"root itself":    `[{"path":"/","mode":493}]`,
		"prefix look-al": `[{"path":"` + sandbox + `-evil/x","mode":493}]`,
	} {
		t.Run(name, func(t *testing.T) {
			recreateRuntimeDirs(envFunc(val), []string{sandbox})
		})
	}

	for _, p := range []string{escapeTarget, outside, sandbox + "-evil/x"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("created %s (err=%v); the path guard regressed", p, err)
		}
	}
}

func envFunc(val string) func(string) string {
	return func(k string) string {
		if k == envRuntimeDirs {
			return val
		}
		return ""
	}
}
