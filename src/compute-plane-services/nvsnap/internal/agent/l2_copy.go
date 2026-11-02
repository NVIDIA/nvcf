// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"strconv"
)

const (
	l2CopyDefaultWorkers = 32
	l2CopyWorkersEnv     = "NVSNAP_L2_FETCH_WORKERS"
)

// l2CopyWorkers is how many files the L2 fetch reads at once: direct reads
// of a network volume need many in flight to reach its throughput.
func l2CopyWorkers() int {
	if n, err := strconv.Atoi(os.Getenv(l2CopyWorkersEnv)); err == nil && n > 0 {
		return n
	}
	return l2CopyDefaultWorkers
}
