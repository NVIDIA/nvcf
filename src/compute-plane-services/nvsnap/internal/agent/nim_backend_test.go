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

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectBackend(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  BackendKind
	}{
		{
			name: "riva backend — /opt/riva tree present",
			setup: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "opt", "riva", "bin"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: BackendRiva,
		},
		{
			name: "triton backend — start_server.sh runs tritonserver",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, "opt", "nim")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				script := "#!/bin/bash\nexec tritonserver --model-repository=/models --http-port=8000\n"
				if err := os.WriteFile(filepath.Join(dir, "start_server.sh"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: BackendTriton,
		},
		{
			name: "vllm backend — start_server.sh launches vllm",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, "opt", "nim")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				script := "#!/bin/bash\nexec python3 -m vllm.entrypoints.openai.api_server --port 8000\n"
				if err := os.WriteFile(filepath.Join(dir, "start_server.sh"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: BackendVLLM,
		},
		{
			name: "riva wins over a vllm start_server.sh (gemma-NIM has both)",
			setup: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "opt", "riva"), 0o755); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(root, "opt", "nim")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				script := "#!/bin/bash\nexec python3 -m vllm ...\n"
				if err := os.WriteFile(filepath.Join(dir, "start_server.sh"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: BackendRiva,
		},
		{
			name: "empty rootfs — unknown",
			setup: func(t *testing.T, root string) {
				// nothing
			},
			want: BackendUnknown,
		},
		{
			name: "non-NIM workload (plain vllm or sglang image) — unknown, CRIU path stays",
			setup: func(t *testing.T, root string) {
				// vllm/vllm-openai image puts entrypoint at /usr/local/bin,
				// has no /opt/nim/ and no /opt/riva/. Should be unknown so
				// the caller defaults to CRIU.
				if err := os.MkdirAll(filepath.Join(root, "usr", "local", "bin"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: BackendUnknown,
		},
		{
			name: "start_server.sh exists but mentions neither tritonserver nor vllm",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, "opt", "nim")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "start_server.sh"), []byte("#!/bin/bash\nexec mystery-server\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: BackendUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			got := DetectBackend(0, root)
			if got != tc.want {
				t.Fatalf("DetectBackend = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectBackend_NoPidAndNoOverride(t *testing.T) {
	if got := DetectBackend(0, ""); got != BackendUnknown {
		t.Fatalf("DetectBackend(0, \"\") = %q, want %q", got, BackendUnknown)
	}
	if got := DetectBackend(-1, ""); got != BackendUnknown {
		t.Fatalf("DetectBackend(-1, \"\") = %q, want %q", got, BackendUnknown)
	}
}

func TestBackendKind_UseRootfsCapture(t *testing.T) {
	tests := []struct {
		b    BackendKind
		want bool
	}{
		{BackendRiva, true},
		{BackendTriton, true},
		{BackendVLLM, false},
		{BackendUnknown, false},
	}
	for _, tc := range tests {
		if got := tc.b.UseRootfsCapture(); got != tc.want {
			t.Errorf("BackendKind(%q).UseRootfsCapture() = %v, want %v", tc.b, got, tc.want)
		}
	}
}

func TestBackendRedirectError(t *testing.T) {
	e := &BackendRedirectError{Backend: BackendRiva}
	if e.Error() == "" {
		t.Fatal("error string must be non-empty")
	}
	// must mention the backend so callers can grep logs
	if !strings.Contains(e.Error(), "riva") {
		t.Fatalf("error must mention backend, got: %s", e.Error())
	}
}

func TestRootfsIsDefault(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{"unset → rootfs", "", true},
		{"empty → rootfs", "  ", true},
		{"criu lower → CRIU", "criu", false},
		{"CRIU upper → CRIU", "CRIU", false},
		{"Criu mixed → CRIU", "Criu", false},
		{"criu with whitespace → CRIU", "  criu  ", false},
		{"rootfs explicit → rootfs", "rootfs", true},
		{"garbage → rootfs (fail-safe)", "anything-else", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NVSNAP_DEFAULT_CAPTURE_PATH", tc.env)
			if got := RootfsIsDefault(); got != tc.want {
				t.Errorf("RootfsIsDefault() with env=%q = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// resolveCheckpointRedirect: caller can override the cluster default
// (rootfs) to force CRIU on a capable cell, but an explicit "criu" on a
// hard-incapable backend (Riva/Triton) errors rather than silently
// falling back. Multi-GPU is gated downstream, not here.
func TestResolveCheckpointRedirect(t *testing.T) {
	cases := []struct {
		name          string
		requested     string
		backend       BackendKind
		rootfsDefault bool
		wantRedirect  bool
		wantErr       bool
	}{
		// cluster default = rootfs (the live config)
		{"auto + vllm + rootfs-default -> rootfs", "", BackendVLLM, true, true, false},
		{"criu override beats rootfs-default", capturePathCRIU, BackendVLLM, true, false, false},
		{"explicit rootfs always redirects", capturePathRootfs, BackendVLLM, false, true, false},
		// cluster default = criu
		{"auto + vllm + criu-default -> criu", "", BackendVLLM, false, false, false},
		{"explicit rootfs even when criu-default", capturePathRootfs, BackendVLLM, false, true, false},
		// hard backend incapability
		{"auto + riva -> rootfs", "", BackendRiva, true, true, false},
		{"criu on riva -> ERROR", capturePathCRIU, BackendRiva, true, false, true},
		{"criu on triton -> ERROR", capturePathCRIU, BackendTriton, false, false, true},
		{"rootfs on riva -> rootfs", capturePathRootfs, BackendRiva, true, true, false},
		// validation
		{"invalid value -> ERROR", "bogus", BackendVLLM, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			redirect, err := resolveCheckpointRedirect(tc.requested, tc.backend, tc.rootfsDefault)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if err == nil && redirect != tc.wantRedirect {
				t.Errorf("redirect=%v, want %v", redirect, tc.wantRedirect)
			}
		})
	}
}
