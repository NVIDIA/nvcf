/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package db

import (
	"fmt"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestMigrations(t *testing.T) {
	d := openTestDB(t)

	// Verify tables exist by querying them
	var count int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM checkpoints").Scan(&count); err != nil {
		t.Fatalf("checkpoints table missing: %v", err)
	}
	if err := d.db.QueryRow("SELECT COUNT(*) FROM restores").Scan(&count); err != nil {
		t.Fatalf("restores table missing: %v", err)
	}
	if err := d.db.QueryRow("SELECT COUNT(*) FROM demo_state").Scan(&count); err != nil {
		t.Fatalf("demo_state table missing: %v", err)
	}

	// Verify migration is idempotent
	if err := d.runMigrations(); err != nil {
		t.Fatalf("re-running migrations failed: %v", err)
	}
}

func TestCheckpointCRUD(t *testing.T) {
	d := openTestDB(t)

	now := time.Now().UTC()
	c := &Checkpoint{
		ID:           "ckpt-1",
		CheckpointID: "abc123",
		Namespace:    "default",
		PodName:      "vllm-pod",
		NodeName:     "node-1",
		Status:       "InProgress",
		HasGPU:       true,
		WorkloadType: "vllm",
		StartedAt:    &now,
	}

	// Create
	if err := d.CreateCheckpoint(c); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get
	got, err := d.GetCheckpoint("ckpt-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PodName != "vllm-pod" {
		t.Errorf("pod name = %q, want %q", got.PodName, "vllm-pod")
	}
	if got.Status != "InProgress" {
		t.Errorf("status = %q, want %q", got.Status, "InProgress")
	}
	if !got.HasGPU {
		t.Error("hasGPU should be true")
	}

	// Update status (nvsnap#61: hash is the new arg between path and size)
	const wantHash = "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b"
	if uerr := d.UpdateCheckpointStatus("ckpt-1", "Completed", "Done in 30s", "/data/ckpt", wantHash, 1024*1024, 30.0); uerr != nil {
		t.Fatalf("update: %v", uerr)
	}

	got, err = d.GetCheckpoint("ckpt-1")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Status != "Completed" {
		t.Errorf("status = %q, want %q", got.Status, "Completed")
	}
	if got.CheckpointPath != "/data/ckpt" {
		t.Errorf("path = %q, want %q", got.CheckpointPath, "/data/ckpt")
	}
	if got.Hash != wantHash {
		t.Errorf("hash = %q, want %q", got.Hash, wantHash)
	}
	if got.CompletedAt == nil {
		t.Error("completedAt should be set")
	}

	// nvsnap#61: COALESCE preserves prior hash on a subsequent
	// status-only update (e.g., a Failed reconcile after success
	// shouldn't wipe the hash a previous Completed wrote).
	if uerr := d.UpdateCheckpointStatus("ckpt-1", "Failed", "subsequent failure", "/data/ckpt", "", 0, 0); uerr != nil {
		t.Fatalf("second update: %v", uerr)
	}
	got, err = d.GetCheckpoint("ckpt-1")
	if err != nil {
		t.Fatalf("get after second update: %v", err)
	}
	if got.Hash != wantHash {
		t.Errorf("hash got wiped by empty-hash update; got %q, want %q", got.Hash, wantHash)
	}

	// Delete
	if derr := d.DeleteCheckpoint("ckpt-1"); derr != nil {
		t.Fatalf("delete: %v", derr)
	}
	_, err = d.GetCheckpoint("ckpt-1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestListCheckpointsFilter(t *testing.T) {
	d := openTestDB(t)

	checkpoints := []Checkpoint{
		{ID: "c1", CheckpointID: "x1", Namespace: "default", PodName: "pod-a", NodeName: "n1", Status: "Completed", WorkloadType: "vllm"},
		{ID: "c2", CheckpointID: "x2", Namespace: "default", PodName: "pod-b", NodeName: "n2", Status: "Failed", WorkloadType: "sglang"},
		{ID: "c3", CheckpointID: "x3", Namespace: "prod", PodName: "pod-c", NodeName: "n1", Status: "Completed", WorkloadType: "vllm"},
	}
	for i := range checkpoints {
		if err := d.CreateCheckpoint(&checkpoints[i]); err != nil {
			t.Fatalf("create %s: %v", checkpoints[i].ID, err)
		}
	}

	tests := []struct {
		name   string
		filter CheckpointFilter
		want   int
	}{
		{"all", CheckpointFilter{}, 3},
		{"by namespace", CheckpointFilter{Namespace: "default"}, 2},
		{"by status", CheckpointFilter{Status: "Completed"}, 2},
		{"by node", CheckpointFilter{NodeName: "n1"}, 2},
		{"by workload", CheckpointFilter{WorkloadType: "sglang"}, 1},
		{"combined", CheckpointFilter{Namespace: "default", Status: "Completed"}, 1},
		{"no match", CheckpointFilter{Namespace: "staging"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, err := d.ListCheckpoints(tt.filter)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(list) != tt.want {
				t.Errorf("got %d, want %d", len(list), tt.want)
			}
		})
	}
}

func TestRestoreCRUD(t *testing.T) {
	d := openTestDB(t)

	r := &Restore{
		ID:           "restore-1",
		CheckpointID: "abc123",
		Namespace:    "default",
		NodeName:     "node-1",
		NewPodName:   "pod-restored",
		Status:       "Pending",
	}

	// Create
	if err := d.CreateRestore(r); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get
	got, err := d.GetRestore("restore-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "Pending" {
		t.Errorf("status = %q, want %q", got.Status, "Pending")
	}

	// Update to CreatingPod (should set started_at)
	if uerr := d.UpdateRestoreStatus("restore-1", "CreatingPod", "pod-restored", "Getting manifest"); uerr != nil {
		t.Fatalf("update to CreatingPod: %v", uerr)
	}
	got, err = d.GetRestore("restore-1")
	if err != nil {
		t.Fatalf("get after CreatingPod: %v", err)
	}
	if got.StartedAt == nil {
		t.Error("started_at should be set after CreatingPod")
	}

	// Update to Completed
	if uerr := d.UpdateRestoreStatus("restore-1", "Completed", "pod-restored", "Done"); uerr != nil {
		t.Fatalf("update to Completed: %v", uerr)
	}
	got, err = d.GetRestore("restore-1")
	if err != nil {
		t.Fatalf("get after Completed: %v", err)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at should be set")
	}

	// List
	list, err := d.ListRestores()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("got %d restores, want 1", len(list))
	}
}

