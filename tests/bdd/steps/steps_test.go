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

package steps

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v21"

	"nvcf-bdd/harness"
)

type recordedRun struct {
	command        string
	sensitiveStdin string
}

type fakeRunner struct {
	runs       []recordedRun
	result     harness.Result
	runResults []harness.Result
	err        error
}

func (f *fakeRunner) Run(_ context.Context, command string) (harness.Result, error) {
	f.runs = append(f.runs, recordedRun{command: command})
	if index := len(f.runs) - 1; index < len(f.runResults) {
		return f.runResults[index], f.err
	}
	return f.result, f.err
}

func (f *fakeRunner) RunWithSensitiveStdin(
	_ context.Context,
	command,
	sensitiveStdin string,
) (harness.Result, error) {
	f.runs = append(f.runs, recordedRun{command: command, sensitiveStdin: sensitiveStdin})
	if index := len(f.runs) - 1; index < len(f.runResults) {
		return f.runResults[index], f.err
	}
	return f.result, f.err
}

func (f *fakeRunner) RunWithTTY(ctx context.Context, command string) (harness.Result, error) {
	return f.Run(ctx, command)
}

func newScenarioContext(t *testing.T) (*ScenarioContext, *fakeRunner) {
	t.Helper()
	repoRoot := t.TempDir()
	fake := &fakeRunner{}
	suite := &harness.Suite{
		Config:    harness.Config{RepoRoot: repoRoot},
		Runner:    fake,
		Ledger:    harness.NewLedger(filepath.Join(repoRoot, "snaps")),
		EnvLedger: harness.NewEnvLedger(),
		Cache:     harness.NewCommandCache(),
	}
	return NewScenarioContext(suite), fake
}

