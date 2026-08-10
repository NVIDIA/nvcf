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
	"strings"
	"time"
)

// LogAudit inserts an audit trail entry.
func (d *DB) LogAudit(e *AuditEntry) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if e.Details == "" {
		e.Details = "{}"
	}
	if e.Status == "" {
		e.Status = "success"
	}
	result, err := d.db.Exec(`INSERT INTO audit_log
		(timestamp, action, resource, resource_id, actor, details, status, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		now, e.Action, e.Resource, e.ResourceID, e.Actor, e.Details, e.Status, e.Message,
	)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	e.ID = id
	e.Timestamp, _ = parseTime(now)
	return nil
}

// ListAuditLog returns audit entries matching the given filter.
func (d *DB) ListAuditLog(f AuditFilter) ([]AuditEntry, error) {
	query := `SELECT id, timestamp, action, resource, resource_id, actor, details, status, message
		FROM audit_log`

	var conditions []string
	var args []any

	if f.Action != "" {
		conditions = append(conditions, "action = ?")
		args = append(args, f.Action)
	}
	if f.Resource != "" {
		conditions = append(conditions, "resource = ?")
		args = append(args, f.Resource)
	}
	if f.ResourceID != "" {
		conditions = append(conditions, "resource_id = ?")
		args = append(args, f.ResourceID)
	}
	if f.Actor != "" {
		conditions = append(conditions, "actor = ?")
		args = append(args, f.Actor)
	}
	if f.Since != "" {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, f.Since)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY timestamp DESC"

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

	var result []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Action, &e.Resource, &e.ResourceID,
			&e.Actor, &e.Details, &e.Status, &e.Message); err != nil {
			return nil, err
		}
		e.Timestamp, _ = parseTime(ts)
		result = append(result, e)
	}
	return result, rows.Err()
}
