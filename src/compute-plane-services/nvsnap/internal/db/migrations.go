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

import "fmt"

// migrations is an ordered list of SQL statements. Each is applied once,
// tracked by the schema_version table. Add new migrations at the end.
var migrations = []string{
	// Migration 1: checkpoints, restores, demo_state
	`CREATE TABLE IF NOT EXISTS checkpoints (
		id              TEXT PRIMARY KEY,
		checkpoint_id   TEXT NOT NULL,
		namespace       TEXT NOT NULL DEFAULT 'default',
		pod_name        TEXT NOT NULL,
		container_name  TEXT NOT NULL DEFAULT '',
		container_image TEXT NOT NULL DEFAULT '',
		node_name       TEXT NOT NULL,
		checkpoint_path TEXT NOT NULL DEFAULT '',
		checkpoint_size INTEGER NOT NULL DEFAULT 0,
		status          TEXT NOT NULL DEFAULT 'InProgress',
		message         TEXT NOT NULL DEFAULT '',
		has_gpu         INTEGER NOT NULL DEFAULT 0,
		model_name      TEXT NOT NULL DEFAULT '',
		workload_type   TEXT NOT NULL DEFAULT '',
		created_at      TEXT NOT NULL DEFAULT (datetime('now')),
		started_at      TEXT,
		completed_at    TEXT,
		duration_secs   REAL NOT NULL DEFAULT 0,
		metadata_json   TEXT NOT NULL DEFAULT '{}'
	);
	CREATE INDEX IF NOT EXISTS idx_checkpoints_namespace ON checkpoints(namespace);
	CREATE INDEX IF NOT EXISTS idx_checkpoints_pod_name ON checkpoints(pod_name);
	CREATE INDEX IF NOT EXISTS idx_checkpoints_node_name ON checkpoints(node_name);
	CREATE INDEX IF NOT EXISTS idx_checkpoints_status ON checkpoints(status);
	CREATE INDEX IF NOT EXISTS idx_checkpoints_created_at ON checkpoints(created_at);
	CREATE INDEX IF NOT EXISTS idx_checkpoints_workload_type ON checkpoints(workload_type);

	CREATE TABLE IF NOT EXISTS restores (
		id              TEXT PRIMARY KEY,
		checkpoint_id   TEXT NOT NULL,
		checkpoint_ref  TEXT NOT NULL DEFAULT '',
		namespace       TEXT NOT NULL DEFAULT 'default',
		node_name       TEXT NOT NULL,
		new_pod_name    TEXT NOT NULL DEFAULT '',
		status          TEXT NOT NULL DEFAULT 'Pending',
		message         TEXT NOT NULL DEFAULT '',
		created_at      TEXT NOT NULL DEFAULT (datetime('now')),
		started_at      TEXT,
		completed_at    TEXT,
		duration_secs   REAL NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS demo_state (
		id               INTEGER PRIMARY KEY CHECK (id = 1),
		phase            TEXT NOT NULL DEFAULT 'IDLE',
		workload_type    TEXT NOT NULL DEFAULT '',
		pod_name         TEXT NOT NULL DEFAULT '',
		node_name        TEXT NOT NULL DEFAULT '',
		message          TEXT NOT NULL DEFAULT '',
		error            TEXT NOT NULL DEFAULT '',
		deploy_duration  REAL NOT NULL DEFAULT 0,
		ckpt_duration    REAL NOT NULL DEFAULT 0,
		restore_duration REAL NOT NULL DEFAULT 0,
		started_at       TEXT,
		checkpoints_json TEXT NOT NULL DEFAULT '[]',
		updated_at       TEXT NOT NULL DEFAULT (datetime('now'))
	);`,

	// Migration 2: retention policies + audit log
	`CREATE TABLE IF NOT EXISTS retention_policies (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		name            TEXT NOT NULL UNIQUE,
		namespace       TEXT NOT NULL DEFAULT '*',
		workload_type   TEXT NOT NULL DEFAULT '*',
		max_count       INTEGER NOT NULL DEFAULT 0,
		max_age_hours   INTEGER NOT NULL DEFAULT 0,
		max_total_bytes INTEGER NOT NULL DEFAULT 0,
		enabled         INTEGER NOT NULL DEFAULT 1,
		created_at      TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS audit_log (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp  TEXT NOT NULL DEFAULT (datetime('now')),
		action     TEXT NOT NULL,
		resource   TEXT NOT NULL,
		resource_id TEXT NOT NULL DEFAULT '',
		actor      TEXT NOT NULL DEFAULT 'system',
		details    TEXT NOT NULL DEFAULT '{}',
		status     TEXT NOT NULL DEFAULT 'success',
		message    TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp);
	CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log(action);
	CREATE INDEX IF NOT EXISTS idx_audit_log_resource ON audit_log(resource, resource_id);`,

	// Migration 3: phase 5d.1 peer-fanout catalog routing.
	//
	// checkpoint_peers tracks which agent nodes have a local copy of
	// each checkpoint. The capture-source node is auto-registered when
	// the checkpoint reaches Completed status (its node IS the first
	// peer). Other agents register themselves after restore-side
	// cascading fetch lands the dump on their local disk; they
	// deregister on local LRU eviction.
	//
	// Stage 5d.2 will add s3_uri/blob_uploaded_at — pre-staged here so
	// the schema is stable; columns stay empty until the blob uploader
	// lands.
	`CREATE TABLE IF NOT EXISTS checkpoint_peers (
		checkpoint_id   TEXT NOT NULL,
		node_name       TEXT NOT NULL,
		agent_url       TEXT NOT NULL,
		registered_at   TEXT NOT NULL DEFAULT (datetime('now')),
		last_seen       TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (checkpoint_id, node_name)
	);
	CREATE INDEX IF NOT EXISTS idx_checkpoint_peers_checkpoint_id ON checkpoint_peers(checkpoint_id);

	ALTER TABLE checkpoints ADD COLUMN s3_uri TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN blob_uploaded_at TEXT NOT NULL DEFAULT '';`,

	// Migration 4: content-addressed catalog (nvsnap#59).
	//
	// Adds the agent's CatalogInfo fields to the catalog row so the
	// server can answer "do you have a checkpoint for image X, model Y,
	// flags Z on driver-major D?" with an indexed SQL query instead of
	// fan-out HTTP scraping of every agent's metadata.json.
	//
	// Indexed columns: hash (exact match for restore-by-hash),
	// image_ref (the primary lookup key — NVCA doesn't know the
	// digest at admission time), driver_major (filtered alongside
	// image_ref for hardware-compatible matches). engine_flags is
	// stored as a sorted JSON array so the canonical-form comparison
	// can stay in SQL (exact-string match on the JSON blob).
	//
	// All columns default to '' / 0; pre-existing rows from
	// migrations 1–3 stay valid and simply return empty fields until
	// the agent re-registers them with the new payload.
	`ALTER TABLE checkpoints ADD COLUMN hash TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN image_ref TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN image_digest TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN model_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN engine_flags TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE checkpoints ADD COLUMN gpu_type TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN gpu_count INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE checkpoints ADD COLUMN driver_version TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN driver_major INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE checkpoints ADD COLUMN cuda_version TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN cpu_architecture TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN function_name TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN function_version_id TEXT NOT NULL DEFAULT '';

	CREATE INDEX IF NOT EXISTS idx_checkpoints_hash ON checkpoints(hash);
	CREATE INDEX IF NOT EXISTS idx_checkpoints_image_ref ON checkpoints(image_ref);
	CREATE INDEX IF NOT EXISTS idx_checkpoints_function_version_id ON checkpoints(function_version_id);`,

	// Migration 5: L2 per-capture PVC (nvsnap#63).
	//
	// Tracks the lifecycle of a per-capture RWX PVC that backs multi-
	// node restore fan-out. Two columns, set ONLY by the
	// PerCapturePVCBackend.Put state machine — never by the agent's
	// register call:
	//
	//   pvc_name           = "" when no L2 attempt has happened (older
	//                        capture, or cluster without RWX SC).
	//                      = "rox-<short-hash>" once snapshot/clone
	//                        is done and the reader PVC is Bound.
	//
	//   pvc_promote_state  = ""              no L2 attempt
	//                      = "pending"       Backend.Put called, Job not yet started
	//                      = "writing"       writer Job mounting rwx-<hash>, copying
	//                      = "snapshotting"  writer succeeded, snapshot + rox-<hash> in flight
	//                      = "ready"         rox-<hash> Bound; restore-side can mount
	//                      = "failed"        writer or snapshot errored; fall back to peer cascade
	//
	// Restore-side polls /api/v1/checkpoints/lookup?hash=<hash> and
	// reads pvc_promote_state to decide L1 → L2-bound → L2-poll-wait →
	// L3-peer → L4-blob (see docs/L2-PVC-CRIU-DESIGN.md §"Restore-side
	// resolver"). The index on pvc_promote_state makes the orphan-GC
	// sweep (find rows stuck in writing/snapshotting past a deadline)
	// a single-pass query.
	`ALTER TABLE checkpoints ADD COLUMN pvc_name TEXT NOT NULL DEFAULT '';
	ALTER TABLE checkpoints ADD COLUMN pvc_promote_state TEXT NOT NULL DEFAULT '';

	CREATE INDEX IF NOT EXISTS idx_checkpoints_pvc_promote_state ON checkpoints(pvc_promote_state);`,

	// Migration 6: enforce content-hash uniqueness (nvsnap#106).
	//
	// A capture's durable identity is its content hash, but the table
	// keyed only on `id` (a free-form string each writer minted
	// differently: the agent used "<pod>-<unixts>", the replication /
	// dedup paths used the raw hash). Same hash + different id = two
	// rows for one physical capture, with no constraint to stop it.
	// Symptoms: inflated catalog/UI, DELETE-by-id leaving siblings,
	// double retention accounting.
	//
	// This migration (a) backfills `hash` for rows that put the hash in
	// `id` but left the column empty, (b) collapses same-hash duplicates
	// keeping the most-complete row, then (c) adds a PARTIAL UNIQUE index
	// so the store can never hold two rows for one hash. Partial
	// (WHERE hash <> '') so genuinely hash-less legacy rows are exempt
	// and don't all collide on ''.
	`UPDATE checkpoints SET hash = id
	  WHERE hash = '' AND length(id) = 64 AND id NOT GLOB '*[^0-9a-fA-F]*';

	DELETE FROM checkpoints WHERE rowid IN (
	  SELECT rowid FROM (
	    SELECT rowid, ROW_NUMBER() OVER (
	      PARTITION BY hash
	      ORDER BY (status = 'Completed') DESC, checkpoint_size DESC, created_at DESC
	    ) AS rn
	    FROM checkpoints WHERE hash <> ''
	  ) WHERE rn > 1
	);

	CREATE UNIQUE INDEX IF NOT EXISTS idx_checkpoints_hash_unique
	  ON checkpoints(hash) WHERE hash <> '';`,
}

// runMigrations applies any pending migrations.
func (d *DB) runMigrations() error {
	// Create version tracking table
	if _, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	var currentVersion int
	row := d.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version")
	if err := row.Scan(&currentVersion); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	for i := currentVersion; i < len(migrations); i++ {
		if _, err := d.db.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := d.db.Exec("INSERT INTO schema_version (version) VALUES (?)", i+1); err != nil {
			return fmt.Errorf("record migration %d: %w", i+1, err)
		}
	}

	return nil
}