func TestICopyFileSnapshotsAndCopies(t *testing.T) {
	sc, _ := newScenarioContext(t)
	srcRel := "src.yaml"
	destRel := "dest/dest.yaml"
	srcAbs := filepath.Join(sc.Suite.Config.RepoRoot, srcRel)
	if err := os.WriteFile(srcAbs, []byte("hello: world\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := sc.iCopyFile(srcRel, destRel); err != nil {
		t.Fatalf("copy: %v", err)
	}
	destAbs := filepath.Join(sc.Suite.Config.RepoRoot, destRel)
	got, _ := os.ReadFile(destAbs)
	if string(got) != "hello: world\n" {
		t.Fatalf("dest body = %q", got)
	}
	// Verify Ledger snapshotted dest by mutating and restoring.
	if err := os.WriteFile(destAbs, []byte("mutated\n"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := sc.Suite.Ledger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(destAbs); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dest should be deleted: %v", err)
	}
}

func TestIPrepareHelmfileEnvironmentCopiesUpdatesAndRestoresAbsentDestination(t *testing.T) {
	sc, _ := newScenarioContext(t)
	t.Setenv("BDD_TMP_ENV_FIXTURE", "fixtures/base.yaml")
	t.Setenv("SAMPLE_NGC_ORG", "test-org")
	t.Setenv("SAMPLE_NGC_TEAM", "test-team")
	fixtureAbs := filepath.Join(sc.Suite.Config.RepoRoot, "fixtures", "base.yaml")
	if err := os.MkdirAll(filepath.Dir(fixtureAbs), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(fixtureAbs, []byte("global:\n  storageClass: local-path\n"), 0o644); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	table := docTable(t, [][]string{
		{"global.imagePullSecrets[0].name", "nvcr-pull-secret"},
		{"global.image.repository", "${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}"},
	})

	if err := sc.iPrepareHelmfileEnvironment("local-bdd", "self-managed", "${BDD_TMP_ENV_FIXTURE}", table); err != nil {
		t.Fatalf("prepare environment: %v", err)
	}
	dest := filepath.Join(sc.Suite.Config.RepoRoot, "deploy", "stacks", "self-managed", "environments", "local-bdd.yaml")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	for _, want := range []string{"storageClass: local-path", "name: nvcr-pull-secret", "repository: test-org/test-team"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("destination missing %q:\n%s", want, got)
		}
	}

	if err := sc.Suite.Ledger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated destination should be removed: %v", err)
	}
}

func TestIPrepareHelmfileEnvironmentRestoresExistingDestination(t *testing.T) {
	sc, _ := newScenarioContext(t)
	fixture := "fixtures/base.yaml"
	fixtureAbs := filepath.Join(sc.Suite.Config.RepoRoot, fixture)
	if err := os.MkdirAll(filepath.Dir(fixtureAbs), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(fixtureAbs, []byte("global: {}\n"), 0o644); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	dest := filepath.Join(sc.Suite.Config.RepoRoot, "deploy", "stacks", "observability", "environments", "existing.yaml")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	original := []byte("operatorAuthored: true\n")
	if err := os.WriteFile(dest, original, 0o640); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	table := docTable(t, [][]string{{"observability.mode", "install"}})

	if err := sc.iPrepareHelmfileEnvironment("existing", "observability", fixture, table); err != nil {
		t.Fatalf("prepare environment: %v", err)
	}
	if err := sc.Suite.Ledger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read restored destination: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("restored body = %q, want %q", got, original)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat restored destination: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("restored mode = %o, want 640", info.Mode().Perm())
	}
}

func TestIPrepareHelmfileEnvironmentRejectsInvalidNamesBeforeWriting(t *testing.T) {
	sc, _ := newScenarioContext(t)
	table := docTable(t, [][]string{{"global.image.registry", "nvcr.io"}})
	for _, tc := range []struct {
		name        string
		environment string
		stack       string
	}{
		{name: "unsupported stack", environment: "local", stack: "unknown"},
		{name: "unsafe environment", environment: "../local", stack: "self-managed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := sc.iPrepareHelmfileEnvironment(tc.environment, tc.stack, "missing.yaml", table); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(sc.Suite.Config.RepoRoot, "deploy")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("validation failure should not create deploy tree: %v", err)
	}
}

func TestIPrepareSelfManagedSecretsFileRendersInterpolatedPaths(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NGC_API_KEY", "test-api-key")
	t.Setenv("BDD_TMP_SECRETS_NAME", "local-bdd-secrets.yaml")
	t.Setenv("BDD_TMP_TEMPLATE_NAME", "secrets.yaml.template")
	templateRel := "templates/${BDD_TMP_TEMPLATE_NAME}"
	templateAbs := filepath.Join(sc.Suite.Config.RepoRoot, "templates", "secrets.yaml.template")
	if err := os.MkdirAll(filepath.Dir(templateAbs), 0o755); err != nil {
		t.Fatalf("mkdir template: %v", err)
	}
	if err := os.WriteFile(templateAbs, []byte("registryCredential: REPLACE_WITH_BASE64_DOCKER_CREDENTIAL\n"), 0o644); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	destRel := "secrets/${BDD_TMP_SECRETS_NAME}"
	if err := sc.iPrepareSelfManagedSecretsFile(destRel, templateRel); err != nil {
		t.Fatalf("prepare secrets: %v", err)
	}
	destAbs := filepath.Join(sc.Suite.Config.RepoRoot, "secrets", "local-bdd-secrets.yaml")
	got, err := os.ReadFile(destAbs)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	wantCredential := base64.StdEncoding.EncodeToString([]byte("$oauthtoken:test-api-key"))
	if string(got) != "registryCredential: "+wantCredential+"\n" {
		t.Fatalf("destination body does not contain the expected encoded credential")
	}
	info, err := os.Stat(destAbs)
	if err != nil {
		t.Fatalf("stat destination: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("destination mode = %o, want 600", info.Mode().Perm())
	}
	if len(fake.runs) != 0 {
		t.Fatalf("secret preparation wrote %d command log entries, want 0", len(fake.runs))
	}
}

func TestIPrepareSelfManagedSecretsFileRestoresExistingDestination(t *testing.T) {
	sc, _ := newScenarioContext(t)
	t.Setenv("NGC_API_KEY", "test-api-key")
	templateRel := "secrets.yaml.template"
	templateAbs := filepath.Join(sc.Suite.Config.RepoRoot, templateRel)
	if err := os.WriteFile(templateAbs, []byte("registryCredential: REPLACE_WITH_BASE64_DOCKER_CREDENTIAL\n"), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	destRel := "local-secrets.yaml"
	destAbs := filepath.Join(sc.Suite.Config.RepoRoot, destRel)
	original := []byte("operator-authored: original\n")
	if err := os.WriteFile(destAbs, original, 0o640); err != nil {
		t.Fatalf("seed destination: %v", err)
	}

	if err := sc.iPrepareSelfManagedSecretsFile(destRel, templateRel); err != nil {
		t.Fatalf("prepare secrets: %v", err)
	}
	renderedInfo, err := os.Stat(destAbs)
	if err != nil {
		t.Fatalf("stat rendered destination: %v", err)
	}
	if renderedInfo.Mode().Perm() != 0o600 {
		t.Fatalf("rendered destination mode = %o, want 600", renderedInfo.Mode().Perm())
	}
	if err := sc.Suite.Ledger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(destAbs)
	if err != nil {
		t.Fatalf("read restored destination: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("restored body = %q, want original", got)
	}
	info, err := os.Stat(destAbs)
	if err != nil {
		t.Fatalf("stat restored destination: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("restored mode = %o, want 640", info.Mode().Perm())
	}
}

func TestIPrepareSelfManagedSecretsFileRestoresAbsentDestination(t *testing.T) {
	sc, _ := newScenarioContext(t)
	t.Setenv("NGC_API_KEY", "test-api-key")
	templateRel := "secrets.yaml.template"
	templateAbs := filepath.Join(sc.Suite.Config.RepoRoot, templateRel)
	if err := os.WriteFile(templateAbs, []byte("registryCredential: REPLACE_WITH_BASE64_DOCKER_CREDENTIAL\n"), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	destRel := "generated/local-secrets.yaml"
	destAbs := filepath.Join(sc.Suite.Config.RepoRoot, destRel)

	if err := sc.iPrepareSelfManagedSecretsFile(destRel, templateRel); err != nil {
		t.Fatalf("prepare secrets: %v", err)
	}
	if err := sc.Suite.Ledger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(destAbs); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated destination should be removed: %v", err)
	}
}

func TestIPrepareSelfManagedSecretsFileFailureHidesCredentialMaterial(t *testing.T) {
	sc, fake := newScenarioContext(t)
	apiKey := "sensitive-test-api-key"
	t.Setenv("NGC_API_KEY", apiKey)
	templateRel := "secrets.yaml.template"
	templateAbs := filepath.Join(sc.Suite.Config.RepoRoot, templateRel)
	if err := os.WriteFile(templateAbs, []byte("registryCredential: missing\n"), 0o600); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	err := sc.iPrepareSelfManagedSecretsFile("local-secrets.yaml", templateRel)
	if err == nil {
		t.Fatal("expected missing-placeholder error")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("$oauthtoken:" + apiKey))
	for _, secret := range []string{apiKey, encoded} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked credential material: %v", err)
		}
	}
	if len(fake.runs) != 0 {
		t.Fatalf("failed secret preparation wrote %d command log entries, want 0", len(fake.runs))
	}
}

func TestIUpdateYAMLFileWritesKeys(t *testing.T) {
	sc, _ := newScenarioContext(t)
	rel := "env.yaml"
	abs := filepath.Join(sc.Suite.Config.RepoRoot, rel)
	if err := os.WriteFile(abs, []byte("global:\n  storageClass: local-path\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	table := docTable(t, [][]string{{"global.image.registry", "nvcr.io"}, {"global.image.repository", "test-org/test-team"}})
	if err := sc.iUpdateYAMLFile(rel, table); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := os.ReadFile(abs)
	if !strings.Contains(string(got), "registry: nvcr.io") {
		t.Fatalf("missing key:\n%s", got)
	}
}

func TestISubstituteBlockReplacesAndRestoresFile(t *testing.T) {
	sc, _ := newScenarioContext(t)
	rel := "global.yaml.gotmpl"
	abs := filepath.Join(sc.Suite.Config.RepoRoot, rel)
	original := "before\nold one\nold two\nafter\n"
	if err := os.WriteFile(abs, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	doc := &godog.DocString{Content: "old one\nold two\n---\nnew one\nnew two"}

	if err := sc.iSubstituteBlock(rel, doc); err != nil {
		t.Fatalf("substitute block: %v", err)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "before\nnew one\nnew two\nafter\n" {
		t.Fatalf("body = %q", got)
	}

	if err := os.WriteFile(abs, []byte("mutated\n"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := sc.Suite.Ledger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err = os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(got) != original {
		t.Fatalf("restored body = %q", got)
	}
}

func TestEnvironmentVariableIsSet(t *testing.T) {
	sc, _ := newScenarioContext(t)
	t.Setenv("BDD_TMP_TEST_FOO", "bar")
	if err := sc.environmentVariableIsSet("BDD_TMP_TEST_FOO"); err != nil {
		t.Fatalf("present: %v", err)
	}
	if err := sc.environmentVariableIsSet("BDD_TMP_TEST_UNSET"); err == nil {
		t.Fatal("expected error for unset var")
	}
}

func TestEnvironmentVariablesAreSet(t *testing.T) {
	sc, _ := newScenarioContext(t)
	t.Setenv("BDD_TMP_REQUIRED_ONE", "one")
	t.Setenv("BDD_TMP_REQUIRED_TWO", "two")
	table := docTable(t, [][]string{
		{"name"},
		{"BDD_TMP_REQUIRED_ONE"},
		{"BDD_TMP_REQUIRED_TWO"},
	})
	if err := sc.environmentVariablesAreSet(table); err != nil {
		t.Fatalf("require variables: %v", err)
	}
}

func TestEnvironmentVariablesAreSetReportsMissingVariable(t *testing.T) {
	sc, _ := newScenarioContext(t)
	t.Setenv("BDD_TMP_REQUIRED_PRESENT", "present")
	t.Setenv("BDD_TMP_REQUIRED_MISSING", "")
	table := docTable(t, [][]string{
		{"name"},
		{"BDD_TMP_REQUIRED_PRESENT"},
		{"BDD_TMP_REQUIRED_MISSING"},
	})
	err := sc.environmentVariablesAreSet(table)
	if err == nil {
		t.Fatal("expected missing-variable error")
	}
	if !strings.Contains(err.Error(), `"BDD_TMP_REQUIRED_MISSING"`) {
		t.Fatalf("error %q does not name the missing variable", err)
	}
}

func TestEnvironmentVariablesAreSetRejectsInvalidTable(t *testing.T) {
	sc, _ := newScenarioContext(t)
	for _, table := range []*godog.Table{
		docTable(t, [][]string{{"variable"}, {"BDD_TMP_REQUIRED"}}),
		docTable(t, [][]string{{"name"}, {""}}),
	} {
		if err := sc.environmentVariablesAreSet(table); err == nil {
			t.Fatal("expected invalid-table error")
		}
	}
}

func TestCommandHasSucceededCachesResolved(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("EXAMPLE_VAR", "value")
	doc := &godog.DocString{Content: "make example VAR=${EXAMPLE_VAR}"}
	if err := sc.commandHasSucceededDoc(context.Background(), doc); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := sc.commandHasSucceededDoc(context.Background(), doc); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d, want 1 (cache should suppress the second call)", len(fake.runs))
	}
	if fake.runs[0].command != "make example VAR=value" {
		t.Fatalf("recorded command = %q, want resolved", fake.runs[0].command)
	}
}

func TestCommandHasSucceededMissesOnDifferentResolvedText(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("EXAMPLE_VAR", "one")
	doc := &godog.DocString{Content: "make example VAR=${EXAMPLE_VAR}"}
	if err := sc.commandHasSucceededDoc(context.Background(), doc); err != nil {
		t.Fatalf("first: %v", err)
	}
	t.Setenv("EXAMPLE_VAR", "two")
	if err := sc.commandHasSucceededDoc(context.Background(), doc); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(fake.runs) != 2 {
		t.Fatalf("runs = %d, want 2 (different resolved commands should miss the cache)", len(fake.runs))
	}
}

func TestIRunCommandRecordsResult(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: "ok"}
	if err := sc.iRunCommandLine(context.Background(), "echo ok"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if sc.LastResult.Stdout != "ok" {
		t.Fatalf("last stdout = %q", sc.LastResult.Stdout)
	}
}

func TestISuccessfullyRunCommandLineRecordsResultAndCaches(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("EXAMPLE_VAR", "value")
	fake.result = harness.Result{ExitCode: 0, Stdout: "ok"}
	if err := sc.iSuccessfullyRunCommandLine(context.Background(), "echo ${EXAMPLE_VAR}"); err != nil {
		t.Fatalf("run successfully: %v", err)
	}
	if sc.LastResult.Stdout != "ok" {
		t.Fatalf("last stdout = %q", sc.LastResult.Stdout)
	}
	if sc.LastCommand != "echo value" {
		t.Fatalf("last command = %q, want resolved command", sc.LastCommand)
	}
	if !sc.Suite.Cache.Has("echo value") {
		t.Fatal("successful command was not recorded in cache")
	}
}

func TestISuccessfullyRunCommandDocPreservesOutput(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: "rendered output"}
	doc := &godog.DocString{Content: "make template"}
	if err := sc.iSuccessfullyRunCommandDoc(context.Background(), doc); err != nil {
		t.Fatalf("run successfully: %v", err)
	}
	if err := sc.commandOutputShouldContain("rendered output"); err != nil {
		t.Fatalf("assert preserved output: %v", err)
	}
}

func TestISuccessfullyRunCommandRejectsNonZeroExit(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 2, Stderr: "failed"}
	fake.err = errors.New("command failed: exit status 2")
	err := sc.iSuccessfullyRunCommandLine(context.Background(), "make install")
	if err == nil {
		t.Fatal("expected non-zero command to fail the step")
	}
	if sc.LastResult.ExitCode != 2 {
		t.Fatalf("last exit code = %d, want 2", sc.LastResult.ExitCode)
	}
	if sc.Suite.Cache.Has("make install") {
		t.Fatal("failed command was recorded in cache")
	}
}

// TestIRunCommandWithTTYDocRecordsResult verifies the pty-backed run form
// wires through RunWithTTY and records the result like the plain docstring
// form. The fake runner ignores the TTY and resolves identically.
func TestIRunCommandWithTTYDocRecordsResult(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: "up ok"}
	doc := &godog.DocString{Content: "nvcf-cli self-hosted --env local up --cluster-name ncp-local"}
	if err := sc.iRunCommandWithTTYDoc(context.Background(), doc); err != nil {
		t.Fatalf("run with terminal: %v", err)
	}
	if sc.LastResult.Stdout != "up ok" {
		t.Fatalf("last stdout = %q", sc.LastResult.Stdout)
	}
	if len(fake.runs) != 1 || fake.runs[0].command != "nvcf-cli self-hosted --env local up --cluster-name ncp-local" {
		t.Fatalf("recorded runs = %+v", fake.runs)
	}
}

// TestIRunCommandSurfacesRunnerErrorWhenProcessDidNotExec covers the
// phantom-success case: when the runner returns an error AND the
// recorded ExitCode is non-positive (parse failure, empty command,
// "did not run" classes), the step must fail rather than silently
// record a zero-exit result that the next "should be 0" assertion
// would happily accept.
func TestIRunCommandSurfacesRunnerErrorWhenProcessDidNotExec(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0}
	fake.err = errors.New("parse command: bad quoting")
	err := sc.iRunCommandLine(context.Background(), "echo 'unterminated")
	if err == nil {
		t.Fatal("expected step error when runner reports a non-exec failure")
	}
	if !strings.Contains(err.Error(), "command did not execute") {
		t.Fatalf("error message %q does not name the failure mode", err.Error())
	}
}

// TestIRunCommandLeavesNonZeroExitForAssertion covers the opposite
// case: when the runner reports a real non-zero exit (positive
// ExitCode plus a wrapping error), runAndRecord must NOT propagate
// the error so the explicit `the command exit code should be N`
// assertion in the feature file can inspect ExitCode. The conflict
// precheck pattern relies on this (asserts `should be 1` against a
// `k3d cluster get` that exits 1 by design).
func TestIRunCommandLeavesNonZeroExitForAssertion(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 1, Stderr: "not found"}
	fake.err = errors.New("command failed: exit status 1")
	if err := sc.iRunCommandLine(context.Background(), "k3d cluster get ncp-local-cp"); err != nil {
		t.Fatalf("non-zero exit should not fail the step: %v", err)
	}
	if sc.LastResult.ExitCode != 1 {
		t.Fatalf("LastResult.ExitCode = %d, want 1", sc.LastResult.ExitCode)
	}
}

// TestCachedRunSetsLastResultOnCacheHit covers the cache-hit branch
// of cachedRun: a `the command exit code should be 0` assertion
// running immediately after a cached Given must observe a synthetic
// success rather than the stale LastResult from whatever ran
// earlier in the scenario.
func TestCachedRunSetsLastResultOnCacheHit(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0}
	doc := &godog.DocString{Content: "make install HELMFILE_ENV=local"}
	if err := sc.commandHasSucceededDoc(context.Background(), doc); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Simulate scenario state from a later step that recorded a
	// non-zero exit; the cached Given must overwrite it.
	sc.LastResult = harness.Result{ExitCode: 7, Stderr: "stale"}
	if err := sc.commandHasSucceededDoc(context.Background(), doc); err != nil {
		t.Fatalf("second run (cached): %v", err)
	}
	if sc.LastResult.ExitCode != 0 {
		t.Fatalf("LastResult.ExitCode = %d, want 0 (cached Given should overwrite stale state)", sc.LastResult.ExitCode)
	}
}

// TestExitZeroAssertionSeedsCacheFromWhenRun covers the bug where
// `When I run command + Then the command exit code should be 0` did
// not seed the suite-level CommandCache. Only `Given command has
// succeeded:` recorded. A later `Given command has succeeded:` for
// the same resolved command then missed the cache and reran a
// potentially destructive install. The assertion-driven record-on-
// success path closes the gap.
func TestExitZeroAssertionSeedsCacheFromWhenRun(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: "ok"}
	cmd := "make install HELMFILE_ENV=local-bdd"

	if err := sc.iRunCommandLine(context.Background(), cmd); err != nil {
		t.Fatalf("when run: %v", err)
	}
	if err := sc.commandExitCodeShouldBe(0); err != nil {
		t.Fatalf("then exit 0: %v", err)
	}

	// Simulate the Before hook between scenarios; the per-scenario
	// state resets but the suite-level cache must persist.
	sc.LastResult = harness.Result{}
	sc.LastErr = nil
	sc.LastCommand = ""

	doc := &godog.DocString{Content: cmd}
	if err := sc.commandHasSucceededDoc(context.Background(), doc); err != nil {
		t.Fatalf("given (should hit cache): %v", err)
	}

	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d, want 1 (When+Then should seed the cache; the later Given must not rerun)", len(fake.runs))
	}
}

// TestExitNonZeroAssertionDoesNotSeedCache confirms that negative
// prechecks ("the command exit code should be 1" against a missing
// k3d cluster, for example) do not seed the cache. Recording them
// would let a later "Given command has succeeded:" for the same
// command succeed without ever running it, which silently breaks
// the conflict precheck pattern the BDD relies on.
func TestExitNonZeroAssertionDoesNotSeedCache(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 1, Stderr: "not found"}
	fake.err = errors.New("exit 1")
	cmd := "k3d cluster get ncp-local-cp"

	if err := sc.iRunCommandLine(context.Background(), cmd); err != nil {
		t.Fatalf("when run: %v", err)
	}
	if err := sc.commandExitCodeShouldBe(1); err != nil {
		t.Fatalf("then exit 1: %v", err)
	}

	if sc.Suite.Cache.Has(cmd) {
		t.Fatal("cache should not contain a negative-precheck command (expected==1 must not record)")
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d, want 1 (initial run only)", len(fake.runs))
	}
}

