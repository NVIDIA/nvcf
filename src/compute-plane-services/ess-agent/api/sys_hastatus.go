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

func (c *Sys) HAStatus() (*HAStatusResponse, error) {
	return c.HAStatusWithContext(context.Background())
}

func (c *Sys) HAStatusWithContext(ctx context.Context) (*HAStatusResponse, error) {
	ctx, cancelFunc := c.c.withConfiguredTimeout(ctx)
	defer cancelFunc()

	r := c.c.NewRequest(http.MethodGet, "/v1/sys/ha-status")

	resp, err := c.c.rawRequestWithContext(ctx, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result HAStatusResponse
	err = resp.DecodeJSON(&result)
	return &result, err
}

type HAStatusResponse struct {
	Nodes []HANode
}

type HANode struct {
	Hostname       string     `json:"hostname"`
	APIAddress     string     `json:"api_address"`
	ClusterAddress string     `json:"cluster_address"`
	ActiveNode     bool       `json:"active_node"`
	LastEcho       *time.Time `json:"last_echo"`
	Version        string     `json:"version"`
	UpgradeVersion string     `json:"upgrade_version,omitempty"`
	RedundancyZone string     `json:"redundancy_zone,omitempty"`
}
