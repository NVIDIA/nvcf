// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//nolint:revive
package xtime

import "time"

var clock Clock = &RealClock{}

func SetClock(c Clock) {
	clock = c
}

func Now() time.Time {
	return clock.Now()
}

func Since(t time.Time) time.Duration {
	return clock.Since(t)
}

func Until(t time.Time) time.Duration {
	return clock.Until(t)
}

func Sleep(d time.Duration) {
	clock.Sleep(d)
}

type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	Until(t time.Time) time.Duration
	Sleep(d time.Duration)
}
