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
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The committed examples are OSS-allowlisted files and must carry the SPDX
// header. writeConfig owns that now, rather than each caller re-adding it.
func TestWriteConfigPrependsSPDXHeaderAndPreservesBody(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "rendered config", data: []byte("receivers:\n  otlp: {}\n")},
		{name: "empty body", data: []byte{}},
		{name: "body already mentioning SPDX", data: []byte("# SPDX-notes: see above\nkey: value\n")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := writeConfig(tt.data, path); err != nil {
				t.Fatalf("writeConfig failed: %v", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back failed: %v", err)
			}

			if !bytes.HasPrefix(got, []byte(spdxHeader)) {
				t.Fatalf("output does not start with the SPDX header, got %q", got)
			}
			if body := got[len(spdxHeader):]; !bytes.Equal(body, tt.data) {
				t.Fatalf("body changed: want %q, got %q", tt.data, body)
			}
		})
	}
}

// Regenerating over an existing file must replace it, not append, so repeated
// runs of the example generator stay byte-identical.
func TestWriteConfigIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("receivers:\n  otlp: {}\n")

	if err := writeConfig(data, path); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after first write failed: %v", err)
	}

	if err := writeConfig(data, path); err != nil {
		t.Fatalf("second write failed: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after second write failed: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Fatalf("regeneration is not idempotent:\nfirst:  %q\nsecond: %q", first, second)
	}
	if n := bytes.Count(second, []byte("SPDX-License-Identifier")); n != 1 {
		t.Fatalf("expected exactly one SPDX header after regeneration, got %d", n)
	}
}
