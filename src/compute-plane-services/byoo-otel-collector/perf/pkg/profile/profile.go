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

// Package profile defines the named execution profiles for the BYOO collector
// performance suite. Profiles bundle the run-shape knobs (warmup, measurement
// window, repetitions, and default load rates). All values are overridable via
// CLI flags; the constants here are documented defaults only.
package profile

import (
	"fmt"
	"time"
)

// Profile is a named bundle of run parameters.
type Profile struct {
	// Name is the profile identifier ("dev" or "baseline").
	Name string
	// Warmup is the duration load runs before the measurement window opens.
	Warmup time.Duration
	// MeasurementWindow is the duration over which measurements are recorded.
	MeasurementWindow time.Duration
	// Repetitions is how many times the measurement window is repeated.
	Repetitions int
	// LogRecordsPerSec is the default logs load rate.
	LogRecordsPerSec int
	// MetricDataPointsPerSec is the default metrics load rate.
	MetricDataPointsPerSec int
}

// Dev is a short run intended to validate wiring and support local iteration.
func Dev() Profile {
	return Profile{
		Name:                   "dev",
		Warmup:                 10 * time.Second,
		MeasurementWindow:      30 * time.Second,
		Repetitions:            1,
		LogRecordsPerSec:       1000,
		MetricDataPointsPerSec: 1000,
	}
}

// Baseline is a longer, repeatable run with warmup, a defined measurement
// window, and repeated measurements.
func Baseline() Profile {
	return Profile{
		Name:                   "baseline",
		Warmup:                 60 * time.Second,
		MeasurementWindow:      5 * time.Minute,
		Repetitions:            3,
		LogRecordsPerSec:       10000,
		MetricDataPointsPerSec: 10000,
	}
}

// Lookup returns the named profile, or an error if the name is unknown.
func Lookup(name string) (Profile, error) {
	switch name {
	case "dev":
		return Dev(), nil
	case "baseline":
		return Baseline(), nil
	default:
		return Profile{}, fmt.Errorf("unknown profile %q (want \"dev\" or \"baseline\")", name)
	}
}
