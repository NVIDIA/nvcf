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

package agent

import (
	"os"
	"strconv"
)

// cudaParallelism returns the cap on concurrent cuda-checkpoint subprocesses
// the agent will run during a single Checkpoint/Restore call. The default of
// 8 matches typical multi-GPU NIM scale and stays well under the per-PID
// driver locking we observed on 38-PID workloads (memory entry #176).
//
// Override with NVSNAP_CUDA_PARALLELISM. A value of 1 falls back to fully
// serial behavior — useful if NVIDIA's tool turns out to serialize at the
// driver level on a given hardware/driver combo.
func cudaParallelism() int {
	const def = 8
	if v := os.Getenv("NVSNAP_CUDA_PARALLELISM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
