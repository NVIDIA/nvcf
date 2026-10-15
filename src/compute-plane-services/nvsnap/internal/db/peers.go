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
	"database/sql"
	"fmt"
	"time"
)

// CheckpointPeer is one row in checkpoint_peers — a node that holds a
// local copy of the checkpoint and is reachable via agent_url.
type CheckpointPeer struct {
	CheckpointID string    `json:"checkpoint_id"`
	NodeName     string    `json:"node_name"`
	AgentURL     string    `json:"agent_url"`
	RegisteredAt time.Time `json:"registered_at"`
	LastSeen     time.Time `json:"last_seen"`
}

// AddPeer registers a node as having a local copy of the checkpoint.
// Idempotent — re-adding refreshes last_seen but keeps registered_at.
// Used by:
//
//   - NvSnap-server when CreateCheckpoint completes (source node auto-reg).
//   - Agent's restore-side cascading fetch after a successful download
//     (the receiver becomes a new peer for future fanouts).
//
// Timestamps stored as RFC3339 to match the rest of the schema —
// every other catalog table uses Go-side time.Now().UTC().Format
// rather than sqlite's datetime('now') so reads parse uniformly.
func (d *DB) AddPeer(checkpointID, nodeName, agentURL string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		INSERT INTO checkpoint_peers (checkpoint_id, node_name, agent_url, registered_at, last_seen)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(checkpoint_id, node_name) DO UPDATE SET
			agent_url = excluded.agent_url,
			last_seen = excluded.last_seen
	`, checkpointID, nodeName, agentURL, now, now)
	if err != nil {
		return fmt.Errorf("add peer: %w", err)
	}
	return nil
}

// RemovePeer deregisters a node — called when an agent evicts the
// local copy under LRU pressure, or when the periodic health-check
// sweep finds an unreachable peer. Returns nil even if the row didn't
// exist (idempotent — we don't want eviction notifications to error
// just because the catalog already pruned the entry).
func (d *DB) RemovePeer(checkpointID, nodeName string) error {
	_, err := d.db.Exec(`
		DELETE FROM checkpoint_peers WHERE checkpoint_id = ? AND node_name = ?
	`, checkpointID, nodeName)
	if err != nil {
		return fmt.Errorf("remove peer: %w", err)
	}
	return nil
}

// ListPeers returns all registered peers for a checkpoint, ordered
// most-recently-seen first. The restore-side cascading fetch tries
// peers in this order before falling back to the blob store; a peer
// that responded recently is more likely to still be reachable.
func (d *DB) ListPeers(checkpointID string) ([]CheckpointPeer, error) {
	rows, err := d.db.Query(`
		SELECT checkpoint_id, node_name, agent_url, registered_at, last_seen
		FROM checkpoint_peers
		WHERE checkpoint_id = ?
		ORDER BY last_seen DESC
	`, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var peers []CheckpointPeer
	for rows.Next() {
		var p CheckpointPeer
		var registered, lastSeen string
		if err := rows.Scan(&p.CheckpointID, &p.NodeName, &p.AgentURL, &registered, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan peer: %w", err)
		}
		p.RegisteredAt, _ = time.Parse(time.RFC3339, registered)
		p.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
		peers = append(peers, p)
	}
	return peers, rows.Err()
}

// TouchPeer refreshes a peer's last_seen — invoked by the periodic
// health sweep when a peer responds to /healthz. Decoupled from
// AddPeer so the sweep doesn't accidentally insert a fresh row for
// a peer that was just deregistered by another path.
func (d *DB) TouchPeer(checkpointID, nodeName string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := d.db.Exec(`
		UPDATE checkpoint_peers SET last_seen = ?
		WHERE checkpoint_id = ? AND node_name = ?
	`, now, checkpointID, nodeName)
	if err != nil {
		return fmt.Errorf("touch peer: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetCheckpointBlobURI updates a checkpoint with the nvsnap-blobstore
// URI after a successful background upload (stage 5d.2). For 5d.1
// this is unused; the schema column exists but stays empty.
func (d *DB) SetCheckpointBlobURI(id, s3URI string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		UPDATE checkpoints
		SET s3_uri = ?, blob_uploaded_at = ?
		WHERE id = ?
	`, s3URI, now, id)
	if err != nil {
		return fmt.Errorf("set blob uri: %w", err)
	}
	return nil
}

// GetCheckpointSources is the routing query the restore-side cascade
// reads on every fanout. Returns:
//
//   - peers: ordered list of {node_name, agent_url} the restore should
//     try, most-recently-seen first.
//   - blobURI: the nvsnap-blobstore URI as a final fallback (empty if
//     blob upload hasn't completed yet — only peers usable then).
//
// One DB round-trip — kept lean because every restore hits this.
func (d *DB) GetCheckpointSources(checkpointID string) (peers []CheckpointPeer, blobURI string, err error) {
	peers, err = d.ListPeers(checkpointID)
	if err != nil {
		return nil, "", err
	}
	row := d.db.QueryRow(`SELECT s3_uri FROM checkpoints WHERE id = ?`, checkpointID)
	if err := row.Scan(&blobURI); err != nil {
		if err == sql.ErrNoRows {
			return peers, "", nil // checkpoint may have been recently deleted
		}
		return nil, "", fmt.Errorf("read s3_uri: %w", err)
	}
	return peers, blobURI, nil
}
