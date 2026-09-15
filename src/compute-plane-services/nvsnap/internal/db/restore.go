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
	"strings"
	"time"
)

// CreateRestore inserts a new restore record.
func (d *DB) CreateRestore(r *Restore) error {
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := d.db.Exec(`INSERT INTO restores
		(id, checkpoint_id, checkpoint_ref, namespace, node_name, new_pod_name, status, message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.CheckpointID, r.CheckpointRef, r.Namespace, r.NodeName,
		r.NewPodName, r.Status, r.Message, now,
	)
	return err
}

// UpdateRestoreStatus updates the status of a restore.
func (d *DB) UpdateRestoreStatus(id, status, newPodName, message string) error {
	now := time.Now().UTC().Format(time.RFC3339)

	var startedAt *string
	if status == "CreatingPod" || status == "Restoring" {
		startedAt = &now
	}
	var completedAt *string
	if status == "Completed" || status == "Failed" {
		completedAt = &now
	}

	// Only set started_at if not already set
	if startedAt != nil {
		_, err := d.db.Exec(`UPDATE restores
			SET status = ?, new_pod_name = ?, message = ?,
			    started_at = COALESCE(started_at, ?), completed_at = ?
			WHERE id = ?`,
			status, newPodName, message, startedAt, completedAt, id,
		)
		return err
	}

	_, err := d.db.Exec(`UPDATE restores
		SET status = ?, new_pod_name = ?, message = ?, completed_at = ?
		WHERE id = ?`,
		status, newPodName, message, completedAt, id,
	)
	return err
}

// GetRestore returns a single restore by ID.
func (d *DB) GetRestore(id string) (*Restore, error) {
	row := d.db.QueryRow(`SELECT
		id, checkpoint_id, checkpoint_ref, namespace, node_name,
		new_pod_name, status, message, created_at, started_at, completed_at, duration_secs
		FROM restores WHERE id = ?`, id)

	var r Restore
	var createdAt string
	var startedAt, completedAt sql.NullString

	err := row.Scan(
		&r.ID, &r.CheckpointID, &r.CheckpointRef, &r.Namespace, &r.NodeName,
		&r.NewPodName, &r.Status, &r.Message, &createdAt, &startedAt, &completedAt, &r.DurationSecs,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("restore not found")
		}
		return nil, err
	}

	r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if startedAt.Valid {
		t, _ := time.Parse(time.RFC3339, startedAt.String)
		r.StartedAt = &t
	}
	if completedAt.Valid {
		t, _ := time.Parse(time.RFC3339, completedAt.String)
		r.CompletedAt = &t
	}

	return &r, nil
}

// ListRestores returns all restore records, newest first.
func (d *DB) ListRestores() ([]Restore, error) {
	return d.ListRestoresFiltered(RestoreFilter{})
}

// ListRestoresFiltered returns restore records matching the given filter with pagination.
func (d *DB) ListRestoresFiltered(f RestoreFilter) ([]Restore, error) {
	query := `SELECT
		id, checkpoint_id, checkpoint_ref, namespace, node_name,
		new_pod_name, status, message, created_at, started_at, completed_at, duration_secs
		FROM restores`

	var conditions []string
	var args []any

	if f.Namespace != "" {
		conditions = append(conditions, "namespace = ?")
		args = append(args, f.Namespace)
	}
	if f.CheckpointID != "" {
		conditions = append(conditions, "checkpoint_id = ?")
		args = append(args, f.CheckpointID)
	}
	if f.NodeName != "" {
		conditions = append(conditions, "node_name = ?")
		args = append(args, f.NodeName)
	}
	if f.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, f.Status)
	}
	if f.Cursor != "" {
		conditions = append(conditions, "created_at < ?")
		args = append(args, f.Cursor)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []Restore
	for rows.Next() {
		var r Restore
		var createdAt string
		var startedAt, completedAt sql.NullString

		err := rows.Scan(
			&r.ID, &r.CheckpointID, &r.CheckpointRef, &r.Namespace, &r.NodeName,
			&r.NewPodName, &r.Status, &r.Message, &createdAt, &startedAt, &completedAt, &r.DurationSecs,
		)
		if err != nil {
			return nil, err
		}

		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if startedAt.Valid {
			t, _ := time.Parse(time.RFC3339, startedAt.String)
			r.StartedAt = &t
		}
		if completedAt.Valid {
			t, _ := time.Parse(time.RFC3339, completedAt.String)
			r.CompletedAt = &t
		}

		result = append(result, r)
	}
	return result, rows.Err()
}