func TestCommandExitCodeAssertion(t *testing.T) {
	sc, _ := newScenarioContext(t)
	sc.LastResult = harness.Result{ExitCode: 0}
	if err := sc.commandExitCodeShouldBe(0); err != nil {
		t.Fatalf("expected zero: %v", err)
	}
	sc.LastResult = harness.Result{ExitCode: 7, Stderr: "oops"}
	if err := sc.commandExitCodeShouldBe(0); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestIExportCommandOutputToEnvHappyPath(t *testing.T) {
	const name = "BDD_TEST_EXPORT_HAPPY"
	t.Setenv(name, "original")

	sc, _ := newScenarioContext(t)
	if err := sc.Suite.EnvLedger.Snapshot(name); err != nil {
		t.Fatalf("pre-snapshot: %v", err)
	}
	sc.LastResult = harness.Result{ExitCode: 0, Stdout: "  exported-value\n"}
	if err := sc.iExportCommandOutputToEnv(context.Background(), name); err != nil {
		t.Fatalf("export: %v", err)
	}
	if got := os.Getenv(name); got != "exported-value" {
		t.Fatalf("got %q, want exported-value", got)
	}
	// Suite.Teardown is not called by unit tests; restore manually so
	// this test does not leak the value to the next test.
	if err := sc.Suite.EnvLedger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := os.Getenv(name); got != "original" {
		t.Fatalf("post-restore got %q, want original", got)
	}
}

func TestIExportCommandOutputToEnvRejectsNonZeroExit(t *testing.T) {
	sc, _ := newScenarioContext(t)
	sc.LastResult = harness.Result{ExitCode: 1, Stdout: "fake-value"}
	err := sc.iExportCommandOutputToEnv(context.Background(), "BDD_TEST_EXPORT_NONZERO")
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
	if !strings.Contains(err.Error(), "exited 1") {
		t.Fatalf("error did not name exit code: %v", err)
	}
}

func TestIExportCommandOutputToEnvRejectsEmptyStdout(t *testing.T) {
	sc, _ := newScenarioContext(t)
	sc.LastResult = harness.Result{ExitCode: 0, Stdout: "   \n\t"}
	err := sc.iExportCommandOutputToEnv(context.Background(), "BDD_TEST_EXPORT_EMPTY")
	if err == nil {
		t.Fatal("expected error for empty stdout, got nil")
	}
	if !strings.Contains(err.Error(), "empty stdout") {
		t.Fatalf("error did not name empty stdout: %v", err)
	}
}

func TestIExportCommandOutputToEnvRejectsEmptyName(t *testing.T) {
	sc, _ := newScenarioContext(t)
	sc.LastResult = harness.Result{ExitCode: 0, Stdout: "value"}
	err := sc.iExportCommandOutputToEnv(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}

func TestMultiClusterTableRejectsControlPlane(t *testing.T) {
	sc, _ := newScenarioContext(t)
	table := singleColumnTable(t, []string{"ncp-local-cp"})
	if err := sc.multiClusterComputeRunning(context.Background(), table); err == nil {
		t.Fatal("expected rejection for control plane row")
	}
}

func TestMultiClusterTableRejectsTypo(t *testing.T) {
	sc, _ := newScenarioContext(t)
	table := singleColumnTable(t, []string{"ncp-local-comput-1"})
	if err := sc.multiClusterComputeRunning(context.Background(), table); err == nil {
		t.Fatal("expected rejection for malformed row")
	}
}

func TestMultiClusterTableMaps2RowsToCount2(t *testing.T) {
	sc, fake := newScenarioContext(t)
	table := singleColumnTable(t, []string{"ncp-local-compute-1", "ncp-local-compute-2"})
	if err := sc.multiClusterComputeRunning(context.Background(), table); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(fake.runs))
	}
	if !strings.Contains(fake.runs[0].command, "COMPUTE_CLUSTER_COUNT=2") {
		t.Fatalf("command = %q, want COMPUTE_CLUSTER_COUNT=2", fake.runs[0].command)
	}
}

