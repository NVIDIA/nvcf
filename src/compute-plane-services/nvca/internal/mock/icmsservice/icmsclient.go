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

package mockicmsservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func GetRegisteredNVCACluster(ctx context.Context, addr, clusterID string) (ClusterInfo, error) {
	if !strings.HasPrefix(addr, "http") {
		addr = "http://" + addr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/dump", nil)
	if err != nil {
		return ClusterInfo{}, err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ClusterInfo{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return ClusterInfo{}, errors.New(res.Status)
	}

	state := ServiceState{}
	if err := json.NewDecoder(res.Body).Decode(&state); err != nil {
		return ClusterInfo{}, err
	}

	for _, cluster := range state.BartClusters {
		if cluster.ClusterID == clusterID {
			return cluster, nil
		}
	}

	return ClusterInfo{}, fmt.Errorf("cluster not found: %v", clusterID)
}
