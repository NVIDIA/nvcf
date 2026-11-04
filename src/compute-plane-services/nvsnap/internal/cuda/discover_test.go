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

package cuda

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLibCudaDir(t *testing.T) {
	tests := []struct {
		name string
		maps string
		want string
	}{
		{
			name: "empty input",
			maps: "",
			want: "",
		},
		{
			name: "no libcuda at all",
			maps: lines(
				"7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /lib/x86_64-linux-gnu/ld-2.31.so",
				"7f0000001000-7f0000002000 r--p 00001000 fc:01 1  /lib/x86_64-linux-gnu/ld-2.31.so",
			),
			want: "",
		},
		{
			name: "libcudart (Runtime API) is not libcuda — must not match",
			maps: lines(
				"7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /opt/cuda/lib64/libcudart.so.12",
				"7f0000001000-7f0000002000 r--p 00001000 fc:01 1  /opt/cuda/lib64/libcudart.so.12",
			),
			want: "",
		},
		{
			name: "GKE COS layout (the regression that triggered this)",
			maps: lines(
				"7f000000a000-7f000000b000 r-xp 00000000 fc:01 1  /home/kubernetes/bin/nvidia/lib64/libcuda.so.580.126.09",
			),
			want: "/home/kubernetes/bin/nvidia/lib64",
		},
		{
			name: "older GKE / NVIDIA Operator layout",
			maps: lines(
				"7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /run/nvidia/driver/usr/lib/x86_64-linux-gnu/libcuda.so.535.183.06",
			),
			want: "/run/nvidia/driver/usr/lib/x86_64-linux-gnu",
		},
		{
			name: "bare-metal Ubuntu",
			maps: lines(
				"7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /usr/lib/x86_64-linux-gnu/libcuda.so.1",
			),
			want: "/usr/lib/x86_64-linux-gnu",
		},
		{
			name: "nvidia-container-runtime hookspath",
			maps: lines(
				"7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /usr/local/nvidia/lib64/libcuda.so.1",
			),
			want: "/usr/local/nvidia/lib64",
		},
		{
			name: "libcuda mixed with other libraries — finds libcuda not first match",
			maps: lines(
				"7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /lib/x86_64-linux-gnu/libpthread.so.0",
				"7f0000010000-7f0000011000 r-xp 00000000 fc:01 1  /lib/x86_64-linux-gnu/libc-2.31.so",
				"7f0000020000-7f0000021000 r-xp 00000000 fc:01 1  /opt/nvidia/lib64/libcuda.so.580.x",
			),
			want: "/opt/nvidia/lib64",
		},
		{
			name: "libcuda.so (no version suffix)",
			maps: lines(
				"7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /opt/nvidia/lib64/libcuda.so",
			),
			want: "/opt/nvidia/lib64",
		},
		{
			name: "anonymous mappings (no path) skipped",
			maps: lines(
				"7f0000000000-7f0000001000 rw-p 00000000 00:00 0",
				"7f0000010000-7f0000011000 r-xp 00000000 fc:01 1  /usr/local/nvidia/lib64/libcuda.so.1",
			),
			want: "/usr/local/nvidia/lib64",
		},
		{
			name: "deleted-file marker is part of path — directory still resolves correctly",
			maps: lines(
				"7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /usr/local/nvidia/lib64/libcuda.so.1 (deleted)",
			),
			// Filepath.Base on "libcuda.so.1 (deleted)" still starts with "libcuda.so" — accepted.
			// Filepath.Dir returns "/usr/local/nvidia/lib64".
			want: "/usr/local/nvidia/lib64",
		},
		{
			name: "first libcuda wins when multiple paths present",
			maps: lines(
				"7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /first/path/libcuda.so.1",
				"7f0000010000-7f0000011000 r-xp 00000000 fc:01 1  /second/path/libcuda.so.1",
			),
			want: "/first/path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLibCudaDir(strings.NewReader(tc.maps))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestDiscoverCudaLibDir exercises the file-IO + path-translation layer.
// We can't read a real /proc/<pid>/maps in a unit test, but we can build a
// fake hostProcRoot directory structure and verify the function reads the
// right file and translates paths correctly.
func TestDiscoverCudaLibDir(t *testing.T) {
	tmp := t.TempDir()

	procRoot := filepath.Join(tmp, "proc")
	pidDir := filepath.Join(procRoot, "12345")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mapsContent := "7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /home/kubernetes/bin/nvidia/lib64/libcuda.so.580.126.09\n"
	if err := os.WriteFile(filepath.Join(pidDir, "maps"), []byte(mapsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Containerized agent case: procRoot is at "/host/proc"; the same
	// host paths are visible as "/host" + path.
	containerView, hostView, err := DiscoverCudaLibDir(procRoot, "/host", 12345)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hostView != "/home/kubernetes/bin/nvidia/lib64" {
		t.Errorf("hostView = %q, want /home/kubernetes/bin/nvidia/lib64", hostView)
	}
	if containerView != "/host/home/kubernetes/bin/nvidia/lib64" {
		t.Errorf("containerView = %q, want /host/home/kubernetes/bin/nvidia/lib64", containerView)
	}

	// Non-container case: no /host prefix, both views identical.
	containerView, hostView, err = DiscoverCudaLibDir(procRoot, "", 12345)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hostView != "/home/kubernetes/bin/nvidia/lib64" || containerView != hostView {
		t.Errorf("non-container: containerView=%q hostView=%q want both /home/kubernetes/bin/nvidia/lib64", containerView, hostView)
	}

	// PID with no libcuda mapping: function returns "", "", nil (caller falls back).
	pidDir2 := filepath.Join(procRoot, "99999")
	if mkErr := os.MkdirAll(pidDir2, 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	if wErr := os.WriteFile(filepath.Join(pidDir2, "maps"), []byte("7f0000000000-7f0000001000 r-xp 00000000 fc:01 1  /lib/libc.so.6\n"), 0o644); wErr != nil {
		t.Fatal(wErr)
	}
	containerView, hostView, err = DiscoverCudaLibDir(procRoot, "/host", 99999)
	if err != nil {
		t.Fatalf("unexpected error for no-libcuda case: %v", err)
	}
	if containerView != "" || hostView != "" {
		t.Errorf("no-libcuda case: containerView=%q hostView=%q want both empty", containerView, hostView)
	}

	// Missing PID: error.
	_, _, err = DiscoverCudaLibDir(procRoot, "/host", 11111)
	if err == nil {
		t.Errorf("missing PID expected error, got nil")
	}
}

// lines joins per-line strings with newlines so the test data reads cleanly.
func lines(parts ...string) string {
	return strings.Join(parts, "\n") + "\n"
}

// fmt import is referenced by sprintf in helpers below.
var _ = fmt.Sprintf
