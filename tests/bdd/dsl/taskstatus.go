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

package dsl

import (
	"encoding/json"
	"fmt"
)

// TaskStatus returns the raw `.task.status` string from `nvcf-cli task get
// --json` output. It does not interpret the value: NVCT owns status meanings
// and transitions, so callers compare the result to an expected string only.
func TaskStatus(raw string) (string, error) {
	var response struct {
		Task struct {
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return "", fmt.Errorf("parse nvcf-cli task get json: %w", err)
	}
	return response.Task.Status, nil
}
