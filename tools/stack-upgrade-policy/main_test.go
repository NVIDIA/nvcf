// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

// release-bump-type reports the conventional-commit type, not a semver word,
// so that is what this has to accept.
func TestParseBumpAcceptsReleaseBumpTypeOutput(t *testing.T) {
	for in, want := range map[string]Bump{"feat!": Major, "fix!": Major, "feat": Minor, "fix": Patch} {
		got, err := ParseBump(in)
		if err != nil {
			t.Fatalf("ParseBump(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseBump(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseBumpRejectsGarbage(t *testing.T) {
	if _, err := ParseBump("probably-fine"); err == nil {
		t.Fatal("err = nil, want an error rather than a silent Patch")
	}
}

// fullFixture is a repo carrying both the release metadata and a published
// stack tag, so run() can be exercised end to end.
func fullFixture(t *testing.T) string {
	t.Helper()
	dir := fixture(t)
	write(t, dir, MetadataPath, `{"version":1,"services":[
      {"id":"nvcf-self-managed-stack","path":"deploy/stacks/self-managed",
       "migration_paths":["migrations/cassandra"]}]}`)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "metadata")
	return dir
}

func TestRunFailsOnADestructiveMigrationWithoutAMajorBump(t *testing.T) {
	dir := fullFixture(t)
	write(t, dir, "migrations/cassandra/keyspaces/nvct_api/05_drop_health_info.up.sql",
		"ALTER TABLE nvct_api.tasks_v2 DROP IF EXISTS health_info;")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "drop health_info")

	var out, errOut bytes.Buffer
	code, err := run(dir, "nvcf-self-managed-stack", "feat", false, &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "05_drop_health_info.up.sql") {
		t.Fatalf("stderr does not name the offending file:\n%s", errOut.String())
	}
}

func TestRunPassesTheSameChangeWithAMajorBump(t *testing.T) {
	dir := fullFixture(t)
	write(t, dir, "migrations/cassandra/keyspaces/nvct_api/05_drop_health_info.up.sql",
		"ALTER TABLE nvct_api.tasks_v2 DROP IF EXISTS health_info;")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "drop health_info")

	var out, errOut bytes.Buffer
	code, err := run(dir, "nvcf-self-managed-stack", "feat!", false, &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, errOut.String())
	}
}

// The report is the reason this runs on every candidate, not only failing
// ones: a release cutter staring at a list of chart version bumps otherwise
// has no way to know a migration landed.
func TestRunReportsAdditiveMigrationsAndStillPasses(t *testing.T) {
	dir := fullFixture(t)
	write(t, dir, "migrations/cassandra/keyspaces/sis_api/09_add_reservation_backup_disabled.up.sql",
		"ALTER TABLE sis_api.reservations ADD backup_disabled boolean;")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "add column")

	var out, errOut bytes.Buffer
	code, err := run(dir, "nvcf-self-managed-stack", "fix", false, &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "09_add_reservation_backup_disabled.up.sql") {
		t.Fatalf("report omits the additive migration:\n%s", out.String())
	}
}

func TestRunIsANoOpForAStackWithNoMigrationPaths(t *testing.T) {
	dir := fullFixture(t)
	write(t, dir, MetadataPath, `{"version":1,"services":[
      {"id":"nvcf-observability-stack","path":"deploy/stacks/observability"}]}`)
	write(t, dir, "migrations/cassandra/keyspaces/nvcf_api/07_drop_gpu_spec.up.sql",
		"DROP TYPE IF EXISTS nvcf_api.gpu_spec_udt;")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "unrelated drop")
	git(t, dir, "tag", "deploy/stacks/observability/v0.3.0")

	var out, errOut bytes.Buffer
	code, err := run(dir, "nvcf-observability-stack", "fix", false, &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, errOut.String())
	}
}

func TestRunEmitsJSON(t *testing.T) {
	dir := fullFixture(t)
	write(t, dir, "migrations/cassandra/keyspaces/sis_api/09_add.up.sql",
		"ALTER TABLE sis_api.reservations ADD backup_disabled boolean;")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "add column")

	var out, errOut bytes.Buffer
	if _, err := run(dir, "nvcf-self-managed-stack", "fix", true, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"class": "Additive"`) {
		t.Fatalf("json output missing class field:\n%s", out.String())
	}
}

func TestRunRejectsAnUnknownStack(t *testing.T) {
	dir := fullFixture(t)
	var out, errOut bytes.Buffer
	if _, err := run(dir, "not-a-stack", "fix", false, &out, &errOut); err == nil {
		t.Fatal("err = nil, want an error naming the unknown stack")
	}
}