func TestDemoState(t *testing.T) {
	d := openTestDB(t)

	// Load before any save → nil
	s, err := d.LoadDemoState()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if s != nil {
		t.Fatalf("expected nil, got %+v", s)
	}

	// Save
	state := &DemoState{
		Phase:           "RUNNING",
		WorkloadType:    "vllm",
		PodName:         "vllm-pod",
		NodeName:        "node-1",
		Message:         "Model ready",
		DeployDuration:  120.5,
		CheckpointsJSON: `[{"id":"abc","size":1024,"duration":30}]`,
	}
	if serr := d.SaveDemoState(state); serr != nil {
		t.Fatalf("save: %v", serr)
	}

	// Load
	loaded, err := d.LoadDemoState()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Phase != "RUNNING" {
		t.Errorf("phase = %q, want %q", loaded.Phase, "RUNNING")
	}
	if loaded.PodName != "vllm-pod" {
		t.Errorf("pod = %q, want %q", loaded.PodName, "vllm-pod")
	}
	if loaded.DeployDuration != 120.5 {
		t.Errorf("deploy duration = %f, want 120.5", loaded.DeployDuration)
	}

	// Upsert (update existing)
	state.Phase = "CHECKPOINTED"
	state.CkptDuration = 89.0
	if serr := d.SaveDemoState(state); serr != nil {
		t.Fatalf("upsert: %v", serr)
	}
	loaded, err = d.LoadDemoState()
	if err != nil {
		t.Fatalf("load after upsert: %v", err)
	}
	if loaded.Phase != "CHECKPOINTED" {
		t.Errorf("phase = %q, want %q", loaded.Phase, "CHECKPOINTED")
	}
	if loaded.CkptDuration != 89.0 {
		t.Errorf("ckpt duration = %f, want 89.0", loaded.CkptDuration)
	}
}

