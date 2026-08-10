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

package harness

import (
	"os"
	"testing"
)

func TestEnvLedgerRestoresPreviouslySetVar(t *testing.T) {
	const name = "BDD_ENV_LEDGER_TEST_PRESET"
	t.Setenv(name, "original-value")

	l := NewEnvLedger()
	if err := l.Snapshot(name); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := os.Setenv(name, "overwritten"); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	if err := l.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := os.Getenv(name); got != "original-value" {
		t.Fatalf("got %q, want original-value", got)
	}
}

func TestEnvLedgerUnsetsVarThatWasNotSet(t *testing.T) {
	const name = "BDD_ENV_LEDGER_TEST_UNSET"
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("pre-unset: %v", err)
	}

	l := NewEnvLedger()
	if err := l.Snapshot(name); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := os.Setenv(name, "feature-wrote-this"); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	if err := l.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, ok := os.LookupEnv(name); ok {
		t.Fatalf("expected %s to be unset after restore, but it is still present", name)
	}
}

func TestEnvLedgerSnapshotIsIdempotentPerName(t *testing.T) {
	const name = "BDD_ENV_LEDGER_TEST_IDEMPOTENT"
	t.Setenv(name, "first")

	l := NewEnvLedger()
	if err := l.Snapshot(name); err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}

	// Suite-internal write between snapshots: imagine a scenario
	// Background ran the export step, then a later Background ran it
	// again. The second Snapshot must not record "second" as the
	// original; the original was "first" before any suite work.
	if err := os.Setenv(name, "second"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	if err := l.Snapshot(name); err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}

	if err := l.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := os.Getenv(name); got != "first" {
		t.Fatalf("got %q, want first", got)
	}
}

func TestEnvLedgerRejectsEmptyName(t *testing.T) {
	l := NewEnvLedger()
	if err := l.Snapshot(""); err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}
