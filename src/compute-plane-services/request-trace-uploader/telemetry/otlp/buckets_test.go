// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"slices"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

func TestOTLPExportsEachHistogramWithItsBoundaries(t *testing.T) {
	var histograms []telemetry.Instrument
	for _, instrument := range telemetry.Instruments() {
		if instrument.Kind == telemetry.Histogram {
			histograms = append(histograms, instrument)
		}
	}
	r, endpoint := httpReceiver(t)
	exporter := newOTLP(t, endpoint, ProtocolHTTP)
	for _, instrument := range histograms {
		exporter.Observe(instrument.Name, 1, nil)
	}
	flush(t, exporter)

	for _, instrument := range histograms {
		t.Run(instrument.Name, func(t *testing.T) {
			points := r.metric(MetricPrefix + instrument.Name).GetHistogram().GetDataPoints()
			if len(points) != 1 {
				t.Fatalf("points = %v, want one", points)
			}
			if got := points[0].GetExplicitBounds(); !slices.Equal(got, instrument.Buckets) {
				t.Fatalf("exported boundaries = %v, want %v", got, instrument.Buckets)
			}
		})
	}
}