func TestSingleClusterBootstrapCachesAcrossCalls(t *testing.T) {
	sc, fake := newScenarioContext(t)
	if err := sc.singleClusterIsRunning(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := sc.singleClusterIsRunning(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d, want 1 (cache should suppress the second call)", len(fake.runs))
	}
}

func TestHelmRegistryAuthenticationUsesSensitiveStdinAndCaches(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NGC_API_KEY", "super-secret-token")
	t.Setenv("BDD_TMP_REGISTRY", "nvcr.io")

	for i := 0; i < 2; i++ {
		if err := sc.helmIsAuthenticatedToOCIRegistry(context.Background(), "${BDD_TMP_REGISTRY}"); err != nil {
			t.Fatalf("authenticate call %d: %v", i+1, err)
		}
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(fake.runs))
	}
	wantCommand := "helm registry login nvcr.io --username '$oauthtoken' --password-stdin"
	if fake.runs[0].command != wantCommand {
		t.Fatalf("command = %q, want %q", fake.runs[0].command, wantCommand)
	}
	if fake.runs[0].sensitiveStdin != "super-secret-token" {
		t.Fatal("NGC API key was not supplied through sensitive stdin")
	}
	if strings.Contains(fake.runs[0].command, "super-secret-token") {
		t.Fatalf("NGC API key leaked into command: %q", fake.runs[0].command)
	}
	if sc.LastResult.ExitCode != 0 {
		t.Fatalf("cached result exit code = %d, want 0", sc.LastResult.ExitCode)
	}
}

