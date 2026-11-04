/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package nvsnap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateCheckpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/checkpoints" {
			t.Errorf("path = %q, want /api/v1/checkpoints", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		var req CheckpointRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.Namespace != "nvcf-backend" || req.PodName != "0-sr-abc" || req.ContainerName != "inference" {
			t.Errorf("body = %+v, want {nvcf-backend, 0-sr-abc, inference}", req)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(Checkpoint{
			ID:        "0-sr-abc-1234567890",
			Namespace: "nvcf-backend",
			Phase:     PhaseInProgress,
			Message:   "Checkpoint started",
		})
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	got, err := c.CreateCheckpoint(context.Background(), CheckpointRequest{
		Namespace:     "nvcf-backend",
		PodName:       "0-sr-abc",
		ContainerName: "inference",
		LeaveRunning:  true,
	})
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if got.ID != "0-sr-abc-1234567890" {
		t.Errorf("ID = %q, want 0-sr-abc-1234567890", got.ID)
	}
	if got.Phase != PhaseInProgress {
		t.Errorf("Phase = %q, want %q", got.Phase, PhaseInProgress)
	}
	if got.IsTerminal() {
		t.Errorf("IsTerminal() = true for InProgress, want false")
	}
}

func TestCreateCheckpointRequiresFields(t *testing.T) {
	c := NewClient()
	for _, tc := range []struct {
		name string
		req  CheckpointRequest
	}{
		{"missing namespace", CheckpointRequest{PodName: "p"}},
		{"missing pod", CheckpointRequest{Namespace: "ns"}},
		{"empty", CheckpointRequest{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.CreateCheckpoint(context.Background(), tc.req)
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestGetCheckpointTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/checkpoints/0-sr-abc-1234567890" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Checkpoint{
			ID:       "0-sr-abc-1234567890",
			Phase:    PhaseCompleted,
			Hash:     "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			Size:     81604378624,
			Duration: 81.2,
			Message:  "Completed",
		})
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	got, err := c.GetCheckpoint(context.Background(), "0-sr-abc-1234567890")
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if !got.IsTerminal() {
		t.Errorf("IsTerminal() = false for Completed, want true")
	}
	if got.Hash == "" || got.Size == 0 {
		t.Errorf("expected Hash + Size populated on terminal Completed, got %+v", got)
	}
}

func TestGetCheckpointNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "checkpoint not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	_, err := c.GetCheckpoint(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", apiErr.StatusCode)
	}
}

func TestDeleteCheckpointIdempotent(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"204 No Content", http.StatusNoContent, false},
		{"200 OK", http.StatusOK, false},
		{"404 treated as success (idempotent)", http.StatusNotFound, false},
		{"500 propagates", http.StatusInternalServerError, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("method = %q, want DELETE", r.Method)
				}
				w.WriteHeader(tc.statusCode)
			}))
			defer srv.Close()

			c := NewClient(WithBaseURL(srv.URL))
			err := c.DeleteCheckpoint(context.Background(), "any-id")
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRequestContextCancellation(t *testing.T) {
	// Server hangs forever.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.GetCheckpoint(ctx, "x")
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") &&
		!errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context deadline exceeded", err)
	}
}

func TestUserAgentSent(t *testing.T) {
	gotUA := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"x","phase":"InProgress"}`)
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL), WithUserAgent("nvca-test/9"))
	_, _ = c.GetCheckpoint(context.Background(), "x")
	if gotUA != "nvca-test/9" {
		t.Errorf("User-Agent = %q, want nvca-test/9", gotUA)
	}
}

func TestBaseURLTrailingSlashStripped(t *testing.T) {
	c := NewClient(WithBaseURL("http://example.com/"))
	if c.baseURL != "http://example.com" {
		t.Errorf("baseURL = %q, want stripped trailing slash", c.baseURL)
	}
}

func TestAPIErrorContainsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "namespace required", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	_, err := c.CreateCheckpoint(context.Background(), CheckpointRequest{Namespace: "ns", PodName: "p"})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "namespace required") {
		t.Errorf("Body = %q, want it to contain server message", apiErr.Body)
	}
}
