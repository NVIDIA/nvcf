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

func TestRestoreFeatureStateResetsCLISelectionAndKeepsLifecycleBaseline(t *testing.T) {
	root := t.TempDir()
	featurePath := filepath.Join(root, "feature.yaml")
	statePath := filepath.Join(root, "cli.state")
	writeTestFile(t, featurePath, "feature-before")
	writeTestFile(t, statePath, "state-before")
	const envName = "BDD_FEATURE_STATE_TEST"
	t.Setenv(envName, "env-before")

	suite := &Suite{
		Config:             Config{LedgerDir: filepath.Join(root, "ledger")},
		CLIStatePath:       statePath,
		Ledger:             NewLedger(filepath.Join(root, "ledger", "features")),
		EnvLedger:          NewEnvLedger(),
		Cache:              NewCommandCache(),
		lifecycleLedger:    NewLedger(filepath.Join(root, "ledger", "lifecycle")),
		featureStateLedger: NewLedger(filepath.Join(root, "ledger", "feature-cli-state")),
	}
	suite.Cache.Record("provider command")
	if err := suite.Ledger.Snapshot(featurePath); err != nil {
		t.Fatal(err)
	}
	if err := suite.snapshotCLIStateFile(statePath); err != nil {
		t.Fatal(err)
	}
	if err := suite.featureStateLedger.Snapshot(statePath); err != nil {
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
	assertTestFile(t, statePath, "state-before")
	if got := os.Getenv(envName); got != "env-before" {
		t.Fatalf("environment=%q", got)
	}
	if suite.Cache.Has("provider command") {
		t.Fatal("feature restore preserved suite command cache")
	}
	writeTestFile(t, statePath, "second-feature")

	if err := suite.Teardown(); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	assertTestFile(t, statePath, "state-before")
}

func TestCLIStatePathForConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for config, want := range map[string]string{
		"/tmp/nvcf-cli-local.yaml": filepath.Join(home, ".nvcf-cli.nvcf-cli-local.state"),
		"/tmp/.nvcf-cli.yaml":      filepath.Join(home, ".nvcf-cli.state"),
		"default":                  filepath.Join(home, ".nvcf-cli.state"),
	} {
		got, err := CLIStatePathForConfig(config)
		if err != nil {
			t.Fatalf("state path for %s: %v", config, err)
		}
		if got != want {
			t.Fatalf("state path for %s=%s want %s", config, got, want)
		}
	}
}

func TestPrepareIsolatedCLISessionCopiesStateWithoutMutatingSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	configPath := filepath.Join(root, "source.yaml")
	writeTestFile(t, configPath, "base_http_url: http://example\n")
	sourceStatePath, err := CLIStatePathForConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, sourceStatePath, "source-state")
	suite := &Suite{
		Config: Config{
			RepoRoot:  root,
			OutDir:    filepath.Join(root, "out", "run-1"),
			LedgerDir: filepath.Join(root, "out", "run-1", "originals"),
		},
		lifecycleLedger: NewLedger(filepath.Join(root, "out", "run-1", "originals", "lifecycle")),
	}
	if err := suite.prepareIsolatedCLISession(configPath); err != nil {
		t.Fatalf("prepare isolated session: %v", err)
	}
	if suite.CLIConfigPath == configPath || suite.CLIStatePath == sourceStatePath {
		t.Fatalf("session was not isolated: config=%s state=%s", suite.CLIConfigPath, suite.CLIStatePath)
	}
	assertTestFile(t, suite.CLIStatePath, "source-state")
	writeTestFile(t, suite.CLIStatePath, "isolated-change")
	assertTestFile(t, sourceStatePath, "source-state")
	if err := suite.lifecycleLedger.RestoreAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(suite.CLIStatePath); !os.IsNotExist(err) {
		t.Fatalf("isolated state was not removed: %v", err)
	}
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
