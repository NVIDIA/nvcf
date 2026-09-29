// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A transient 5xx from nvsnap-server must not fail the promote: the write
// is retried and succeeds once the server answers. A 4xx is an answer and
// is returned at once.
func TestPVCStateHTTPWriter_RetriesTransientServerErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(w, "database is locked (5) (SQLITE_BUSY)", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	w := &pvcStateHTTPWriter{catalogURL: srv.URL, client: srv.Client()}
	if err := w.UpdatePVCPromoteState("abc", "pending", ""); err != nil {
		t.Fatalf("two 500s then 204 must succeed, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}

	var bad atomic.Int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bad.Add(1)
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv2.Close()
	w2 := &pvcStateHTTPWriter{catalogURL: srv2.URL, client: srv2.Client()}
	if err := w2.UpdatePVCPromoteState("abc", "pending", ""); err == nil {
		t.Fatal("400 must be returned as an error")
	}
	if bad.Load() != 1 {
		t.Errorf("4xx must not be retried, calls=%d", bad.Load())
	}
}
