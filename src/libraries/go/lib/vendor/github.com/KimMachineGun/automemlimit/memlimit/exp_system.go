// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package memlimit

import (
	"github.com/pbnjay/memory"
)

// FromSystem returns the total memory of the system.
func FromSystem() (uint64, error) {
	limit := memory.TotalMemory()
	if limit == 0 {
		return 0, ErrNoLimit
	}
	return limit, nil
}
