// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
)

func TestSubmitRequestTransports(t *testing.T) {
	tests := []struct {
		name    string
		request SubmitRequest
		want    []File
	}{
		{
			name:    "files listed for a job",
			request: SubmitRequest{Files: []File{{Path: "/t/a", Bytes: 10, Rows: 2}, {Path: "/t/b", Bytes: 20, Rows: 3}}},
			want:    []File{{Path: "/t/a", Bytes: 10, Rows: 2}, {Path: "/t/b", Bytes: 20, Rows: 3}},
		},
		{
			name:    "single segment path becomes one file",
			request: SubmitRequest{Segment: segment.Segment{Path: "/s/seg", Size: 42}, Path: "/s/seg"},
			want:    []File{{Path: "/s/seg", Bytes: 42}},
		},
		{
			name:    "files win over a matching single path",
			request: SubmitRequest{Path: "/t/a", Files: []File{{Path: "/t/a", Bytes: 10}}},
			want:    []File{{Path: "/t/a", Bytes: 10}},
		},
		{
			name:    "nothing to submit",
			request: SubmitRequest{},
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.request.Transports(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Transports() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCapabilitiesCheck(t *testing.T) {
	batching := Capabilities{
		MaxBatchFiles:  3,
		MaxBatchBytes:  100,
		MaxObjectBytes: 60,
		Destinations:   []Destination{"primary", "secondary"},
	}
	single := Capabilities{MaxObjectBytes: 60}
	files := func(sizes ...int64) []File {
		out := make([]File, 0, len(sizes))
		for i, size := range sizes {
			out = append(out, File{Path: string(rune('a'+i)) + ".part", Bytes: size})
		}
		return out
	}

	tests := []struct {
		name         string
		caps         Capabilities
		request      SubmitRequest
		wantErr      bool
		wantSegFault bool
	}{
		{name: "batch within every limit", caps: batching, request: SubmitRequest{Destination: "primary", Files: files(30, 30, 30)}},
		{name: "single path on a non-batching backend", caps: single, request: SubmitRequest{Path: "/s/seg", Segment: segment.Segment{Size: 10}}},
		{name: "no files", caps: batching, request: SubmitRequest{Destination: "primary"}, wantErr: true},
		{name: "more files than the backend batches", caps: batching, request: SubmitRequest{Destination: "primary", Files: files(1, 1, 1, 1)}, wantErr: true},
		{name: "zero batch files means one file per job", caps: single, request: SubmitRequest{Files: files(1, 1)}, wantErr: true},
		{name: "total over the batch byte limit", caps: batching, request: SubmitRequest{Destination: "primary", Files: files(50, 50, 50)}, wantErr: true},
		{name: "one file over the object limit is a segment fault", caps: batching, request: SubmitRequest{Destination: "primary", Files: files(61)}, wantErr: true, wantSegFault: true},
		{name: "undeclared destination", caps: batching, request: SubmitRequest{Destination: "other", Files: files(1)}, wantErr: true},
		{name: "empty destination when several are declared", caps: batching, request: SubmitRequest{Files: files(1)}, wantErr: true},
		{name: "named destination when none are declared", caps: single, request: SubmitRequest{Destination: "primary", Files: files(1)}, wantErr: true},
		{name: "negative file size", caps: batching, request: SubmitRequest{Destination: "primary", Files: files(10, -5)}, wantErr: true},
		{name: "sizes that overflow the total", caps: Capabilities{MaxBatchFiles: 3, MaxBatchBytes: 100}, request: SubmitRequest{Files: files(math.MaxInt64, math.MaxInt64)}, wantErr: true},
		{name: "path disagrees with files", caps: batching, request: SubmitRequest{Destination: "primary", Path: "/elsewhere", Files: files(1)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.caps.Check(tt.request)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Check() error = %v, wantErr %v", err, tt.wantErr)
			}
			var fault *Fault
			isSegFault := errors.As(err, &fault) && fault.Scope == ScopeSegment && fault.Class == ClassTerminal
			if isSegFault != tt.wantSegFault {
				t.Fatalf("Check() error = %v, segment terminal fault = %v, want %v", err, isSegFault, tt.wantSegFault)
			}
		})
	}
}

func TestCapabilitiesDestinations(t *testing.T) {
	if got := (Capabilities{}).AllDestinations(); !reflect.DeepEqual(got, []Destination{""}) {
		t.Fatalf("AllDestinations() with none declared = %v, want one default lane", got)
	}
	declared := []Destination{"primary", "secondary"}
	if got := (Capabilities{Destinations: declared}).AllDestinations(); !reflect.DeepEqual(got, declared) {
		t.Fatalf("AllDestinations() = %v, want %v", got, declared)
	}
}

// plainClient is a backend with no Classifier.
type plainClient struct{}

func (plainClient) Submit(context.Context, SubmitRequest) (string, error) { return "", nil }
func (plainClient) Status(context.Context, string) (Status, error)        { return StatusSuccess, nil }
func (plainClient) Capabilities() Capabilities                            { return Capabilities{} }

// classifyingClient recognizes one error and maps it to a fixed Fault.
type classifyingClient struct {
	plainClient
	known error
	fault Fault
}

func (c classifyingClient) Classify(err error) Fault {
	if errors.Is(err, c.known) {
		return c.fault
	}
	return Fault{}
}

func TestClassifyPrecedence(t *testing.T) {
	known := errors.New("job canceled by the destination")
	own := Fault{Class: ClassTerminal, Scope: ScopeSegment, Reason: "canceled"}
	classifier := classifyingClient{known: known, fault: own}
	wrapped := &Fault{Class: ClassRetryable, Scope: ScopeBackend, Reason: ReasonAuth}

	tests := []struct {
		name   string
		client Client
		err    error
		want   Fault
	}{
		{name: "nil is no fault", client: classifier, err: nil, want: Fault{}},
		{name: "fault in the chain wins over the classifier", client: classifier, err: fmt.Errorf("x: %w", fmt.Errorf("%w: %w", known, wrapped)), want: Fault{Class: ClassRetryable, Scope: ScopeBackend, Reason: ReasonAuth}},
		{name: "classifier maps an error it knows", client: classifier, err: fmt.Errorf("status: %w", known), want: own},
		{name: "classifier falls back to the default", client: classifier, err: context.DeadlineExceeded, want: Fault{Class: ClassAmbiguous, Scope: ScopeBackend, Reason: ReasonTimeout}},
		{name: "backend without a classifier uses the default", client: plainClient{}, err: known, want: Fault{Class: ClassRetryable, Scope: ScopeUnknown, Reason: ReasonUnknown}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.client, tt.err)
			if got.Class != tt.want.Class || got.Scope != tt.want.Scope || got.Reason != tt.want.Reason {
				t.Fatalf("Classify() = %s/%s/%s, want %s/%s/%s", got.Class, got.Scope, got.Reason, tt.want.Class, tt.want.Scope, tt.want.Reason)
			}
		})
	}
}
