// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package format_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/format"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
)

var testSegmentID = journal.SegmentID("request-trace", 1, 100, "f00d")

func newTarget(t *testing.T, destinations ...backend.Destination) format.Target {
	t.Helper()
	if destinations == nil {
		destinations = []backend.Destination{""}
	}
	return format.Target{
		Segment:      segment.Segment{Path: "/spool/request-trace.000001.jsonl.gz", Prefix: "request-trace", Index: 1, Size: 100},
		SegmentID:    testSegmentID,
		Dir:          t.TempDir(),
		Destinations: destinations,
	}
}

// The names are pinned: a journal written by an earlier release records these
// paths, and recovery finds unfinished files by the segment id prefix.
func TestTargetPathKeepsTransportNames(t *testing.T) {
	target := newTarget(t, "primary", "legacy")
	tests := []struct {
		destination backend.Destination
		ext         string
		want        string
	}{
		{"primary", ".parquet", testSegmentID + ".986a1b7135f49861.parquet"},
		{"legacy", ".parquet", testSegmentID + ".c49fea7425fa7f86.parquet"},
		{"", ".jsonl.gz", testSegmentID + ".e3b0c44298fc1c14.jsonl.gz"},
	}
	for _, tt := range tests {
		if got, want := target.Path(tt.destination, tt.ext), filepath.Join(target.Dir, tt.want); got != want {
			t.Errorf("Path(%q, %q) = %q, want %q", tt.destination, tt.ext, got, want)
		}
	}
}

// ownFormat is a backend that writes its own format.
type ownFormat struct {
	*backendtest.Fake
	format backend.Format
}

func (o ownFormat) Format() backend.Format                         { return o.format }
func (o ownFormat) NewWriter(format.Target) (format.Writer, error) { return nil, nil }

func fakeWith(caps backend.Capabilities) *backendtest.Fake {
	fake := backendtest.NewFake()
	fake.SetCapabilities(caps)
	return fake
}

func TestForSelectsWriterByCapability(t *testing.T) {
	gzipOnly := backend.Capabilities{AcceptedFormats: []backend.Format{backend.FormatGzipJSONL}}
	parquetOnly := backend.Capabilities{AcceptedFormats: []backend.Format{backend.FormatParquet}}
	both := backend.Capabilities{AcceptedFormats: []backend.Format{backend.FormatGzipJSONL, backend.FormatParquet}}
	namedLane := gzipOnly
	namedLane.Destinations = []backend.Destination{"bucket"}
	twoLanes := gzipOnly
	twoLanes.Destinations = []backend.Destination{"primary", "legacy"}

	tests := []struct {
		name    string
		client  backend.Client
		want    backend.Format
		wantErr bool
	}{
		{name: "gzip JSON lines on the unnamed lane", client: fakeWith(gzipOnly), want: backend.FormatGzipJSONL},
		{name: "gzip JSON lines on one named lane", client: fakeWith(namedLane), want: backend.FormatGzipJSONL},
		{name: "parquet with no writer of its own", client: fakeWith(parquetOnly), wantErr: true},
		{name: "two lanes with no writer of its own", client: fakeWith(twoLanes), wantErr: true},
		{name: "no accepted format", client: fakeWith(backend.Capabilities{}), wantErr: true},
		{name: "own writer for an accepted format", client: ownFormat{fakeWith(parquetOnly), backend.FormatParquet}, want: backend.FormatParquet},
		{name: "own writer wins over gzip JSON lines", client: ownFormat{fakeWith(both), backend.FormatParquet}, want: backend.FormatParquet},
		{name: "own writer for a format the backend does not accept", client: ownFormat{fakeWith(gzipOnly), backend.FormatParquet}, wantErr: true},
		{name: "no client", client: nil, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory, err := format.For(tt.client)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("For() = %v, want an error", factory)
				}
				return
			}
			if err != nil {
				t.Fatalf("For() error = %v", err)
			}
			if got := factory.Format(); got != tt.want {
				t.Fatalf("For().Format() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestForReturnsTheBackendsOwnFactory(t *testing.T) {
	client := ownFormat{fakeWith(backend.Capabilities{AcceptedFormats: []backend.Format{backend.FormatParquet}}), backend.FormatParquet}
	factory, err := format.For(client)
	if err != nil {
		t.Fatalf("For() error = %v", err)
	}
	if _, ok := factory.(ownFormat); !ok {
		t.Fatalf("For() = %T, want the backend's own factory", factory)
	}
}

func TestDescribeMatchesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.parquet")
	data := []byte("PAR1 not really")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)

	got, err := format.Describe("primary", path, 7)
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}
	want := format.Output{Destination: "primary", Path: path, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), Rows: 7}
	if got != want {
		t.Fatalf("Describe() = %+v, want %+v", got, want)
	}
}

func TestDescribeMissingFileFails(t *testing.T) {
	if _, err := format.Describe("", filepath.Join(t.TempDir(), "gone"), 1); err == nil {
		t.Fatal("Describe() error = nil for a missing file")
	}
}
