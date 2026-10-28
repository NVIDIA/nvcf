// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import "testing"

// bucketRange is the span each histogram unit must cover: seconds from
// sub-second calls to day-long confirmations, bytes from KiB to GiB, counts
// from one to the largest batch.
var bucketRange = map[string]struct{ maxFirst, minLast float64 }{
	"s":  {maxFirst: 0.5, minLast: 3600},
	"By": {maxFirst: 1024, minLast: 1 << 30},
	"1":  {maxFirst: 1, minLast: 500},
}

func histograms() []Instrument {
	var out []Instrument
	for _, instrument := range Instruments() {
		if instrument.Kind == Histogram {
			out = append(out, instrument)
		}
	}
	return out
}

func TestEveryHistogramHasBoundariesForItsUnit(t *testing.T) {
	for _, instrument := range histograms() {
		t.Run(instrument.Name, func(t *testing.T) {
			span, known := bucketRange[instrument.Unit]
			if !known {
				t.Fatalf("unit %q has no boundary rule", instrument.Unit)
			}
			bounds := instrument.Buckets
			if len(bounds) < 2 {
				t.Fatalf("boundaries = %v, want an explicit list", bounds)
			}
			for i := 1; i < len(bounds); i++ {
				if bounds[i] <= bounds[i-1] {
					t.Fatalf("boundaries = %v, want them strictly increasing", bounds)
				}
			}
			if bounds[0] <= 0 || bounds[0] > span.maxFirst || bounds[len(bounds)-1] < span.minLast {
				t.Fatalf("boundaries = %v, want the first in (0, %v] and the last at least %v", bounds, span.maxFirst, span.minLast)
			}
		})
	}
}

func TestOnlyHistogramsHaveBoundaries(t *testing.T) {
	for _, instrument := range Instruments() {
		if instrument.Kind != Histogram && instrument.Buckets != nil {
			t.Errorf("%s is a %v with boundaries", instrument.Name, instrument.Kind)
		}
	}
}
