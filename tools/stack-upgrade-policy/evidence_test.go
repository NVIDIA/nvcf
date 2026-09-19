// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture builds a repo with one released stack tag and returns its root.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "migrations/cassandra/keyspaces/nvcf_api/03_init_tables.up.sql",
		"CREATE TABLE IF NOT EXISTS nvcf_api.functions_v3 (id uuid PRIMARY KEY);")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "base")
	git(t, dir, "tag", "deploy/stacks/self-managed/v1.0.0")
	return dir
}

func TestLatestReleaseTagPicksHighestSemver(t *testing.T) {
	dir := fixture(t)
	git(t, dir, "tag", "deploy/stacks/self-managed/v0.20.7")
	git(t, dir, "tag", "deploy/stacks/self-managed/v1.0.0-rc.1")

	got, err := LatestReleaseTag(dir, "deploy/stacks/self-managed")
	if err != nil {
		t.Fatal(err)
	}
	if want := "deploy/stacks/self-managed/v1.0.0"; got != want {
		t.Fatalf("LatestReleaseTag() = %q, want %q", got, want)
	}
}

func TestGatherEvidenceReportsNothingWhenNoMigrationsChanged(t *testing.T) {
	dir := fixture(t)
	write(t, dir, "src/foo/main.go", "package main")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "unrelated")

	ev, err := GatherEvidence(dir, "deploy/stacks/self-managed/v1.0.0", []string{"migrations/cassandra"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Changes) != 0 {
		t.Fatalf("Changes = %v, want none", ev.Changes)
	}
}

func TestGatherEvidenceClassifiesAnAddedDestructiveMigration(t *testing.T) {
	dir := fixture(t)
	write(t, dir, "migrations/cassandra/keyspaces/nvcf_api/07_drop_gpu_spec.up.sql",
		"ALTER TABLE IF EXISTS nvcf_api.functions_deployment_v2 DROP IF EXISTS gpu_specs;")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "drop")

	ev, err := GatherEvidence(dir, "deploy/stacks/self-managed/v1.0.0", []string{"migrations/cassandra"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Changes) != 1 {
		t.Fatalf("Changes = %v, want exactly one", ev.Changes)
	}
	c := ev.Changes[0]
	if c.Status != Added {
		t.Errorf("Status = %v, want Added", c.Status)
	}
	if c.Class != Destructive {
		t.Errorf("Class = %v, want Destructive", c.Class)
	}
}

func TestGatherEvidenceReportsADeletedMigration(t *testing.T) {
	dir := fixture(t)
	if err := os.Remove(filepath.Join(dir, "migrations/cassandra/keyspaces/nvcf_api/03_init_tables.up.sql")); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "remove the bridge")

	ev, err := GatherEvidence(dir, "deploy/stacks/self-managed/v1.0.0", []string{"migrations/cassandra"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Changes) != 1 {
		t.Fatalf("Changes = %v, want exactly one", ev.Changes)
	}
	if ev.Changes[0].Status != Deleted {
		t.Fatalf("Status = %v, want Deleted", ev.Changes[0].Status)
	}
}

// An additive migration added on top of a released tag must not be reported as
// qualifying, or every ordinary schema addition becomes a customer stop.
func TestGatherEvidenceClassifiesAnAddedAdditiveMigration(t *testing.T) {
	dir := fixture(t)
	write(t, dir, "migrations/cassandra/keyspaces/sis_api/09_add_reservation_backup_disabled.up.sql",
		"ALTER TABLE sis_api.reservations ADD backup_disabled boolean;")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "add column")

	ev, err := GatherEvidence(dir, "deploy/stacks/self-managed/v1.0.0", []string{"migrations/cassandra"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Changes) != 1 {
		t.Fatalf("Changes = %v, want exactly one", ev.Changes)
	}
	if ev.Changes[0].Class != Additive {
		t.Fatalf("Class = %v, want Additive", ev.Changes[0].Class)
	}
}
