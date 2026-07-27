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
	command string
}

type fakeRunner struct {
	runs   []recordedRun
	result harness.Result
	err    error
}

func (f *fakeRunner) Run(_ context.Context, command string) (harness.Result, error) {
	f.runs = append(f.runs, recordedRun{command: command})
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

func TestRegisterAllRunsAFeatureFile(t *testing.T) {
	// End-to-end smoke check that RegisterAll wires every category. A
	// minimal in-memory feature is driven through a Godog TestSuite so
	// the regex registrations and the Before hook are both exercised.
	feature := `Feature: Smoke
  Scenario: register-all smoke
    Given environment variable "BDD_TMP_SMOKE" is set
    When I run command "echo smoke"
    Then the command exit code should be 0
`
	t.Setenv("BDD_TMP_SMOKE", "ready")
	sc, fake := newScenarioContext(t)
	fake.result = harness.Result{ExitCode: 0, Stdout: "smoke\n"}

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

func TestISubstituteBase64DoesNotReturnSecretMaterial(t *testing.T) {
	sc, _ := newScenarioContext(t)
	rel := "secrets.yaml"
	abs := filepath.Join(sc.Suite.Config.RepoRoot, rel)
	if err := os.WriteFile(abs, []byte("token: REPLACE_ME\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Setenv("BDD_TMP_API_KEY", "real-secret-token")
	if err := sc.iSubstituteBase64("REPLACE_ME", rel, "${BDD_TMP_API_KEY}"); err != nil {
		t.Fatalf("substitute: %v", err)
	}
	got, _ := os.ReadFile(abs)
	if strings.Contains(string(got), "real-secret-token") {
		t.Fatalf("raw secret leaked into file body:\n%s", got)
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
	if err := sc.yamlFileKeyShouldContain(rel, "controlPlane.endpoints.inCluster", &godog.DocString{Content: "icmsURL: http://api.sis:8080\n"}); err != nil {
		t.Fatalf("contain: %v", err)
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
