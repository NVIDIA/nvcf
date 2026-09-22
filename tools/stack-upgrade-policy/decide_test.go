// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

func TestDecideNoMigrationChangesPasses(t *testing.T) {
	d := Decide(Evidence{}, Minor)
	if !d.OK {
		t.Fatalf("OK = false, want true (reason: %s)", d.Reason)
	}
	if d.Qualifying {
		t.Fatal("Qualifying = true, want false")
	}
}

func TestDecideAdditiveOnlyPassesOnMinor(t *testing.T) {
	ev := Evidence{Changes: []Change{
		{Path: "keyspaces/sis_api/09_add_reservation_backup_disabled.up.sql", Status: Added, Class: Additive},
	}}
	d := Decide(ev, Minor)
	if !d.OK {
		t.Fatalf("OK = false, want true (reason: %s)", d.Reason)
	}
	if d.Qualifying {
		t.Fatal("Qualifying = true, want false")
	}
}

func TestDecideDestructiveWithoutMajorFails(t *testing.T) {
	ev := Evidence{Changes: []Change{
		{Path: "keyspaces/nvcf_api/07_drop_gpu_spec.up.sql", Status: Added, Class: Destructive},
	}}
	d := Decide(ev, Minor)
	if d.OK {
		t.Fatal("OK = true, want false")
	}
	if !d.Qualifying {
		t.Fatal("Qualifying = false, want true")
	}
	if d.Reason == "" {
		t.Fatal("Reason is empty, want an explanation of what to do")
	}
}

func TestDecideDestructiveWithMajorPasses(t *testing.T) {
	ev := Evidence{Changes: []Change{
		{Path: "keyspaces/nvcf_api/07_drop_gpu_spec.up.sql", Status: Added, Class: Destructive},
	}}
	d := Decide(ev, Major)
	if !d.OK {
		t.Fatalf("OK = false, want true (reason: %s)", d.Reason)
	}
	if !d.Qualifying {
		t.Fatal("Qualifying = false, want true")
	}
}

// A deleted migration is how the compatibility bridge disappears: the backfill
// task stops shipping, so a cluster arriving later has no way to run it. That
// is the change that forces a customer stop, and it is invisible to a
// classifier that only reads added files.
func TestDecideDeletedMigrationWithoutMajorFails(t *testing.T) {
	ev := Evidence{Changes: []Change{
		{Path: "keyspaces/nvcf_api/05_add_model_specs.up.sql", Status: Deleted},
	}}
	d := Decide(ev, Patch)
	if d.OK {
		t.Fatal("OK = true, want false")
	}
	if !d.Qualifying {
		t.Fatal("Qualifying = false, want true")
	}
}

func TestDecideDeletedMigrationWithMajorPasses(t *testing.T) {
	ev := Evidence{Changes: []Change{
		{Path: "keyspaces/nvcf_api/05_add_model_specs.up.sql", Status: Deleted},
	}}
	if d := Decide(ev, Major); !d.OK {
		t.Fatalf("OK = false, want true (reason: %s)", d.Reason)
	}
}