func TestHelmRegistryAuthenticationRedactsFailure(t *testing.T) {
	sc, fake := newScenarioContext(t)
	const apiKey = "super-secret-token"
	t.Setenv("NGC_API_KEY", apiKey)
	fake.result = harness.Result{ExitCode: 1, Stderr: "unauthorized: " + apiKey}
	fake.err = errors.New("login failed for " + apiKey)

	err := sc.helmIsAuthenticatedToOCIRegistry(context.Background(), "nvcr.io")
	if err == nil {
		t.Fatal("expected authentication failure")
	}
	for label, value := range map[string]string{
		"returned error": err.Error(),
		"LastErr":        sc.LastErr.Error(),
		"stderr":         sc.LastResult.Stderr,
	} {
		if strings.Contains(value, apiKey) {
			t.Fatalf("%s leaked NGC API key: %q", label, value)
		}
	}
	if !strings.Contains(err.Error(), "exit code 1") || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("error lacks useful diagnostics: %v", err)
	}
}

func TestHelmRegistryAuthenticationRequiresAPIKey(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NGC_API_KEY", "")
	if err := sc.helmIsAuthenticatedToOCIRegistry(context.Background(), "nvcr.io"); err == nil {
		t.Fatal("expected error when NGC_API_KEY is unset")
	}
	if len(fake.runs) != 0 {
		t.Fatalf("runs = %d, want 0", len(fake.runs))
	}
}

func TestKubernetesResourcesShouldExistRunsExplicitGets(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0}
	table := docTable(t, [][]string{
		{"kind", "name"},
		{"ServiceMonitor", "nvcf-default-monitors-state-metrics"},
		{"PodMonitor", "nvcf-default-monitors-worker"},
	})

	if err := sc.kubernetesResourcesShouldExist(context.Background(), "monitoring", "k3d-ncp-local", table); err != nil {
		t.Fatalf("assert Kubernetes resources: %v", err)
	}
	if len(fake.runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(fake.runs))
	}
	want := []string{
		"kubectl get servicemonitor/nvcf-default-monitors-state-metrics --namespace monitoring --context k3d-ncp-local -o name",
		"kubectl get podmonitor/nvcf-default-monitors-worker --namespace monitoring --context k3d-ncp-local -o name",
	}
	for index, run := range fake.runs {
		if run.command != want[index] {
			t.Fatalf("command %d = %q, want %q", index+1, run.command, want[index])
		}
	}
}

func TestKubernetesResourcesShouldExistNamesFailingRow(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 1}
	table := docTable(t, [][]string{
		{"kind", "name"},
		{"ServiceMonitor", "missing-monitor"},
	})

	err := sc.kubernetesResourcesShouldExist(context.Background(), "monitoring", "k3d-ncp-local", table)
	if err == nil || !strings.Contains(err.Error(), "row 1") || !strings.Contains(err.Error(), "ServiceMonitor/missing-monitor should exist") {
		t.Fatalf("error = %v", err)
	}
}

func TestKubernetesResourcesShouldNotExistUsesIgnoreNotFound(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0}
	table := docTable(t, [][]string{
		{"kind", "name"},
		{"Secret", "nvcr-pull-secret"},
	})

	if err := sc.kubernetesResourcesShouldNotExist(context.Background(), "nvca-system", "k3d-ncp-local", table); err != nil {
		t.Fatalf("assert Kubernetes resource absence: %v", err)
	}
	want := "kubectl get secret/nvcr-pull-secret --namespace nvca-system --context k3d-ncp-local --ignore-not-found -o name"
	if len(fake.runs) != 1 || fake.runs[0].command != want {
		t.Fatalf("runs = %#v, want %q", fake.runs, want)
	}
}

func TestKubernetesResourcesShouldNotExistNamesExistingResource(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: "secret/nvcr-pull-secret\n"}
	table := docTable(t, [][]string{
		{"kind", "name"},
		{"Secret", "nvcr-pull-secret"},
	})

	err := sc.kubernetesResourcesShouldNotExist(context.Background(), "nvca-system", "k3d-ncp-local", table)
	if err == nil || !strings.Contains(err.Error(), "row 1") || !strings.Contains(err.Error(), "Secret/nvcr-pull-secret exists") {
		t.Fatalf("error = %v", err)
	}
}

func TestKubernetesResourceTableRejectsEmptyFields(t *testing.T) {
	for _, row := range [][]string{{"", "name"}, {"Secret", ""}} {
		table := docTable(t, [][]string{{"kind", "name"}, row})
		if _, err := tableToKubernetesResources(table); err == nil {
			t.Fatalf("expected validation error for row %#v", row)
		}
	}
}

func TestKubernetesResourcesValidateAllRowsBeforeRunning(t *testing.T) {
	sc, fake := newScenarioContext(t)
	table := docTable(t, [][]string{
		{"kind", "name"},
		{"Secret", "valid-secret"},
		{"PodMonitor", ""},
	})

	if err := sc.kubernetesResourcesShouldExist(context.Background(), "monitoring", "k3d-ncp-local", table); err == nil {
		t.Fatal("expected validation error")
	}
	if len(fake.runs) != 0 {
		t.Fatalf("runs = %d, want 0 before all rows validate", len(fake.runs))
	}
}

func TestKubernetesResourceShouldContainRunsExplicitYAMLGet(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: `spec:
  targetAllocator:
    enabled: true
`}
	doc := &godog.DocString{Content: `spec:
  targetAllocator:
    enabled: true
`}

	if err := sc.kubernetesResourceShouldContain(
		context.Background(),
		"OpenTelemetryCollector",
		"nvcf-observability",
		"monitoring",
		"k3d-ncp-local",
		doc,
	); err != nil {
		t.Fatalf("assert Kubernetes resource YAML: %v", err)
	}
	want := "kubectl get opentelemetrycollector/nvcf-observability --namespace monitoring --context k3d-ncp-local -o yaml"
	if len(fake.runs) != 1 || fake.runs[0].command != want {
		t.Fatalf("runs = %#v, want %q", fake.runs, want)
	}
}

func TestDeploymentShouldCompleteRolloutRunsExplicitWait(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0}

	if err := sc.deploymentShouldCompleteRollout(context.Background(), "nvca-operator", "nvca-operator", "k3d-ncp-local", "10m"); err != nil {
		t.Fatalf("wait for deployment rollout: %v", err)
	}
	want := "kubectl rollout status deployment/nvca-operator -n nvca-operator --context k3d-ncp-local --timeout=10m"
	if len(fake.runs) != 1 || fake.runs[0].command != want {
		t.Fatalf("runs = %#v, want %q", fake.runs, want)
	}
}

