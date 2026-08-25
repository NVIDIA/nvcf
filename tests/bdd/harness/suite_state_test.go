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
	"path/filepath"
	"testing"
)

func TestRestoreFeatureStateKeepsLifecycleStateUntilTeardown(t *testing.T) {
	root := t.TempDir()
	featurePath := filepath.Join(root, "feature.yaml")
	statePath := filepath.Join(root, "cli.state")
	writeTestFile(t, featurePath, "feature-before")
	writeTestFile(t, statePath, "state-before")
	const envName = "BDD_FEATURE_STATE_TEST"
	t.Setenv(envName, "env-before")

	suite := &Suite{
		Config:          Config{LedgerDir: filepath.Join(root, "ledger")},
		Ledger:          NewLedger(filepath.Join(root, "ledger", "features")),
		EnvLedger:       NewEnvLedger(),
		lifecycleLedger: NewLedger(filepath.Join(root, "ledger", "lifecycle")),
	}
	if err := suite.Ledger.Snapshot(featurePath); err != nil {
		t.Fatal(err)
	}
	if err := suite.snapshotCLIStateFile(statePath); err != nil {
		t.Fatal(err)
	}
	if err := suite.EnvLedger.Snapshot(envName); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, featurePath, "feature-after")
	writeTestFile(t, statePath, "state-after")
	if err := os.Setenv(envName, "env-after"); err != nil {
		t.Fatal(err)
	}

	if err := suite.RestoreFeatureState(); err != nil {
		t.Fatalf("restore feature state: %v", err)
	}
	assertTestFile(t, featurePath, "feature-before")
	assertTestFile(t, statePath, "state-after")
	if got := os.Getenv(envName); got != "env-before" {
		t.Fatalf("environment=%q", got)
	}

	if err := suite.Teardown(); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	assertTestFile(t, statePath, "state-before")
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Fatalf("%s=%q want %q", path, raw, want)
	}
}