func TestCheckpointPagination(t *testing.T) {
	d := openTestDB(t)

	// Create 5 checkpoints with distinct timestamps
	for i := 0; i < 5; i++ {
		c := &Checkpoint{
			ID:           fmt.Sprintf("c%d", i),
			CheckpointID: fmt.Sprintf("x%d", i),
			Namespace:    "default",
			PodName:      "pod-a",
			NodeName:     "n1",
			Status:       "Completed",
			WorkloadType: "vllm",
			CreatedAt:    time.Now().UTC().Add(time.Duration(-5+i) * time.Minute),
		}
		if err := d.CreateCheckpoint(c); err != nil {
			t.Fatalf("create %s: %v", c.ID, err)
		}
	}

	// Page 1: limit=2
	paged, err := d.ListCheckpointsPaged(CheckpointFilter{Limit: 2})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(paged.Items) != 2 {
		t.Fatalf("page 1: got %d items, want 2", len(paged.Items))
	}
	if paged.Total != 5 {
		t.Errorf("total = %d, want 5", paged.Total)
	}
	if !paged.HasMore {
		t.Error("expected hasMore=true")
	}
	if paged.NextCursor == "" {
		t.Error("expected nextCursor to be set")
	}

	// Page 2: use cursor
	paged2, err := d.ListCheckpointsPaged(CheckpointFilter{Limit: 2, Cursor: paged.NextCursor})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(paged2.Items) != 2 {
		t.Fatalf("page 2: got %d items, want 2", len(paged2.Items))
	}

	// Page 3: last page
	paged3, err := d.ListCheckpointsPaged(CheckpointFilter{Limit: 2, Cursor: paged2.NextCursor})
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(paged3.Items) != 1 {
		t.Fatalf("page 3: got %d items, want 1", len(paged3.Items))
	}
	if paged3.HasMore {
		t.Error("expected hasMore=false on last page")
	}
}

