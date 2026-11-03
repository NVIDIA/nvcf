// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
)

// requests is the webhook's count of requests answered with code, and the
// number of timed ones.
func requests(t *testing.T, code string) (counted, timed float64) {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(metrics.WebhookRequests, metrics.WebhookSeconds)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			if mf.GetName() == "nvsnap_webhook_request_seconds" {
				timed = float64(m.GetHistogram().GetSampleCount())
			}
			for _, l := range m.GetLabel() {
				if l.GetName() == "code" && l.GetValue() == code {
					counted = m.GetCounter().GetValue()
				}
			}
		}
	}
	return counted, timed
}

// Every request the webhook answers is counted by its status and timed: a
// handler that writes no status answered 200.
func TestCountRequests(t *testing.T) {
	ok0, timed0 := requests(t, "200")
	bad0, _ := requests(t, "400")
	countRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mutate", http.NoBody))
	rec := httptest.NewRecorder()
	countRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad review", http.StatusBadRequest)
	})).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mutate", http.NoBody))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("the wrapped handler's status was lost: %d", rec.Code)
	}
	ok1, timed1 := requests(t, "200")
	bad1, _ := requests(t, "400")
	if ok1-ok0 != 1 || bad1-bad0 != 1 || timed1-timed0 != 2 {
		t.Fatalf("200 +%v, 400 +%v, timed +%v; want 1, 1, 2", ok1-ok0, bad1-bad0, timed1-timed0)
	}
}

// The server counts the requests to its admission path.
func TestServerCountsAdmissions(t *testing.T) {
	srv := NewServer(ServerConfig{}, &Handler{})
	before, _ := requests(t, "400")
	rec := httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mutate", http.NoBody))
	after, _ := requests(t, "400")
	if rec.Code != http.StatusBadRequest || after-before != 1 {
		t.Fatalf("status %d, counted +%v; want a counted 400 for an empty review", rec.Code, after-before)
	}
}
