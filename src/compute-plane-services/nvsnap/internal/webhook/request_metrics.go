// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"net/http"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
)

// countRequests counts every admission request next answers, by status, and
// its time (metrics.WebhookRequests): a call the API server made that no
// agent counted never reached one.
func countRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		metrics.ObserveWebhook(sw.code, time.Since(start))
	})
}

// statusWriter keeps the status a handler answered with.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}
