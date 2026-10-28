// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package format is the seam between prepare and the backend: the backend
// chooses the format writer that turns prepared records into the files it
// uploads.
//
// Prepare hands every record, after suppression and redaction, to a
// transform.Writer. A Writer here is a transform.Writer that also reports the
// files it published, so the journal and the batcher can record them. A
// backend that needs its own layout, such as table rows in Parquet,
// implements Factory on its client. A backend that stores records as they
// are declares backend.FormatGzipJSONL and gets the gzip JSON-lines writer.
// The core picks the factory with For, from capabilities only.
package format

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/journalfs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform"
)

var segmentIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Target says where one segment's transports go.
type Target struct {
	// Segment is the source segment being prepared.
	Segment segment.Segment
	// SegmentID is the segment's journal key, from journal.SegmentID.
	SegmentID string
	// Dir is the journal's transport directory. Every file a writer
	// creates, finished or not, must be named by Path so recovery can find
	// and remove the files of an interrupted prepare.
	Dir string
	// Destinations lists the lanes to write, from
	// backend.Capabilities.AllDestinations.
	Destinations []backend.Destination
}

// Path returns where a writer puts the transport for one destination. ext
// includes its dot, such as ".jsonl.gz".
func (t Target) Path(destination backend.Destination, ext string) string {
	return journalfs.TransportPath(t.Dir, t.SegmentID, string(destination), ext)
}

// Validate rejects a target no writer can name its files from.
func (t Target) Validate() error {
	var errs []error
	if !segmentIDPattern.MatchString(t.SegmentID) {
		errs = append(errs, errors.New("format target segment id must be a journal key"))
	}
	if !filepath.IsAbs(t.Dir) {
		errs = append(errs, errors.New("format target directory must be an absolute path"))
	}
	if len(t.Destinations) == 0 {
		errs = append(errs, errors.New("format target needs at least one destination"))
	}
	for i, destination := range t.Destinations {
		if slices.Contains(t.Destinations[:i], destination) {
			errs = append(errs, fmt.Errorf("format target destination %q is listed twice", destination))
		}
	}
	return errors.Join(errs...)
}

// Output describes one published transport file.
type Output struct {
	Destination backend.Destination
	Path        string
	// SHA256 is the hex sha256 of the file as stored.
	SHA256 string
	Bytes  int64
	// Rows counts the records in the file.
	Rows int64
}

// Writer is a transform.Writer that reports what it published.
type Writer interface {
	transform.Writer
	// Outputs lists the published files, one per destination, once Close
	// returned nil and wrote at least one record. It is empty otherwise.
	Outputs() []Output
}

// Factory opens format writers. A backend whose destinations need their own
// layout implements it on its backend.Client.
type Factory interface {
	// Format names what the writers produce. It must be one of the
	// backend's AcceptedFormats.
	Format() backend.Format
	// NewWriter starts a writer for one segment.
	NewWriter(Target) (Writer, error)
}

// For returns the factory for client. A client that implements Factory
// chooses its own writer. Otherwise a client that accepts gzip JSON lines on
// a single lane gets GzipJSONL. Anything else is an error, so a wiring
// mistake fails at startup and not on the first segment.
func For(client backend.Client) (Factory, error) {
	if client == nil {
		return nil, errors.New("format: no backend client")
	}
	caps := client.Capabilities()
	if own, ok := client.(Factory); ok {
		if !slices.Contains(caps.AcceptedFormats, own.Format()) {
			return nil, fmt.Errorf("format: backend writes %q but accepts only %v", own.Format(), caps.AcceptedFormats)
		}
		return own, nil
	}
	if !slices.Contains(caps.AcceptedFormats, backend.FormatGzipJSONL) {
		return nil, fmt.Errorf("format: backend accepts %v and has no format writer of its own", caps.AcceptedFormats)
	}
	if lanes := len(caps.AllDestinations()); lanes != 1 {
		return nil, fmt.Errorf("format: backend declares %d destinations and has no format writer of its own", lanes)
	}
	return GzipJSONL{}, nil
}

// Describe sizes and hashes a published file, for a writer that cannot
// count while it writes, such as one that hands its file to a library.
func Describe(destination backend.Destination, path string, rows int64) (Output, error) {
	file, err := os.Open(path)
	if err != nil {
		return Output{}, fmt.Errorf("describe transport: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return Output{}, fmt.Errorf("describe transport: %w", err)
	}
	return Output{
		Destination: destination,
		Path:        path,
		SHA256:      hex.EncodeToString(hash.Sum(nil)),
		Bytes:       size,
		Rows:        rows,
	}, nil
}
