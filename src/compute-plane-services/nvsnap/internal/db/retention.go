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

// CreateRetentionPolicy inserts a new retention policy.
func (d *DB) CreateRetentionPolicy(p *RetentionPolicy) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := d.db.Exec(`INSERT INTO retention_policies
		(name, namespace, workload_type, max_count, max_age_hours, max_total_bytes, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Namespace, p.WorkloadType, p.MaxCount, p.MaxAgeHours, p.MaxTotalBytes,
		boolToInt(p.Enabled), now, now,
	)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	p.ID = id
	return nil
}

// UpdateRetentionPolicy updates an existing retention policy.
func (d *DB) UpdateRetentionPolicy(p *RetentionPolicy) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`UPDATE retention_policies
		SET namespace = ?, workload_type = ?, max_count = ?, max_age_hours = ?,
		    max_total_bytes = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		p.Namespace, p.WorkloadType, p.MaxCount, p.MaxAgeHours, p.MaxTotalBytes,
		boolToInt(p.Enabled), now, p.ID,
	)
	return err
}

// GetRetentionPolicy returns a single retention policy by ID.
func (d *DB) GetRetentionPolicy(id int64) (*RetentionPolicy, error) {
	row := d.db.QueryRow(`SELECT id, name, namespace, workload_type,
		max_count, max_age_hours, max_total_bytes, enabled, created_at, updated_at
		FROM retention_policies WHERE id = ?`, id)
	return scanRetentionPolicy(row)
}

// ListRetentionPolicies returns all retention policies.
func (d *DB) ListRetentionPolicies() ([]RetentionPolicy, error) {
	rows, err := d.db.Query(`SELECT id, name, namespace, workload_type,
		max_count, max_age_hours, max_total_bytes, enabled, created_at, updated_at
		FROM retention_policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []RetentionPolicy
	for rows.Next() {
		var p RetentionPolicy
		var enabled int
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Namespace, &p.WorkloadType,
			&p.MaxCount, &p.MaxAgeHours, &p.MaxTotalBytes, &enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.Enabled = enabled != 0
		p.CreatedAt, _ = parseTime(createdAt)
		p.UpdatedAt, _ = parseTime(updatedAt)
		result = append(result, p)
	}
	return result, rows.Err()
}

// DeleteRetentionPolicy removes a retention policy by ID.
func (d *DB) DeleteRetentionPolicy(id int64) error {
	_, err := d.db.Exec("DELETE FROM retention_policies WHERE id = ?", id)
	return err
}

// FindExpiredCheckpoints returns checkpoint IDs that violate the given policy.
func (d *DB) FindExpiredCheckpoints(p *RetentionPolicy) ([]Checkpoint, error) {
	var allExpired []Checkpoint

	nsCondition := ""
	var nsArgs []any
	if p.Namespace != "*" {
		nsCondition = " AND namespace = ?"
		nsArgs = append(nsArgs, p.Namespace)
	}
	wtCondition := ""
	var wtArgs []any
	if p.WorkloadType != "*" {
		wtCondition = " AND workload_type = ?"
		wtArgs = append(wtArgs, p.WorkloadType)
	}

	baseCondition := "status = 'Completed'" + nsCondition + wtCondition
	baseArgs := make([]any, 0, len(nsArgs)+len(wtArgs))
	baseArgs = append(baseArgs, nsArgs...)
	baseArgs = append(baseArgs, wtArgs...)

	// Max age: find checkpoints older than max_age_hours
	if p.MaxAgeHours > 0 {
		cutoff := time.Now().UTC().Add(-time.Duration(p.MaxAgeHours) * time.Hour).Format(time.RFC3339)
		// #nosec G201 -- checkpointColumns is a const; baseCondition is a
		// fixed predicate with ? placeholders; all values bound via args.
		query := fmt.Sprintf("SELECT "+checkpointColumns+
			" FROM checkpoints WHERE %s AND created_at < ? ORDER BY created_at ASC", baseCondition)
		args := make([]any, 0, len(baseArgs)+1)
		args = append(args, baseArgs...)
		args = append(args, cutoff)
		rows, err := d.db.Query(query, args...)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			c, err := scanCheckpointRow(rows)
			if err != nil {
				return nil, err
			}
			allExpired = append(allExpired, *c)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// Max count: keep only the newest maxCount, expire the rest
	if p.MaxCount > 0 {
		// #nosec G201 -- checkpointColumns is a const; baseCondition is a
		// fixed predicate with ? placeholders; all values bound via args.
		query := fmt.Sprintf("SELECT "+checkpointColumns+
			" FROM checkpoints WHERE %s ORDER BY created_at DESC LIMIT -1 OFFSET ?", baseCondition)
		args := make([]any, 0, len(baseArgs)+1)
		args = append(args, baseArgs...)
		args = append(args, p.MaxCount)
		rows, err := d.db.Query(query, args...)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			c, err := scanCheckpointRow(rows)
			if err != nil {
				return nil, err
			}
			allExpired = append(allExpired, *c)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// Deduplicate by ID
	seen := make(map[string]bool)
	var unique []Checkpoint
	for i := range allExpired {
		c := allExpired[i]
		if !seen[c.ID] {
			seen[c.ID] = true
			unique = append(unique, c)
		}
	}

	return unique, nil
}

func scanRetentionPolicy(row *sql.Row) (*RetentionPolicy, error) {
	var p RetentionPolicy
	var enabled int
	var createdAt, updatedAt string
	err := row.Scan(&p.ID, &p.Name, &p.Namespace, &p.WorkloadType,
		&p.MaxCount, &p.MaxAgeHours, &p.MaxTotalBytes, &enabled, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("retention policy not found")
		}
		return nil, err
	}
	p.Enabled = enabled != 0
	p.CreatedAt, _ = parseTime(createdAt)
	p.UpdatedAt, _ = parseTime(updatedAt)
	return &p, nil
}
