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
	"time"
)

// LoadDemoState loads the singleton demo state row. Returns nil if no state saved yet.
func (d *DB) LoadDemoState() (*DemoState, error) {
	row := d.db.QueryRow(`SELECT
		phase, workload_type, pod_name, node_name, message, error,
		deploy_duration, ckpt_duration, restore_duration,
		started_at, checkpoints_json
		FROM demo_state WHERE id = 1`)

	var s DemoState
	var startedAt sql.NullString

	err := row.Scan(
		&s.Phase, &s.WorkloadType, &s.PodName, &s.NodeName, &s.Message, &s.Error,
		&s.DeployDuration, &s.CkptDuration, &s.RestoreDuration,
		&startedAt, &s.CheckpointsJSON,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if startedAt.Valid {
		s.StartedAt = &startedAt.String
	}

	return &s, nil
}

// SaveDemoState upserts the singleton demo state row.
func (d *DB) SaveDemoState(s *DemoState) error {
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := d.db.Exec(`INSERT INTO demo_state
		(id, phase, workload_type, pod_name, node_name, message, error,
		 deploy_duration, ckpt_duration, restore_duration,
		 started_at, checkpoints_json, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		 phase = excluded.phase,
		 workload_type = excluded.workload_type,
		 pod_name = excluded.pod_name,
		 node_name = excluded.node_name,
		 message = excluded.message,
		 error = excluded.error,
		 deploy_duration = excluded.deploy_duration,
		 ckpt_duration = excluded.ckpt_duration,
		 restore_duration = excluded.restore_duration,
		 started_at = excluded.started_at,
		 checkpoints_json = excluded.checkpoints_json,
		 updated_at = excluded.updated_at`,
		s.Phase, s.WorkloadType, s.PodName, s.NodeName, s.Message, s.Error,
		s.DeployDuration, s.CkptDuration, s.RestoreDuration,
		s.StartedAt, s.CheckpointsJSON, now,
	)
	return err
}