func TestRetentionPolicyCRUD(t *testing.T) {
	d := openTestDB(t)

	p := &RetentionPolicy{
		Name:         "keep-5-vllm",
		Namespace:    "default",
		WorkloadType: "vllm",
		MaxCount:     5,
		MaxAgeHours:  24 * 7, // 1 week
		Enabled:      true,
	}

	// Create
	if err := d.CreateRetentionPolicy(p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 {
		t.Error("expected non-zero ID after create")
	}

	// Get
	got, err := d.GetRetentionPolicy(p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "keep-5-vllm" {
		t.Errorf("name = %q, want %q", got.Name, "keep-5-vllm")
	}
	if got.MaxCount != 5 {
		t.Errorf("maxCount = %d, want 5", got.MaxCount)
	}
	if !got.Enabled {
		t.Error("expected enabled=true")
	}

	// Update
	got.MaxCount = 10
	got.Enabled = false
	if uerr := d.UpdateRetentionPolicy(got); uerr != nil {
		t.Fatalf("update: %v", uerr)
	}
	got2, _ := d.GetRetentionPolicy(p.ID)
	if got2.MaxCount != 10 {
		t.Errorf("maxCount after update = %d, want 10", got2.MaxCount)
	}
	if got2.Enabled {
		t.Error("expected enabled=false after update")
	}

	// List
	list, err := d.ListRetentionPolicies()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("list count = %d, want 1", len(list))
	}

	// Delete
	if err := d.DeleteRetentionPolicy(p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ = d.ListRetentionPolicies()
	if len(list) != 0 {
		t.Errorf("list after delete = %d, want 0", len(list))
	}
}

func TestRetentionFindExpired(t *testing.T) {
	d := openTestDB(t)

	// Create 5 checkpoints: 3 old (2 hours ago), 2 new
	for i := range 5 {
		age := -10 * time.Minute // new
		if i < 3 {
			age = -3 * time.Hour // old
		}
		c := &Checkpoint{
			ID:           fmt.Sprintf("c%d", i),
			CheckpointID: fmt.Sprintf("x%d", i),
			Namespace:    "default",
			PodName:      "pod",
			NodeName:     "n1",
			Status:       "Completed",
			WorkloadType: "vllm",
			CreatedAt:    time.Now().UTC().Add(age),
		}
		if err := d.CreateCheckpoint(c); err != nil {
			t.Fatalf("create %s: %v", c.ID, err)
		}
	}

	// Policy: max age 2 hours → should find the 3 old ones
	p := &RetentionPolicy{
		Namespace:    "*",
		WorkloadType: "*",
		MaxAgeHours:  2,
	}
	expired, err := d.FindExpiredCheckpoints(p)
	if err != nil {
		t.Fatalf("findExpired: %v", err)
	}
	if len(expired) != 3 {
		t.Errorf("expired by age: got %d, want 3", len(expired))
	}

	// Policy: max count 2 → should find 3 excess
	p2 := &RetentionPolicy{
		Namespace:    "*",
		WorkloadType: "*",
		MaxCount:     2,
	}
	expired2, err := d.FindExpiredCheckpoints(p2)
	if err != nil {
		t.Fatalf("findExpired count: %v", err)
	}
	if len(expired2) != 3 {
		t.Errorf("expired by count: got %d, want 3", len(expired2))
	}
}

func TestAuditLog(t *testing.T) {
	d := openTestDB(t)

	// Log entries
	if err := d.LogAudit(&AuditEntry{
		Action: "checkpoint.create", Resource: "checkpoint", ResourceID: "ckpt-1",
		Actor: "api", Message: "Created checkpoint",
	}); err != nil {
		t.Fatalf("log audit 1: %v", err)
	}
	if err := d.LogAudit(&AuditEntry{
		Action: "checkpoint.delete", Resource: "checkpoint", ResourceID: "ckpt-1",
		Actor: "policy:weekly-cleanup", Message: "Deleted by retention",
	}); err != nil {
		t.Fatalf("log audit 2: %v", err)
	}
	if err := d.LogAudit(&AuditEntry{
		Action: "restore.create", Resource: "restore", ResourceID: "restore-1",
		Actor: "api", Message: "Restore started",
	}); err != nil {
		t.Fatalf("log audit 3: %v", err)
	}

	// List all
	entries, err := d.ListAuditLog(AuditFilter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	// Filter by action
	entries, _ = d.ListAuditLog(AuditFilter{Action: "checkpoint.create"})
	if len(entries) != 1 {
		t.Errorf("filter by action: got %d, want 1", len(entries))
	}

	// Filter by resource
	entries, _ = d.ListAuditLog(AuditFilter{Resource: "checkpoint"})
	if len(entries) != 2 {
		t.Errorf("filter by resource: got %d, want 2", len(entries))
	}

	// Filter by actor
	entries, _ = d.ListAuditLog(AuditFilter{Actor: "policy:weekly-cleanup"})
	if len(entries) != 1 {
		t.Errorf("filter by actor: got %d, want 1", len(entries))
	}

	// Limit
	entries, _ = d.ListAuditLog(AuditFilter{Limit: 1})
	if len(entries) != 1 {
		t.Errorf("limit: got %d, want 1", len(entries))
	}
}

func TestGetCheckpointNotFound(t *testing.T) {
	d := openTestDB(t)

	_, err := d.GetCheckpoint("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent checkpoint")
	}
}

func TestGetRestoreNotFound(t *testing.T) {
	d := openTestDB(t)

	_, err := d.GetRestore("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent restore")
	}
}
