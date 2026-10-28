// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, h *Handler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", h.Live)
	mux.HandleFunc("GET /readyz", h.Ready)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func status(t *testing.T, server *httptest.Server, path string) int {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	_ = response.Body.Close()
	return response.StatusCode
}

func TestStartAnswersLiveButNotReadyWhileAStepRuns(t *testing.T) {
	h := New()
	server := serve(t, h)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.Start(context.Background(),
			Step{Name: "load journal", Run: func(context.Context) error { return nil }},
			Step{Name: "first scan", Run: func(context.Context) error {
				close(entered)
				<-release
				return nil
			}},
		)
	}()

	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("Start() returned %v before running the blocking step", err)
	case <-time.After(5 * time.Second):
		t.Fatal("blocking step did not start")
	}
	if got := status(t, server, "/livez"); got != http.StatusOK {
		t.Fatalf("/livez during startup = %d, want 200", got)
	}
	if got := status(t, server, "/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz during startup = %d, want 503", got)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := status(t, server, "/readyz"); got != http.StatusOK {
		t.Fatalf("/readyz after startup = %d, want 200", got)
	}
}

func TestStartRunsStepsInOrder(t *testing.T) {
	var order []string
	step := func(name string) Step {
		return Step{Name: name, Run: func(context.Context) error {
			order = append(order, name)
			return nil
		}}
	}
	if err := New().Start(context.Background(), step("lock"), step("cleanup"), step("journal")); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := strings.Join(order, ","); got != "lock,cleanup,journal" {
		t.Fatalf("order = %s, want lock,cleanup,journal", got)
	}
}

func TestStartStopsAtAFailingStepAndStaysUnready(t *testing.T) {
	h := New()
	server := serve(t, h)
	sentinel := errors.New("disk read failed")
	ranAfter := false

	err := h.Start(context.Background(),
		Step{Name: "load journal", Run: func(context.Context) error { return sentinel }},
		Step{Name: "first scan", Run: func(context.Context) error { ranAfter = true; return nil }},
	)

	if !errors.Is(err, sentinel) {
		t.Fatalf("Start() error = %v, want it to wrap the step error", err)
	}
	if !strings.Contains(err.Error(), "load journal") {
		t.Fatalf("Start() error = %v, want it to name the step", err)
	}
	if ranAfter {
		t.Fatal("a step after the failing one ran")
	}
	if got := status(t, server, "/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz after a failed step = %d, want 503", got)
	}
	if got := status(t, server, "/livez"); got != http.StatusOK {
		t.Fatalf("/livez after a failed step = %d, want 200", got)
	}
}

func TestStartStopsWhenContextEnds(t *testing.T) {
	h := New()
	ctx, cancel := context.WithCancel(context.Background())
	ran := false
	err := h.Start(ctx,
		Step{Name: "lock", Run: func(context.Context) error { cancel(); return nil }},
		Step{Name: "journal", Run: func(context.Context) error { ran = true; return nil }},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}
	if ran {
		t.Fatal("a step ran after the context ended")
	}
}

func TestStartRejectsMalformedSteps(t *testing.T) {
	tests := []struct {
		name string
		step Step
	}{
		{"no name", Step{Run: func(context.Context) error { return nil }}},
		{"no func", Step{Name: "journal"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := New().Start(ctx, tt.step); err == nil {
				t.Fatal("Start() error = nil, want error")
			}
		})
	}
}