func TestDNSNameShouldResolveRunsExplicitWait(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0}
	t.Setenv("BDD_DNS_NAME", "api.192-0-2-10.nip.io")
	t.Setenv("BDD_DNS_TIMEOUT", "180")

	if err := sc.dnsNameShouldResolve(context.Background(), "${BDD_DNS_NAME}", "${BDD_DNS_TIMEOUT}"); err != nil {
		t.Fatalf("wait for DNS resolution: %v", err)
	}
	want := "tests/bdd/scripts/wait-for-dns.sh api.192-0-2-10.nip.io 180"
	if len(fake.runs) != 1 || fake.runs[0].command != want {
		t.Fatalf("runs = %#v, want %q", fake.runs, want)
	}
}

func TestDNSNameShouldResolveRejectsInvalidInputsBeforeRunning(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		timeout  string
		want     string
	}{
		{
			name:     "empty hostname",
			hostname: " ",
			timeout:  "180",
			want:     "DNS name is empty",
		},
		{
			name:     "invalid timeout",
			hostname: "gateway.example.com",
			timeout:  "-1",
			want:     "not a non-negative integer",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sc, fake := newScenarioContext(t)
			err := sc.dnsNameShouldResolve(context.Background(), tc.hostname, tc.timeout)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want error containing %q", err, tc.want)
			}
			if len(fake.runs) != 0 {
				t.Fatalf("runs = %d, want 0", len(fake.runs))
			}
		})
	}
}

func TestDNSNameShouldResolveFailureNamesTargetWithoutResolverOutput(t *testing.T) {
	sc, fake := newScenarioContext(t)
	secretOutput := "unrelated-resolver-output"
	fake.result = harness.Result{ExitCode: 2, Stdout: secretOutput, Stderr: secretOutput}

	err := sc.dnsNameShouldResolve(context.Background(), "gateway.example.com", "180")
	if err == nil {
		t.Fatal("expected DNS resolution failure")
	}
	for _, want := range []string{`DNS name "gateway.example.com"`, "within 180 seconds"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
	if strings.Contains(err.Error(), secretOutput) {
		t.Fatalf("error leaked resolver output: %v", err)
	}
}

func TestKubernetesResourceShouldContainFailureDoesNotExposeResourceValues(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: `data:
  token: actual-secret-value
`}
	doc := &godog.DocString{Content: `data:
  token: expected-secret-value
`}

	err := sc.kubernetesResourceShouldContain(
		context.Background(),
		"Secret",
		"credentials",
		"nvcf",
		"k3d-ncp-local",
		doc,
	)
	if err == nil {
		t.Fatal("expected mismatch")
	}
	for _, sensitive := range []string{"actual-secret-value", "expected-secret-value"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error %q exposes %q", err, sensitive)
		}
	}
}

func TestNVCFBackendShouldReportAgentStatusRunsExplicitWait(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0}

	if err := sc.nvcfBackendShouldReportAgentStatus(context.Background(), "ncp-local", "nvca-operator", "k3d-ncp-local", "healthy", "10m"); err != nil {
		t.Fatalf("wait for NVCFBackend status: %v", err)
	}
	want := "kubectl wait nvcfbackend ncp-local -n nvca-operator --context k3d-ncp-local --for=jsonpath={.status.agentStatus}=healthy --timeout=10m"
	if len(fake.runs) != 1 || fake.runs[0].command != want {
		t.Fatalf("runs = %#v, want %q", fake.runs, want)
	}
}

func TestGatewayAPIRoutesShouldBeAcceptedAndResolvedRunsExplicitWaits(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0}
	t.Setenv("BDD_ROUTE_NAMESPACE", "nvcf")
	table := docTable(t, [][]string{
		{"kind", "name", "namespace", "parent"},
		{"HTTPRoute", "nvcf-api-control-plane", "${BDD_ROUTE_NAMESPACE}", "shared-gw"},
		{"GRPCRoute", "nvcf-api-control-plane-grpc", "nvcf", "api-grpc-gw"},
	})

	if err := sc.gatewayAPIRoutesShouldBeAcceptedAndResolved(context.Background(), "k3d-ncp-local-cp", "2m", table); err != nil {
		t.Fatalf("wait for Gateway API routes: %v", err)
	}
	want := []string{
		`kubectl wait httproute/nvcf-api-control-plane -n nvcf --context k3d-ncp-local-cp '--for=jsonpath={.status.parents[?(@.parentRef.name=="shared-gw")].conditions[?(@.type=="Accepted")].status}=True' --timeout=2m`,
		`kubectl wait grpcroute/nvcf-api-control-plane-grpc -n nvcf --context k3d-ncp-local-cp '--for=jsonpath={.status.parents[?(@.parentRef.name=="api-grpc-gw")].conditions[?(@.type=="Accepted")].status}=True' --timeout=2m`,
		`kubectl wait httproute/nvcf-api-control-plane -n nvcf --context k3d-ncp-local-cp '--for=jsonpath={.status.parents[?(@.parentRef.name=="shared-gw")].conditions[?(@.type=="ResolvedRefs")].status}=True' --timeout=2m`,
		`kubectl wait grpcroute/nvcf-api-control-plane-grpc -n nvcf --context k3d-ncp-local-cp '--for=jsonpath={.status.parents[?(@.parentRef.name=="api-grpc-gw")].conditions[?(@.type=="ResolvedRefs")].status}=True' --timeout=2m`,
	}
	if len(fake.runs) != len(want) {
		t.Fatalf("runs = %d, want %d", len(fake.runs), len(want))
	}
	for index, run := range fake.runs {
		if run.command != want[index] {
			t.Fatalf("command %d = %q, want %q", index+1, run.command, want[index])
		}
	}
}

