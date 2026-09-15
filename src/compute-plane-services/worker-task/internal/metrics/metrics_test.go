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

package metrics

import (
	"strings"
	"testing"

	workermetrics "github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue reads the current value out of a counter collector without
// pulling in prometheus/testutil (which adds an out-of-graph dependency).
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("Counter.Write: %v", err)
	}
	if m.Counter == nil || m.Counter.Value == nil {
		t.Fatal("counter metric has no value")
	}
	return *m.Counter.Value
}

// The package-level collectors are registered with the default registry via
// promauto at init time. This smoke test confirms init did not panic and that
// each exported collector is non-nil.
func TestCollectorsNonNil(t *testing.T) {
	if ResultCounter == nil {
		t.Error("ResultCounter is nil")
	}
	if ResultUploadBytesCounter == nil {
		t.Error("ResultUploadBytesCounter is nil")
	}
	if ResultUploadFailureCounter == nil {
		t.Error("ResultUploadFailureCounter is nil")
	}
	if NgcClientResponseCodeCounter == nil {
		t.Error("NgcClientResponseCodeCounter is nil")
	}
}

func TestNamespaceConstants(t *testing.T) {
	want := workermetrics.NvctRootNamespace + "_ngc_client"
	if NgcClientNamespace != want {
		t.Errorf("NgcClientNamespace = %q, want %q", NgcClientNamespace, want)
	}
	if !strings.HasPrefix(NgcClientNamespace, workermetrics.NvctRootNamespace) {
		t.Errorf("NgcClientNamespace %q must be prefixed by root namespace %q",
			NgcClientNamespace, workermetrics.NvctRootNamespace)
	}
}

// Exercise each counter and verify the observed value so the collectors are
// confirmed wired to a working metric, not merely non-nil.
func TestCountersRecordValues(t *testing.T) {
	before := counterValue(t, ResultCounter)
	ResultCounter.Inc()
	if got := counterValue(t, ResultCounter); got != before+1 {
		t.Errorf("ResultCounter = %v, want %v", got, before+1)
	}

	beforeBytes := counterValue(t, ResultUploadBytesCounter)
	ResultUploadBytesCounter.Add(1024)
	if got := counterValue(t, ResultUploadBytesCounter); got != beforeBytes+1024 {
		t.Errorf("ResultUploadBytesCounter = %v, want %v", got, beforeBytes+1024)
	}

	beforeFail := counterValue(t, ResultUploadFailureCounter)
	ResultUploadFailureCounter.Inc()
	if got := counterValue(t, ResultUploadFailureCounter); got != beforeFail+1 {
		t.Errorf("ResultUploadFailureCounter = %v, want %v", got, beforeFail+1)
	}

	c := NgcClientResponseCodeCounter.WithLabelValues("200", "upload")
	c.Add(3)
	if got := counterValue(t, c); got != 3 {
		t.Errorf("NgcClientResponseCodeCounter{200,upload} = %v, want 3", got)
	}
}

// Each collector reports a stable Desc, which confirms it was constructed and
// can be gathered by a registry.
func TestCollectorDescriptors(t *testing.T) {
	check := func(name string, c prometheus.Collector) {
		ch := make(chan *prometheus.Desc, 1)
		c.Describe(ch)
		close(ch)
		if len(ch) == 0 {
			t.Errorf("%s produced no descriptor", name)
			return
		}
		if d := <-ch; d == nil {
			t.Errorf("%s descriptor is nil", name)
		}
	}
	check("ResultCounter", ResultCounter)
	check("ResultUploadBytesCounter", ResultUploadBytesCounter)
	check("ResultUploadFailureCounter", ResultUploadFailureCounter)
	check("NgcClientResponseCodeCounter", NgcClientResponseCodeCounter)
}
