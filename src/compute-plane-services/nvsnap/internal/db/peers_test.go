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
	"errors"
	"testing"
	"time"
)

// seedCheckpoint inserts a minimal checkpoint row so peer-add can pass
// FK-style validation (catalog: a peer must reference an existing
// checkpoint id). Returns the row's ID so peer ops have something to
// attach to.
func seedCheckpoint(t *testing.T, d *DB, id string) {
	t.Helper()
	c := &Checkpoint{
		ID:           id,
		CheckpointID: id,
		Namespace:    "nvsnap-system",
		PodName:      "vllm-small",
		NodeName:     "node-A",
		Status:       "Completed",
		CreatedAt:    time.Now().UTC(),
	}
	if err := d.CreateCheckpoint(c); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
}

// TestAddPeer_Idempotent — re-adding the same peer must not error and
// must update agent_url + last_seen without duplicating the row.
// Phase 5d.1 invariant: when an agent restarts and re-registers, the
// catalog stays clean (no row explosion across rolling agent updates).
func TestAddPeer_Idempotent(t *testing.T) {
	d := openTestDB(t)
	seedCheckpoint(t, d, "ckpt-1")

	if err := d.AddPeer("ckpt-1", "node-A", "http://10.0.0.1:8081"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	// Second add with a different URL — simulates an agent that
	// rescheduled to a new pod IP.
	if err := d.AddPeer("ckpt-1", "node-A", "http://10.0.0.42:8081"); err != nil {
		t.Fatalf("second add: %v", err)
	}

	peers, err := d.ListPeers("ckpt-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 {
		t.Fatalf("want 1 peer (idempotent), got %d", len(peers))
	}
	if peers[0].AgentURL != "http://10.0.0.42:8081" {
		t.Errorf("agent_url not updated on re-add: got %q", peers[0].AgentURL)
	}
}

// TestListPeers_OrderedByLastSeenDesc — the cascading fetch reads
// peers in this order and tries the first one. Most-recently-seen
// peer should be first because it's most likely to still be alive.
func TestListPeers_OrderedByLastSeenDesc(t *testing.T) {
	d := openTestDB(t)
	seedCheckpoint(t, d, "ckpt-2")

	if err := d.AddPeer("ckpt-2", "node-A", "http://10.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	// Sleep 1 s to get a different RFC3339 second-resolution timestamp.
	// (sqlite stores the second-truncated value; without this gap
	// last_seen would tie and the order would be undefined.)
	time.Sleep(1100 * time.Millisecond)
	if err := d.AddPeer("ckpt-2", "node-B", "http://10.0.0.2:8081"); err != nil {
		t.Fatal(err)
	}

	peers, err := d.ListPeers("ckpt-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("want 2 peers, got %d", len(peers))
	}
	if peers[0].NodeName != "node-B" {
		t.Errorf("most-recent peer should be first; got order: %s, %s",
			peers[0].NodeName, peers[1].NodeName)
	}
}

// TestRemovePeer_Idempotent — removing an absent row is a no-op.
// Agent's eviction notification fires after the local row was
// already pruned (e.g., by another path); we don't want spurious
// 500s in catalog logs.
func TestRemovePeer_Idempotent(t *testing.T) {
	d := openTestDB(t)
	seedCheckpoint(t, d, "ckpt-3")

	if err := d.AddPeer("ckpt-3", "node-A", "http://10.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	if err := d.RemovePeer("ckpt-3", "node-A"); err != nil {
		t.Fatalf("first remove: %v", err)
	}
	// Removing again — no row, but no error.
	if err := d.RemovePeer("ckpt-3", "node-A"); err != nil {
		t.Fatalf("idempotent remove errored: %v", err)
	}
	peers, _ := d.ListPeers("ckpt-3")
	if len(peers) != 0 {
		t.Errorf("after remove, want 0 peers, got %d", len(peers))
	}
}

// TestTouchPeer_NoRowReturnsErrNoRows — health-check sweep uses this
// to detect "peer was deregistered between probes" so it doesn't
// silently re-create a row for a node that already left.
func TestTouchPeer_NoRowReturnsErrNoRows(t *testing.T) {
	d := openTestDB(t)
	seedCheckpoint(t, d, "ckpt-4")

	err := d.TouchPeer("ckpt-4", "node-never-added")
	if !errors.Is(err, errNoRowsSentinel()) {
		// allow the sql.ErrNoRows check via the helper below
		// (errors.Is uses == for sentinels which is what we want)
		t.Errorf("want sql.ErrNoRows, got %v", err)
	}
}

// errNoRowsSentinel returns sql.ErrNoRows without importing it at
// the top of the test file (database/sql is heavy and the test
// already uses errors.Is for the comparison).
func errNoRowsSentinel() error {
	d := openTestDBSilent()
	defer func() { _ = d.Close() }()
	row := d.db.QueryRow("SELECT 1 WHERE FALSE")
	var x int
	return row.Scan(&x)
}

func openTestDBSilent() *DB {
	d, _ := Open(":memory:")
	return d
}

// TestGetCheckpointSources_PeersAndBlobURI — the load-bearing
// single-call query the cascade reads. Returns peers + blob URI in
// one shot.
func TestGetCheckpointSources_PeersAndBlobURI(t *testing.T) {
	d := openTestDB(t)
	seedCheckpoint(t, d, "ckpt-5")

	if err := d.AddPeer("ckpt-5", "node-A", "http://10.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetCheckpointBlobURI("ckpt-5", "s3://nvsnap-blobs/captures/ckpt-5/"); err != nil {
		t.Fatal(err)
	}

	peers, blobURI, err := d.GetCheckpointSources("ckpt-5")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].NodeName != "node-A" {
		t.Errorf("peers wrong: %+v", peers)
	}
	if blobURI != "s3://nvsnap-blobs/captures/ckpt-5/" {
		t.Errorf("blob_uri = %q, want s3://...", blobURI)
	}
}

// TestGetCheckpointSources_NoPeersNoBlob — fresh checkpoint with
// nothing registered yet returns an empty list and empty blob URI.
// The cascade must NOT 500 in this case; it surfaces "no sources" to
// the receiver, which then errors at the application layer.
func TestGetCheckpointSources_NoPeersNoBlob(t *testing.T) {
	d := openTestDB(t)
	seedCheckpoint(t, d, "ckpt-6")

	peers, blobURI, err := d.GetCheckpointSources("ckpt-6")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 0 {
		t.Errorf("want 0 peers, got %d", len(peers))
	}
	if blobURI != "" {
		t.Errorf("want empty blob URI, got %q", blobURI)
	}
}

// TestSetCheckpointBlobURI_PersistsTimestamp — stage 5d.2 hook,
// validated here so the schema/column wiring is testable now even
// though no caller fires it yet.
func TestSetCheckpointBlobURI_PersistsTimestamp(t *testing.T) {
	d := openTestDB(t)
	seedCheckpoint(t, d, "ckpt-7")

	if err := d.SetCheckpointBlobURI("ckpt-7", "s3://x/y"); err != nil {
		t.Fatal(err)
	}
	var s3URI, uploadedAt string
	row := d.db.QueryRow(`SELECT s3_uri, blob_uploaded_at FROM checkpoints WHERE id = 'ckpt-7'`)
	if err := row.Scan(&s3URI, &uploadedAt); err != nil {
		t.Fatal(err)
	}
	if s3URI != "s3://x/y" {
		t.Errorf("s3_uri = %q, want s3://x/y", s3URI)
	}
	if uploadedAt == "" {
		t.Error("blob_uploaded_at should be set")
	}
}
