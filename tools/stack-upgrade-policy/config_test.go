// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

const metadataFixture = `{
  "version": 1,
  "services": [
    {"id": "nvcf-cli", "path": "src/clis/nvcf-cli"},
    {"id": "nvcf-self-managed-stack", "path": "deploy/stacks/self-managed",
     "migration_paths": ["migrations/cassandra", "migrations/openbao"]},
    {"id": "nvcf-observability-stack", "path": "deploy/stacks/observability"}
  ]
}`

func metadataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, MetadataPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(metadataFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadStackReadsPathAndMigrationPaths(t *testing.T) {
	s, err := LoadStack(metadataDir(t), "nvcf-self-managed-stack")
	if err != nil {
		t.Fatal(err)
	}
	if s.Path != "deploy/stacks/self-managed" {
		t.Errorf("Path = %q", s.Path)
	}
	if len(s.MigrationPaths) != 2 {
		t.Errorf("MigrationPaths = %v, want two", s.MigrationPaths)
	}
}

// A stack that declares no migration paths is not an error: the compute-plane
// and observability stacks ship no schema, so the check is a no-op for them
// until they need it.
func TestLoadStackAllowsNoMigrationPaths(t *testing.T) {
	s, err := LoadStack(metadataDir(t), "nvcf-observability-stack")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.MigrationPaths) != 0 {
		t.Errorf("MigrationPaths = %v, want none", s.MigrationPaths)
	}
}

func TestLoadStackRejectsUnknownID(t *testing.T) {
	if _, err := LoadStack(metadataDir(t), "no-such-stack"); err == nil {
		t.Fatal("err = nil, want an error naming the unknown stack")
	}
}

// Guards against pointing the check at a service that is not a stack, which
// would silently compare against a tag series that has nothing to do with a
// published bundle.
func TestLoadStackRejectsANonStackService(t *testing.T) {
	if _, err := LoadStack(metadataDir(t), "nvcf-cli"); err == nil {
		t.Fatal("err = nil, want an error for a non-stack service")
	}
}