func TestGatewayAPIRoutesShouldBeAcceptedAndResolvedNamesFailingRowAndCondition(t *testing.T) {
	sc, fake := newScenarioContext(t)
	secretOutput := "route-resource-secret-value"
	fake.runResults = []harness.Result{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 1, Stdout: secretOutput, Stderr: secretOutput},
	}
	table := docTable(t, [][]string{
		{"kind", "name", "namespace", "parent"},
		{"HTTPRoute", "nvcf-api-control-plane", "nvcf", "shared-gw"},
		{"GRPCRoute", "nvcf-api-control-plane-grpc", "nvcf", "api-grpc-gw"},
	})

	err := sc.gatewayAPIRoutesShouldBeAcceptedAndResolved(context.Background(), "k3d-ncp-local-cp", "2m", table)
	if err == nil {
		t.Fatal("expected route readiness failure")
	}
	for _, want := range []string{"row 2", "GRPCRoute/nvcf-api-control-plane-grpc", `namespace "nvcf"`, `parent "api-grpc-gw"`, `condition "ResolvedRefs"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
	if strings.Contains(err.Error(), secretOutput) {
		t.Fatalf("error leaked command output: %v", err)
	}
}

func TestGatewayAPIRouteTableRejectsEmptyFieldsBeforeRunning(t *testing.T) {
	for _, row := range [][]string{{"", "route", "nvcf", "gateway"}, {"HTTPRoute", "", "nvcf", "gateway"}, {"HTTPRoute", "route", "", "gateway"}, {"HTTPRoute", "route", "nvcf", ""}} {
		sc, fake := newScenarioContext(t)
		table := docTable(t, [][]string{{"kind", "name", "namespace", "parent"}, {"HTTPRoute", "valid", "nvcf", "gateway"}, row})
		if err := sc.gatewayAPIRoutesShouldBeAcceptedAndResolved(context.Background(), "k3d-ncp-local-cp", "2m", table); err == nil {
			t.Fatalf("expected validation error for row %#v", row)
		}
		if len(fake.runs) != 0 {
			t.Fatalf("runs = %d, want 0 before all rows validate", len(fake.runs))
		}
	}
}

func TestKubernetesReadinessFailuresNameTargetWithoutCommandOutput(t *testing.T) {
	secretOutput := "registry-token-value"
	for _, test := range []struct {
		name string
		run  func(*ScenarioContext) error
		want string
	}{
		{
			name: "deployment",
			run: func(sc *ScenarioContext) error {
				return sc.deploymentShouldCompleteRollout(context.Background(), "nvca-operator", "nvca-operator", "k3d-ncp-local", "10m")
			},
			want: "deployment \"nvca-operator\" did not complete rollout",
		},
		{
			name: "backend",
			run: func(sc *ScenarioContext) error {
				return sc.nvcfBackendShouldReportAgentStatus(context.Background(), "ncp-local", "nvca-operator", "k3d-ncp-local", "healthy", "10m")
			},
			want: "NVCFBackend \"ncp-local\" did not report agent status \"healthy\"",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sc, fake := newScenarioContext(t)
			fake.result = harness.Result{ExitCode: 1, Stdout: secretOutput, Stderr: secretOutput}
			err := test.run(sc)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want target context", err)
			}
			if strings.Contains(err.Error(), secretOutput) {
				t.Fatalf("error leaked command output: %v", err)
			}
		})
	}
}

func TestHelmReleasesShouldBeDeployedRunsSingleExplicitList(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: `[{"name":"nats","namespace":"nats-system","revision":"1","status":"deployed"}]`}
	table := docTable(t, [][]string{
		{"name", "namespace", "revision"},
		{"nats", "nats-system", "1"},
	})

	if err := sc.helmReleasesShouldBeDeployed(context.Background(), "k3d-ncp-local", table); err != nil {
		t.Fatalf("assert Helm releases: %v", err)
	}
	if len(fake.runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(fake.runs))
	}
	want := "helm list --all-namespaces --kube-context k3d-ncp-local -o json"
	if fake.runs[0].command != want {
		t.Fatalf("command = %q, want %q", fake.runs[0].command, want)
	}
}

func TestHelmReleaseShouldContainValuesRunsExplicitYAMLGet(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("BDD_TMP_RELEASE", "nvca-operator")
	fake.result = harness.Result{ExitCode: 0, Stdout: `selfManaged:
  otelCollector:
    enabled: true
    imageTag: 0.157.9
`}
	doc := &godog.DocString{Content: `selfManaged:
  otelCollector:
    enabled: true
`}

	if err := sc.helmReleaseShouldContainValues(
		context.Background(),
		"${BDD_TMP_RELEASE}",
		"nvca-operator",
		"k3d-ncp-local",
		doc,
	); err != nil {
		t.Fatalf("assert Helm release values: %v", err)
	}
	want := "helm get values nvca-operator --namespace nvca-operator --kube-context k3d-ncp-local -o yaml"
	if len(fake.runs) != 1 || fake.runs[0].command != want {
		t.Fatalf("runs = %#v, want %q", fake.runs, want)
	}
}

func TestHelmReleaseShouldContainValuesFailureDoesNotExposeValues(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: `selfManaged:
  registryCredential: actual-secret-value
`}
	doc := &godog.DocString{Content: `selfManaged:
  registryCredential: expected-secret-value
`}

	err := sc.helmReleaseShouldContainValues(
		context.Background(),
		"nvca-operator",
		"nvca-operator",
		"k3d-ncp-local",
		doc,
	)
	if err == nil {
		t.Fatal("expected mismatch")
	}
	for _, want := range []string{`helm release "nvca-operator"`, "selfManaged.registryCredential"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
	for _, sensitive := range []string{"actual-secret-value", "expected-secret-value"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error %q exposes %q", err, sensitive)
		}
	}
}

func TestHelmReleaseTableAcceptsNameAndNamespace(t *testing.T) {
	table := docTable(t, [][]string{
		{"name", "namespace"},
		{"nats", "nats-system"},
	})

	got, err := tableToHelmReleaseExpectations(table)
	if err != nil {
		t.Fatalf("parse table: %v", err)
	}
	if len(got) != 1 || got[0].Name != "nats" || got[0].Namespace != "nats-system" || got[0].Revision != "" {
		t.Fatalf("expectations = %#v", got)
	}
}

func TestRegisterAllRunsAFeatureFile(t *testing.T) {
	// End-to-end smoke check that RegisterAll wires every category. A
	// minimal in-memory feature is driven through a Godog TestSuite so
	// the regex registrations and the Before hook are both exercised.
	feature := `Feature: Smoke
  Scenario: register-all smoke
    Given these environment variables are set:
      | name          |
      | BDD_TMP_SMOKE |
    When I successfully run command "echo smoke"
    And I successfully run command:
      """
      echo documented
      """
    Then the command output should contain "documented"
`
	t.Setenv("BDD_TMP_SMOKE", "ready")
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: "smoke documented\n"}

	suite := godog.TestSuite{
		Name: "smoke",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			RegisterAll(ctx, sc)
		},
		Options: &godog.Options{
			Format: "progress",
			FeatureContents: []godog.Feature{
				{Name: "smoke.feature", Contents: []byte(feature)},
			},
			Strict: true,
			Output: io.Discard,
		},
	}
	if status := suite.Run(); status != 0 {
		t.Fatalf("suite status = %d", status)
	}
}

func TestPullSecretInNamespacesKeepsAPIKeyOutOfArgv(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NGC_API_KEY", "super-secret-token")
	table := singleColumnTable(t, []string{"nvcf", "nvca-operator"})
	if err := sc.pullSecretInNamespaces(context.Background(), "nvcr-pull-secret", table); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(fake.runs) != 4 {
		t.Fatalf("runs = %d, want 4 (2 namespaces x (ns manifest + secret manifest))", len(fake.runs))
	}
	for _, run := range fake.runs {
		if strings.Contains(run.command, "super-secret-token") {
			t.Fatalf("api key leaked into argv: %q", run.command)
		}
	}
}

func TestPullSecretInNamespacesUsingContextTargetsEveryApply(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("NGC_API_KEY", "super-secret-token")
	table := singleColumnTable(t, []string{"monitoring", "nvca-operator"})
	if err := sc.pullSecretInNamespacesUsingContext(
		context.Background(),
		"nvcr-pull-secret",
		"k3d-ncp-local-compute-1",
		table,
	); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(fake.runs) != 4 {
		t.Fatalf("runs = %d, want 4 (2 namespaces x (ns manifest + secret manifest))", len(fake.runs))
	}
	for _, run := range fake.runs {
		if !strings.HasPrefix(run.command, "kubectl --context k3d-ncp-local-compute-1 apply -f ") {
			t.Fatalf("command does not target compute context: %q", run.command)
		}
		if strings.Contains(run.command, "super-secret-token") {
			t.Fatalf("api key leaked into argv: %q", run.command)
		}
	}
}

func TestPullSecretInNamespacesRequiresAPIKey(t *testing.T) {
	sc, _ := newScenarioContext(t)
	t.Setenv("NGC_API_KEY", "")
	table := singleColumnTable(t, []string{"nvcf"})
	if err := sc.pullSecretInNamespaces(context.Background(), "x", table); err == nil {
		t.Fatal("expected error when NGC_API_KEY is unset")
	}
}

func TestYAMLAssertionStepsReadFiles(t *testing.T) {
	sc, _ := newScenarioContext(t)
	rel := "profile.yaml"
	abs := filepath.Join(sc.Suite.Config.RepoRoot, rel)
	if err := os.WriteFile(abs, []byte(`apiVersion: v1
controlPlane:
  clusterName: ncp-local
  endpoints:
    inCluster:
      icmsURL: http://api.sis:8080
`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := sc.yamlFileKeyShouldEqual(rel, "controlPlane.clusterName", "ncp-local"); err != nil {
		t.Fatalf("equal: %v", err)
	}
	if err := sc.yamlFileKeyShouldNotBeEmpty(rel, "controlPlane.clusterName"); err != nil {
		t.Fatalf("not empty: %v", err)
	}
	nonEmptyKeys := docTable(t, [][]string{
		{"key"},
		{"controlPlane.clusterName"},
		{"controlPlane.endpoints.inCluster.icmsURL"},
	})
	if err := sc.yamlFileShouldHaveNonEmptyKeys(rel, nonEmptyKeys); err != nil {
		t.Fatalf("non-empty keys: %v", err)
	}
	if err := sc.yamlFileKeyShouldContain(rel, "controlPlane.endpoints.inCluster", &godog.DocString{Content: "icmsURL: http://api.sis:8080\n"}); err != nil {
		t.Fatalf("contain: %v", err)
	}
}

func TestYAMLFileShouldHaveNonEmptyKeysValidatesTable(t *testing.T) {
	sc, _ := newScenarioContext(t)

	tests := []struct {
		name  string
		table *godog.Table
		want  string
	}{
		{
			name: "no data rows",
			table: docTable(t, [][]string{
				{"key"},
			}),
			want: "at least one data row",
		},
		{
			name: "wrong header",
			table: docTable(t, [][]string{
				{"name"},
				{"clusterID"},
			}),
			want: `table header must be "key"`,
		},
		{
			name: "empty key",
			table: docTable(t, [][]string{
				{"key"},
				{""},
			}),
			want: "empty key value",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := sc.yamlFileShouldHaveNonEmptyKeys("registration.yaml", tc.table)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestCommandOutputContainsAssertion(t *testing.T) {
	sc, _ := newScenarioContext(t)
	sc.LastResult = harness.Result{Stdout: "release deployed", Stderr: ""}
	if err := sc.commandOutputShouldContain("deployed"); err != nil {
		t.Fatalf("contain: %v", err)
	}
	if err := sc.commandOutputShouldNotContain("deployed"); err == nil {
		t.Fatal("expected mismatch for not-contain")
	}
}

func TestJSONOutputContainsRowsAssertion(t *testing.T) {
	sc, _ := newScenarioContext(t)
	sc.LastResult = harness.Result{Stdout: `[{"name":"api","namespace":"nvcf"}]`}
	table := docTable(t, [][]string{
		{"name", "namespace"},
		{"api", "nvcf"},
	})
	if err := sc.jsonOutputShouldContainRows(table); err != nil {
		t.Fatalf("rows: %v", err)
	}
}

func TestRenderedManifestsShouldNotContainAcceptsExplicitMarkers(t *testing.T) {
	sc, fake := newScenarioContext(t)
	renderDir := filepath.Join(sc.Suite.Config.RepoRoot, "out", "rendered", "control-plane")
	if err := os.MkdirAll(renderDir, 0o755); err != nil {
		t.Fatalf("create render directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(renderDir, "api.yaml"), []byte("kind: Deployment\n"), 0o644); err != nil {
		t.Fatalf("write rendered manifest: %v", err)
	}
	table := docTable(t, [][]string{
		{"text"},
		{"kind: ServiceMonitor"},
		{"kind: PodMonitor"},
	})

	if err := sc.renderedManifestsShouldNotContain("out/rendered", table); err != nil {
		t.Fatalf("assert rendered manifests: %v", err)
	}
	if len(fake.runs) != 0 {
		t.Fatalf("runs = %d, want 0", len(fake.runs))
	}
}

func TestRenderedManifestsShouldContainAcceptsExplicitMarkers(t *testing.T) {
	sc, fake := newScenarioContext(t)
	renderDir := filepath.Join(sc.Suite.Config.RepoRoot, "out", "rendered")
	if err := os.MkdirAll(renderDir, 0o755); err != nil {
		t.Fatalf("create render directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(renderDir, "collector.yaml"), []byte("kind: OpenTelemetryCollector\n"), 0o644); err != nil {
		t.Fatalf("write rendered manifest: %v", err)
	}
	table := docTable(t, [][]string{
		{"text"},
		{"kind: OpenTelemetryCollector"},
	})

	if err := sc.renderedManifestsShouldContain("out/rendered", table); err != nil {
		t.Fatalf("assert rendered manifests: %v", err)
	}
	if len(fake.runs) != 0 {
		t.Fatalf("runs = %d, want 0", len(fake.runs))
	}
}

func TestRenderedManifestsUnderMatchingDirectoriesShouldContainAcceptsExplicitMarkers(t *testing.T) {
	sc, fake := newScenarioContext(t)
	renderDir := filepath.Join(sc.Suite.Config.RepoRoot, "out", "rendered", "01-nats", "templates")
	if err := os.MkdirAll(renderDir, 0o755); err != nil {
		t.Fatalf("create render directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(renderDir, "nats.yaml"), []byte("image: docker.io/natsio/reloader:0.23.0\n"), 0o644); err != nil {
		t.Fatalf("write rendered manifest: %v", err)
	}
	table := docTable(t, [][]string{
		{"text"},
		{"docker.io/natsio/reloader:0.23.0"},
	})

	if err := sc.renderedManifestsUnderMatchingDirectoriesShouldContain("out/rendered", "*-nats", table); err != nil {
		t.Fatalf("assert rendered manifests: %v", err)
	}
	if len(fake.runs) != 0 {
		t.Fatalf("runs = %d, want 0", len(fake.runs))
	}
}

// docTable builds a godog.Table from a slice of rows. The Picker rows
// type matches what godog hands to step handlers at runtime.
func docTable(t *testing.T, rows [][]string) *godog.Table {
	t.Helper()
	table := &godog.Table{}
	for _, row := range rows {
		cells := make([]*messages.PickleTableCell, 0, len(row))
		for _, v := range row {
			cells = append(cells, &messages.PickleTableCell{Value: v})
		}
		table.Rows = append(table.Rows, &messages.PickleTableRow{Cells: cells})
	}
	return table
}

func singleColumnTable(t *testing.T, values []string) *godog.Table {
	t.Helper()
	rows := make([][]string, 0, len(values))
	for _, v := range values {
		rows = append(rows, []string{v})
	}
	return docTable(t, rows)
}
