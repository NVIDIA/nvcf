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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// adoptIDByHash repoints c.ID to an existing row that already owns
// c.Hash, so an id-keyed upsert updates the canonical row instead of
// inserting a duplicate sibling (nvsnap#106). No-op when the hash is
// empty or not yet seen.
func (d *DB) adoptIDByHash(c *Checkpoint) error {
	if c.Hash == "" {
		return nil
	}
	var existingID string
	switch err := d.db.QueryRow(
		"SELECT id FROM checkpoints WHERE hash = ? LIMIT 1", c.Hash,
	).Scan(&existingID); {
	case err == nil:
		c.ID = existingID
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return nil
	default:
		return err
	}
}

// isHashUniqueViolation reports whether err is the partial UNIQUE(hash)
// index rejecting a second row for one content hash (the nvsnap#106
// backstop). modernc/sqlite renders it as "...UNIQUE constraint failed:
// checkpoints.hash...".
func isHashUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "checkpoints.hash")
}

// CreateCheckpoint inserts a new checkpoint record.
func (d *DB) CreateCheckpoint(c *Checkpoint) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	normalizeCheckpointHash(c)
	now := c.CreatedAt.UTC().Format(time.RFC3339Nano)
	var startedAt *string
	if c.StartedAt != nil {
		s := c.StartedAt.UTC().Format(time.RFC3339)
		startedAt = &s
	}

	_, err := d.db.Exec(`INSERT INTO checkpoints
		(id, checkpoint_id, namespace, pod_name, container_name, container_image,
		 node_name, checkpoint_path, checkpoint_size, status, message,
		 has_gpu, model_name, workload_type, created_at, started_at, duration_secs)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.CheckpointID, c.Namespace, c.PodName, c.ContainerName, c.ContainerImage,
		c.NodeName, c.CheckpointPath, c.CheckpointSize, c.Status, c.Message,
		boolToInt(c.HasGPU), c.ModelName, c.WorkloadType, now, startedAt, c.DurationSecs,
	)
	return err
}

// UpsertCheckpoint inserts a checkpoint row or updates an existing one
// (by ID). Used by the agent's post-capture registration path —
// agents creating checkpoints directly (without going through
// nvsnap-server's API) need a way to register the row idempotently so
// peer-add / blob-uploaded / /sources all work afterward.
//
// Behavior:
//   - On INSERT: created_at = now, started_at = now (treat as just-created).
//   - On UPDATE: status/size/path/duration/completed_at fields are
//     refreshed; created_at and started_at are PRESERVED so we don't
//     rewrite history. CatalogInfo fields (Hash, ImageRef, etc.)
//     refresh on every upsert so a re-register can fix up partially
//     populated rows from older agents.
//
// Idempotent across agent restarts and rolling deploys.
func (d *DB) UpsertCheckpoint(c *Checkpoint) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	normalizeCheckpointHash(c)
	// Content-addressed convergence (nvsnap#106): a capture's identity is
	// its content hash. If a row for this hash already exists under a
	// different id, adopt that id so the ON CONFLICT(id) upsert below
	// updates the canonical row instead of inserting a duplicate sibling.
	if err := d.adoptIDByHash(c); err != nil {
		return err
	}
	now := c.CreatedAt.UTC().Format(time.RFC3339Nano)
	var startedAt *string
	if c.StartedAt != nil {
		s := c.StartedAt.UTC().Format(time.RFC3339)
		startedAt = &s
	}
	var completedAt *string
	if c.CompletedAt != nil {
		s := c.CompletedAt.UTC().Format(time.RFC3339)
		completedAt = &s
	}

	// Derive DriverMajor from DriverVersion if caller didn't set it
	// (mirrors the agent's computeHash logic — first dot-separated
	// segment, parsed best-effort, 0 on failure).
	if c.DriverMajor == 0 && c.DriverVersion != "" {
		c.DriverMajor = parseDriverMajor(c.DriverVersion)
	}
	// Engine flags are stored as a canonical-order JSON array so SQL
	// can exact-string-match them in the lookup query.
	flagsJSON := canonicalizeEngineFlagsJSON(c.EngineFlags)

	const upsertSQL = `INSERT INTO checkpoints
		(id, checkpoint_id, namespace, pod_name, container_name, container_image,
		 node_name, checkpoint_path, checkpoint_size, status, message,
		 has_gpu, model_name, workload_type, created_at, started_at,
		 completed_at, duration_secs,
		 hash, image_ref, image_digest, model_id, engine_flags,
		 gpu_type, gpu_count, driver_version, driver_major,
		 cuda_version, cpu_architecture, function_name, function_version_id,
		 pvc_name, pvc_promote_state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		        ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			namespace           = excluded.namespace,
			pod_name            = excluded.pod_name,
			container_name      = excluded.container_name,
			container_image     = excluded.container_image,
			node_name           = excluded.node_name,
			checkpoint_path     = excluded.checkpoint_path,
			checkpoint_size     = excluded.checkpoint_size,
			status              = excluded.status,
			message             = excluded.message,
			has_gpu             = excluded.has_gpu,
			model_name          = excluded.model_name,
			workload_type       = excluded.workload_type,
			completed_at        = excluded.completed_at,
			duration_secs       = excluded.duration_secs,
			hash                = excluded.hash,
			image_ref           = excluded.image_ref,
			image_digest        = excluded.image_digest,
			model_id            = excluded.model_id,
			engine_flags        = excluded.engine_flags,
			gpu_type            = excluded.gpu_type,
			gpu_count           = excluded.gpu_count,
			driver_version      = excluded.driver_version,
			driver_major        = excluded.driver_major,
			cuda_version        = excluded.cuda_version,
			cpu_architecture    = excluded.cpu_architecture,
			function_name       = excluded.function_name,
			function_version_id = excluded.function_version_id,
			-- L2 columns (nvsnap#63) are NOT overwritten on Upsert.
			-- The agent's register call doesn't know the PVC state;
			-- only the PerCapturePVCBackend's Put state machine drives
			-- those. Use UpdatePVCPromoteStateByHash below for that path.
			pvc_name            = checkpoints.pvc_name,
			pvc_promote_state   = checkpoints.pvc_promote_state`
	// Retry once on the UNIQUE(hash) backstop: a concurrent writer may
	// have inserted this hash between adoptIDByHash and the Exec. Re-adopt
	// the now-existing id and replay as an update.
	for attempt := 0; ; attempt++ {
		_, err := d.db.Exec(upsertSQL,
			c.ID, c.CheckpointID, c.Namespace, c.PodName, c.ContainerName, c.ContainerImage,
			c.NodeName, c.CheckpointPath, c.CheckpointSize, c.Status, c.Message,
			boolToInt(c.HasGPU), c.ModelName, c.WorkloadType, now, startedAt,
			completedAt, c.DurationSecs,
			c.Hash, c.ImageRef, c.ImageDigest, c.ModelID, flagsJSON,
			c.GPUType, c.GPUCount, c.DriverVersion, c.DriverMajor,
			c.CUDAVersion, c.CPUArchitecture, c.FunctionName, c.FunctionVersionID,
			c.PVCName, c.PVCPromoteState,
		)
		if err == nil {
			return nil
		}
		if attempt == 0 && c.Hash != "" && isHashUniqueViolation(err) {
			if aerr := d.adoptIDByHash(c); aerr == nil {
				continue
			}
		}
		return err
	}
}

// UpdatePVCPromoteStateByHash advances the L2 per-capture PVC state
// machine for every catalog row that shares the given content hash
// (nvsnap#63 / nvsnap#76). Called only by the PerCapturePVCBackend.Put
// state machine via the nvsnap-server HTTP endpoint.
//
// The L2 artifact (rox-<short-hash> PVC) is hash-keyed, not id-keyed:
// the Backend's Lease, PVC names, and snapshot all derive from the
// content hash. Multiple capture rows can share a hash (re-capture of
// the same workload), and they all reference the same L2 artifact —
// so the state-machine write fans out across every row with that
// hash. Returns the number of rows touched so the HTTP handler can
// return 404 when no row matches.
//
// pvcName is passed through verbatim — set non-empty when the row
// reaches "ready" (i.e. the rox-<hash> PVC is Bound and the catalog
// row should advertise it to restore-side resolvers). Empty pvcName
// is preserved (UPDATE only touches the column when a non-empty value
// is supplied, matching the COALESCE semantics nvsnap#61 introduced).
func (d *DB) UpdatePVCPromoteStateByHash(hash, state, pvcName string) (int64, error) {
	res, err := d.db.Exec(`UPDATE checkpoints
		SET pvc_promote_state = ?,
		    pvc_name          = COALESCE(NULLIF(?, ''), pvc_name)
		WHERE hash = ?`,
		state, pvcName, hash,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetPVCPromoteStateByHash returns the L2 promote state + PVC name
// for the most recent capture of the given content hash (nvsnap#147).
// Used by the nvsnap-init container at restore-pod admission time to
// poll until the rox-<hash> PVC is ready before exec'ing the
// inference container.
//
// Hash → many rows is normal (re-capture of the same workload), and
// UpdatePVCPromoteStateByHash fans the write across all of them, so
// the "most recent" row reflects the canonical state. created_at
// DESC is the tiebreaker.
//
// Returns sql.ErrNoRows when no row exists for hash. The HTTP handler
// translates that to 404 so nvsnap-init can distinguish "not yet
// registered" from "registered but not ready".
//
// state is one of {pending, writing, snapshotting, ready, failed}
// (see migrations.go) plus the empty string when the agent never
// wrote any state (L2 disabled / failed before first write).
// pvcName is the rox-<short-hash> PVC name, populated only on the
// "ready" transition.
func (d *DB) GetPVCPromoteStateByHash(hash string) (state, pvcName string, err error) {
	err = d.db.QueryRow(`SELECT pvc_promote_state, pvc_name
		FROM checkpoints
		WHERE hash = ?
		ORDER BY created_at DESC
		LIMIT 1`,
		hash,
	).Scan(&state, &pvcName)
	return state, pvcName, err
}

// canonicalizeEngineFlagsJSON returns a JSON-array string of the
// flags with --model / --model-path tokens stripped and the remaining
// args sorted. The format matches the agent's canonicalArgs in
// internal/agent/catalog.go so two captures that pass the same flags
// in different orders produce byte-identical strings — which makes
// SQL exact-match a valid canonical comparison.
func canonicalizeEngineFlagsJSON(flags []string) string {
	out := canonicalizeEngineFlags(flags)
	b, _ := json.Marshal(out)
	return string(b)
}

// canonicalizeEngineFlags is the pure-slice form used by both write
// path (UpsertCheckpoint) and lookup query construction.
func canonicalizeEngineFlags(flags []string) []string {
	if len(flags) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(flags))
	skip := false
	for _, a := range flags {
		if skip {
			skip = false
			continue
		}
		if a == "--model" || a == "--model-path" {
			skip = true
			continue
		}
		if strings.HasPrefix(a, "--model=") || strings.HasPrefix(a, "--model-path=") {
			continue
		}
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// parseDriverMajor returns the first dot-separated integer segment of
// a driver-version string ("550.90.07" -> 550). Returns 0 on parse
// failure so rows without driver info stay groupable.
func parseDriverMajor(v string) int {
	if v == "" {
		return 0
	}
	parts := strings.SplitN(v, ".", 2)
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	return n
}

// UpdateCheckpointStatus updates status fields of an existing checkpoint.
//
// hash is optional (empty string = leave existing value untouched). This
// path is hit on the nvsnap-server's runCheckpoint completion callback,
// which is the *only* place the content hash from the agent's response
// can land on the DB row — registerCheckpointInCatalog runs on the
// agent side and lands a row keyed by the agent's local id (the
// hash-prefixed dir name), while the row created at checkpoint-start
// is keyed by the nvsnap-server CRD name. Without writing hash here, the
// CRD-keyed row stays hash-less and NVCA's Hook B sees Completed +
// empty hash (nvca#15) → retry storm (nvsnap#61, nvca#15).
//
// COALESCE preserves the prior value if the caller passes empty —
// older callers compile unchanged via the wrapper at the bottom of
// this file. New callers should pass the hash from the agent's
// CheckpointResult.
func (d *DB) UpdateCheckpointStatus(id, status, message, checkpointPath, hash string, checkpointSize int64, durationSecs float64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var completedAt *string
	if status == "Completed" || status == "Failed" {
		completedAt = &now
	}
	// Content-addressed convergence (nvsnap#106): completing this row sets
	// its hash. If a different row already owns that hash, this row is a
	// redundant capture of an existing artifact — fold the completion into
	// the canonical row and drop this stub, rather than colliding on the
	// UNIQUE(hash) backstop.
	if hash != "" {
		var otherID string
		switch err := d.db.QueryRow(
			"SELECT id FROM checkpoints WHERE hash = ? AND id <> ? LIMIT 1", hash, id,
		).Scan(&otherID); {
		case err == nil:
			if _, uerr := d.db.Exec(`UPDATE checkpoints
				SET status          = ?,
				    message         = ?,
				    checkpoint_path = COALESCE(NULLIF(?, ''), checkpoint_path),
				    checkpoint_size = MAX(checkpoint_size, ?),
				    duration_secs   = ?,
				    completed_at    = COALESCE(?, completed_at)
				WHERE id = ?`,
				status, message, checkpointPath, checkpointSize, durationSecs, completedAt, otherID,
			); uerr != nil {
				return uerr
			}
			_, derr := d.db.Exec("DELETE FROM checkpoints WHERE id = ?", id)
			return derr
		case errors.Is(err, sql.ErrNoRows):
			// no canonical row for this hash — fall through to normal update
		default:
			return err
		}
	}
	_, err := d.db.Exec(`UPDATE checkpoints
		SET status          = ?,
		    message         = ?,
		    checkpoint_path = ?,
		    checkpoint_size = ?,
		    duration_secs   = ?,
		    completed_at    = ?,
		    hash            = COALESCE(NULLIF(?, ''), hash)
		WHERE id = ?`,
		status, message, checkpointPath, checkpointSize, durationSecs, completedAt, hash, id,
	)
	return err
}

// GetCheckpoint returns a single checkpoint by ID.
func (d *DB) GetCheckpoint(id string) (*Checkpoint, error) {
	row := d.db.QueryRow(`SELECT `+checkpointColumns+`
		FROM checkpoints WHERE id = ?`, id)
	c, err := scanCheckpoint(row)
	if err != nil {
		return c, err
	}
	// Legacy rows (ingested by the rootfs Reconciler, which sets id=hash
	// but leaves the hash column empty) read back with Hash=="". Derive
	// it from the id — same rule CreateCheckpoint applies on write — so
	// every consumer (esp. the delete cascade's hash-keyed CM/L2/sibling
	// cleanup) sees a usable hash and doesn't fall back to the id-only
	// path that leaves the capture manifest CM behind (resurrection bug).
	normalizeCheckpointHash(c)
	return c, nil
}

// LookupCheckpoints returns Completed checkpoints whose canonical
// content matches the given criteria, freshest first. Used by NVCA's
// Hook A (via the /api/v1/checkpoints/lookup endpoint) to find a
// restorable artifact for a pod that has no fvID-keyed entry yet.
//
// Match semantics (nvsnap#59):
//   - ImageRef: required, exact string match (indexed)
//   - ModelID: if non-empty, exact match; if empty, match any
//   - EngineFlags: canonicalized server-side (sorted, --model* stripped),
//     compared as the JSON-array string stored on the row
//   - DriverMajor: if > 0, exact match; if 0, match any
//   - Hash: only rows with non-empty Hash are returned (a restorable
//     artifact must have a content hash, otherwise NVCA can't stamp
//     nvsnap.io/restore-from)
//
// Limit clamps to [1, 100], default 10.
func (d *DB) LookupCheckpoints(c LookupCriteria) ([]Checkpoint, error) {
	if c.ImageRef == "" {
		return nil, fmt.Errorf("LookupCheckpoints: ImageRef is required")
	}
	limit := c.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	conditions := []string{
		"status = 'Completed'",
		"hash != ''",
		"image_ref = ?",
	}
	args := []interface{}{c.ImageRef}

	if c.ModelID != "" {
		conditions = append(conditions, "model_id = ?")
		args = append(args, c.ModelID)
	}
	if c.DriverMajor > 0 {
		conditions = append(conditions, "driver_major = ?")
		args = append(args, c.DriverMajor)
	}
	// engine_flags is stored as canonical JSON; the request's flags
	// are canonicalized the same way and compared as a JSON string.
	// Passing an empty slice matches rows that also have no flags
	// (their stored value is "[]"), which is what we want — "no
	// flags constraint" must require the row to also have no flags,
	// otherwise we'd let through rows that aren't actually compatible.
	flagsJSON := canonicalizeEngineFlagsJSON(c.EngineFlags)
	conditions = append(conditions, "engine_flags = ?")
	args = append(args, flagsJSON)

	// #nosec G202 -- checkpointColumns is a compile-time const column list
	// and the WHERE clause is fixed predicates; all user values are bound
	// via placeholders in args, never concatenated into the query string.
	query := "SELECT " + checkpointColumns +
		" FROM checkpoints WHERE " + strings.Join(conditions, " AND ") +
		" ORDER BY created_at DESC LIMIT " + strconv.Itoa(limit)

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Checkpoint
	for rows.Next() {
		cp, err := scanCheckpointRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cp)
	}
	return out, rows.Err()
}

// checkpointColumns is the canonical column list for SELECT statements
// on the checkpoints table. Keeping it as a constant ensures the
// scanner ordering (in scanCheckpoint / scanCheckpointRow) stays in
// sync with what we read. Field order matches the migration's CREATE
// TABLE + ALTER TABLE order.
const checkpointColumns = `
	id, checkpoint_id, namespace, pod_name, container_name, container_image,
	node_name, checkpoint_path, checkpoint_size, status, message,
	has_gpu, model_name, workload_type, created_at, started_at, completed_at, duration_secs,
	hash, image_ref, image_digest, model_id, engine_flags,
	gpu_type, gpu_count, driver_version, driver_major,
	cuda_version, cpu_architecture, function_name, function_version_id,
	pvc_name, pvc_promote_state`

// ListCheckpoints returns checkpoints matching the given filter with pagination.
func (d *DB) ListCheckpoints(f CheckpointFilter) ([]Checkpoint, error) {
	paged, err := d.ListCheckpointsPaged(f)
	if err != nil {
		return nil, err
	}
	return paged.Items, nil
}

// ListCheckpointsDeduped returns the catalog collapsed to one row per capture
// (by content hash), most-complete row winning, newest-first. This is the
// tier-agnostic "all captures" view the UI consumes — it hides the historical
// double-write (a pod-id row + a hash-id row per rootfs capture) so N captures
// show as N rows, not 2N. f.Limit caps the pre-dedupe fetch; pass a high value
// (the catalog is small) to dedupe the whole set.
func (d *DB) ListCheckpointsDeduped(f CheckpointFilter) ([]Checkpoint, error) {
	if f.Limit <= 0 {
		f.Limit = 1000
	}
	rows, err := d.ListCheckpoints(f)
	if err != nil {
		return nil, err
	}
	return DedupeCheckpointsByHash(rows), nil
}

// ListCheckpointsPaged returns checkpoints with pagination metadata.
func (d *DB) ListCheckpointsPaged(f CheckpointFilter) (*PagedResult[Checkpoint], error) {
	conditions, args := buildCheckpointConditions(f)

	// Count total matching records (without cursor — total is the full filtered count)
	countQuery := "SELECT COUNT(*) FROM checkpoints"
	if len(conditions) > 0 {
		countQuery += " WHERE " + strings.Join(conditions, " AND ")
	}
	var total int
	if err := d.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, err
	}

	sortOrder := "DESC"
	if f.SortOrder == "asc" {
		sortOrder = "ASC"
	}

	// Add cursor condition for pagination
	if f.Cursor != "" {
		op := "<"
		if sortOrder == "ASC" {
			op = ">"
		}
		conditions = append(conditions, "created_at "+op+" ?")
		args = append(args, f.Cursor)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	query := "SELECT " + checkpointColumns + " FROM checkpoints"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at " + sortOrder
	query += fmt.Sprintf(" LIMIT %d", limit+1)

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []Checkpoint
	for rows.Next() {
		c, err := scanCheckpointRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	var nextCursor string
	if hasMore && len(items) > 0 {
		nextCursor = items[len(items)-1].CreatedAt.UTC().Format(time.RFC3339Nano)
	}

	return &PagedResult[Checkpoint]{
		Items:      items,
		Total:      total,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}, nil
}

func buildCheckpointConditions(f CheckpointFilter) (conditions []string, args []interface{}) {
	if f.Namespace != "" {
		conditions = append(conditions, "namespace = ?")
		args = append(args, f.Namespace)
	}
	if f.PodName != "" {
		conditions = append(conditions, "pod_name = ?")
		args = append(args, f.PodName)
	}
	if f.NodeName != "" {
		conditions = append(conditions, "node_name = ?")
		args = append(args, f.NodeName)
	}
	if f.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, f.Status)
	}
	if f.WorkloadType != "" {
		conditions = append(conditions, "workload_type = ?")
		args = append(args, f.WorkloadType)
	}
	if f.HasGPU != nil {
		conditions = append(conditions, "has_gpu = ?")
		args = append(args, boolToInt(*f.HasGPU))
	}

	return conditions, args
}

// DeleteCheckpoint removes a checkpoint record by ID.
func (d *DB) DeleteCheckpoint(id string) error {
	_, err := d.db.Exec("DELETE FROM checkpoints WHERE id = ?", id)
	return err
}

// ListByHash returns every catalog row sharing a content hash. Used
// by the deleteCheckpoint hash-keyed cascade (nvsnap#137): a single
// DELETE call must clean up every sibling row that points at the
// same underlying L2/blob artifacts, otherwise NVCA's Hook A
// content-addressed lookup still resolves a survivor and stamps
// restore-from on new pods with a hash whose bytes are gone.
//
// Rows are returned newest-first (created_at DESC). Empty hash
// returns nil/nil — defensively skipping a query that would match
// every row whose Hash column is NULL/empty.
func (d *DB) ListByHash(hash string) ([]Checkpoint, error) {
	if hash == "" {
		return nil, nil
	}
	rows, err := d.db.Query(
		"SELECT "+checkpointColumns+" FROM checkpoints WHERE hash = ? ORDER BY created_at DESC",
		hash,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Checkpoint
	for rows.Next() {
		c, err := scanCheckpointRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// DeleteByHash removes every catalog row sharing a content hash and
// returns the number of rows deleted. Paired with ListByHash for the
// cascade-delete flow (nvsnap#137).
//
// Caller MUST do the artifact cleanup (PVCs, snapshot, CRD, blobs,
// agent host paths) BEFORE calling this — once the rows are gone we
// no longer know the agent ids or source namespace needed to find
// those artifacts.
func (d *DB) DeleteByHash(hash string) (int64, error) {
	if hash == "" {
		return 0, nil
	}
	// Match the hash COLUMN or the id. Legacy rows ingested by the rootfs
	// Reconciler set id=hash but left the hash column empty, so a column-
	// only match deletes 0 rows and the catalog row lingers forever (the
	// row never disappears from the UI). The id is the content hash for
	// those rows, so "hash = ? OR id = ?" also catches them.
	res, err := d.db.Exec("DELETE FROM checkpoints WHERE hash = ? OR id = ?", hash, hash)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// checkpointScratch holds the raw-form values of columns whose
// in-struct representation differs from their SQL storage (booleans
// stored as ints, times as strings, engine_flags as a JSON array).
// Scoped to the scanner so it doesn't pollute Checkpoint.
type checkpointScratch struct {
	hasGPU          int
	createdAt       string
	startedAt       sql.NullString
	completedAt     sql.NullString
	engineFlagsJSON string
}

func checkpointScanDest(c *Checkpoint, s *checkpointScratch) []interface{} {
	return []interface{}{
		&c.ID, &c.CheckpointID, &c.Namespace, &c.PodName, &c.ContainerName, &c.ContainerImage,
		&c.NodeName, &c.CheckpointPath, &c.CheckpointSize, &c.Status, &c.Message,
		&s.hasGPU, &c.ModelName, &c.WorkloadType,
		&s.createdAt, &s.startedAt, &s.completedAt, &c.DurationSecs,
		&c.Hash, &c.ImageRef, &c.ImageDigest, &c.ModelID, &s.engineFlagsJSON,
		&c.GPUType, &c.GPUCount, &c.DriverVersion, &c.DriverMajor,
		&c.CUDAVersion, &c.CPUArchitecture, &c.FunctionName, &c.FunctionVersionID,
		&c.PVCName, &c.PVCPromoteState,
	}
}

func finalizeCheckpoint(c *Checkpoint, s *checkpointScratch) {
	c.HasGPU = s.hasGPU != 0
	c.CreatedAt, _ = parseTime(s.createdAt)
	if s.startedAt.Valid {
		t, _ := time.Parse(time.RFC3339, s.startedAt.String)
		c.StartedAt = &t
	}
	if s.completedAt.Valid {
		t, _ := time.Parse(time.RFC3339, s.completedAt.String)
		c.CompletedAt = &t
	}
	if s.engineFlagsJSON != "" {
		var flags []string
		if err := json.Unmarshal([]byte(s.engineFlagsJSON), &flags); err == nil {
			c.EngineFlags = flags
		}
	}
}

// scanCheckpoint scans a single row into a Checkpoint. Column order
// MUST match checkpointColumns above.
func scanCheckpoint(row *sql.Row) (*Checkpoint, error) {
	var c Checkpoint
	var s checkpointScratch
	err := row.Scan(checkpointScanDest(&c, &s)...)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("checkpoint not found")
		}
		return nil, err
	}
	finalizeCheckpoint(&c, &s)
	return &c, nil
}

// scanCheckpointRow scans from *sql.Rows (list queries). Same column
// order as scanCheckpoint.
func scanCheckpointRow(rows *sql.Rows) (*Checkpoint, error) {
	var c Checkpoint
	var s checkpointScratch
	if err := rows.Scan(checkpointScanDest(&c, &s)...); err != nil {
		return nil, err
	}
	finalizeCheckpoint(&c, &s)
	return &c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// parseTime tries RFC3339Nano first, then RFC3339, then SQLite datetime format.
func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02 15:04:05", s)
}
