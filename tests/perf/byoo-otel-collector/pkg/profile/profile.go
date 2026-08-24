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
	// Workers is the telemetrygen concurrency per generator Job. Zero uses
	// telemetrygen's default.
	Workers int
	// LogBodyBytes pads each synthetic log body to approximately this size.
	LogBodyBytes int
	// LogPayloadBytes is the size of the oversized string attribute attached to
	// large log records. Zero adds none.
	LogPayloadBytes int
	// LargeRecordFraction is the fraction of the logs rate emitted as large
	// (payload-bearing) records; the rest are small body-only records.
	LargeRecordFraction float64
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

// Nemotron models a large-record log workload: a bimodal record-size mix
// emitted at a modest rate. Small records carry an ~8 KB body (p50 record
// ~8.8 KB); ~45% of records additionally carry a ~320 KB string attribute
// (p95 record ~327 KB). The bytes live in the attribute rather than the body,
// so log chunking must size on the full record; chunking splits at the 256 KB
// threshold.
//
// The 40 rec/s rate is low but the large-record fraction makes each record
// heavy, so throughput is dominated by payload bytes. Sweep the collector's
// memory limit around this profile (optionally with --sink-cpu-limit to model
// a slow backend) to find where it exhausts memory versus holds.
func Nemotron() Profile {
	return Profile{
		Name:                   "nemotron",
		Warmup:                 30 * time.Second,
		MeasurementWindow:      3 * time.Minute,
		Repetitions:            1,
		LogRecordsPerSec:       40,
		MetricDataPointsPerSec: 0,
		Workers:                1,
		LogBodyBytes:           8 * 1024,
		LogPayloadBytes:        320 * 1024,
		LargeRecordFraction:    0.45,
	}
}

// Lookup returns the named profile, or an error if the name is unknown.
func Lookup(name string) (Profile, error) {
	switch name {
	case "dev":
		return Dev(), nil
	case "baseline":
		return Baseline(), nil
	case "nemotron":
		return Nemotron(), nil
	default:
		return Profile{}, fmt.Errorf("unknown profile %q (want \"dev\", \"baseline\", or \"nemotron\")", name)
	}
}
