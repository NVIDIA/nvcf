/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package api

import (
	"context"
	"net/http"
	"time"
)

func (c *Sys) Leader() (*LeaderResponse, error) {
	return c.LeaderWithContext(context.Background())
}

func (c *Sys) LeaderWithContext(ctx context.Context) (*LeaderResponse, error) {
	ctx, cancelFunc := c.c.withConfiguredTimeout(ctx)
	defer cancelFunc()

	r := c.c.NewRequest(http.MethodGet, "/v1/sys/leader")

	resp, err := c.c.rawRequestWithContext(ctx, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result LeaderResponse
	err = resp.DecodeJSON(&result)
	return &result, err
}

type LeaderResponse struct {
	HAEnabled                bool      `json:"ha_enabled"`
	IsSelf                   bool      `json:"is_self"`
	ActiveTime               time.Time `json:"active_time"`
	LeaderAddress            string    `json:"leader_address"`
	LeaderClusterAddress     string    `json:"leader_cluster_address"`
	PerfStandby              bool      `json:"performance_standby"`
	PerfStandbyLastRemoteWAL uint64    `json:"performance_standby_last_remote_wal"`
	LastWAL                  uint64    `json:"last_wal"`
	RaftCommittedIndex       uint64    `json:"raft_committed_index,omitempty"`
	RaftAppliedIndex         uint64    `json:"raft_applied_index,omitempty"`
}
