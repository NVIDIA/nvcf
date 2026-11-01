// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package automemlimit

import (
	"log/slog"

	"github.com/KimMachineGun/automemlimit/memlimit"
)

func init() {
	memlimit.SetGoMemLimitWithOpts(
		memlimit.WithLogger(slog.Default()),
	)
}
