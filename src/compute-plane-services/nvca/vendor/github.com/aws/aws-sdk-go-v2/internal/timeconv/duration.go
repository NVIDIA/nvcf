// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package timeconv

import "time"

// FloatSecondsDur converts a fractional seconds to duration.
func FloatSecondsDur(v float64) time.Duration {
	return time.Duration(v * float64(time.Second))
}

// DurSecondsFloat converts a duration into fractional seconds.
func DurSecondsFloat(d time.Duration) float64 {
	return float64(d) / float64(time.Second)
}
