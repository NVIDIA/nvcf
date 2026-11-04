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

package rootfsonly

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeCmdline writes a /proc/<pid>/cmdline-shaped file (NUL-separated,
// NUL-terminated) under a fake proc root and returns the root.
func writeCmdline(t *testing.T, pid string, args ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 0, len(args))
	for _, a := range args {
		b = append(b, []byte(a)...)
		b = append(b, 0)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadEntryArgv(t *testing.T) {
	root := writeCmdline(t, "42", "/opt/nim/start_server.sh", "--port", "9000")
	got := readEntryArgv(root, 42)
	want := []string{"/opt/nim/start_server.sh", "--port", "9000"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readEntryArgv = %v, want %v", got, want)
	}
}

func TestReadEntryArgv_MissingOrEmpty(t *testing.T) {
	// No cmdline file → nil (best-effort: webhook falls back to pod cmd).
	if got := readEntryArgv(t.TempDir(), 7); got != nil {
		t.Errorf("missing cmdline: got %v, want nil", got)
	}
	// Empty cmdline (kernel thread) → nil.
	root := writeCmdline(t, "9")
	if got := readEntryArgv(root, 9); got != nil {
		t.Errorf("empty cmdline: got %v, want nil", got)
	}
}
