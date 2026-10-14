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

package dsl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ledgerEvents = `{"namespace":"ns","summary":{"total_contexts":2,"by_event":{"InstanceReady":2}},"events":[{"event_name":"InstanceReady","source":"nvidia-spot"}]}`

func TestJSONFieldReadsNestedValues(t *testing.T) {
	cases := map[string]string{
		"namespace":                      "ns",
		"summary.total_contexts":         "2",
		"summary.by_event.InstanceReady": "2",
		"events.0.event_name":            "InstanceReady",
		"events.0.source":                "nvidia-spot",
	}
	for path, want := range cases {
		got, err := JSONField(ledgerEvents, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestJSONFieldReturnsCompactJSONForObjects(t *testing.T) {
	got, err := JSONField(ledgerEvents, "summary.by_event")
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"InstanceReady":2}` {
		t.Fatalf("got %q", got)
	}
}

func TestJSONFieldErrors(t *testing.T) {
	for _, path := range []string{"", "missing", "events.5", "events.x", "namespace.child"} {
		if _, err := JSONField(ledgerEvents, path); err == nil {
			t.Fatalf("path %q: expected error", path)
		}
	}
	if _, err := JSONField("not json", "a"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestJSONFieldAtLeast(t *testing.T) {
	if err := JSONFieldAtLeast(ledgerEvents, "summary.by_event.InstanceReady", 1); err != nil {
		t.Fatalf("at least 1: %v", err)
	}
	if err := JSONFieldAtLeast(ledgerEvents, "summary.by_event.InstanceReady", 3); err == nil {
		t.Fatal("expected failure for min 3")
	}
	if err := JSONFieldAtLeast(ledgerEvents, "namespace", 1); err == nil {
		t.Fatal("expected failure for non-numeric field")
	}
}

func TestCLIStatePathMirrorsCLI(t *testing.T) {
	got := CLIStatePath("/home/u", "/repo/tests/bdd/fixtures/nvcf-cli-local.yaml")
	if got != "/home/u/.nvcf-cli.nvcf-cli-local.state" {
		t.Fatalf("got %q", got)
	}
}

func TestCLIStateToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte(`{"token":"abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := CLIStateToken(path)
	if err != nil || got != "abc" {
		t.Fatalf("token = %q, err = %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CLIStateToken(path); err == nil || !strings.Contains(err.Error(), "nvcf-cli init") {
		t.Fatalf("expected missing-token error, got %v", err)
	}
	if _, err := CLIStateToken(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("expected read error")
	}
}
