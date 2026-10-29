// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package backend defines the export destination contract and the registry
// that maps a configured backend name to a compiled-in implementation.
package backend

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
)

// Client submits one job and reads its status. Submit and Status are separate
// so a slow confirmation cannot block other jobs: an asynchronous backend
// returns from Submit once the job exists, and the caller polls Status from a
// shared loop.
//
// Submit returns the job id together with any error. A non-empty id means the
// job exists even when the error is also set, so the caller must record the
// id before it looks at the error and must poll that job instead of
// submitting the same files again.
//
// Status reads one job. For a job it could read, it returns a non-empty Status
// and a nil error, except that StatusFailure may come with an error that says
// why the job failed. When the read itself fails, it returns an empty Status
// and the error. Classify turns either error into a Fault.
//
// A backend may also implement Classifier to map its own errors.
type Client interface {
	Submit(context.Context, SubmitRequest) (string, error)
	Status(context.Context, string) (Status, error)
	// Capabilities declares what this backend guarantees. Core behavior
	// derives from these properties instead of branching on backend identity.
	Capabilities() Capabilities
}

// Classifier is the optional error classification hook. A backend implements
// it when it knows more about its errors than ClassifyDefault does, such as
// which remote states mean the data may already have landed. It returns the
// zero Fault for an error it does not recognize.
type Classifier interface {
	Classify(error) Fault
}

// Classify maps err to a Fault. A Fault already in err's chain wins, because
// whoever wrapped it knew the most. Otherwise the client's Classifier decides,
// and ClassifyDefault covers what the Classifier does not recognize.
func Classify(client Client, err error) Fault {
	if err == nil {
		return Fault{}
	}
	if fault, ok := asFault(err); ok {
		return fault
	}
	if classifier, ok := client.(Classifier); ok {
		if fault := classifier.Classify(err); fault.Class != "" {
			return fault
		}
	}
	return ClassifyDefault(err)
}

// Capabilities describes one backend's contract with the service that submits
// to it.
type Capabilities struct {
	// ResubmitSafe reports whether Submit can be called again for the same
	// segment after an unconfirmed or failed attempt without side effects
	// beyond the intended upload.
	ResubmitSafe bool
	// TerminalOutcomeSync reports whether Submit does not return until the
	// outcome is durable, so a Status call has nothing left to confirm. An
	// asynchronous job backend reports false.
	TerminalOutcomeSync bool
	// OutOfOrderTolerant reports whether the backend accepts segments in an
	// order other than the one segment.Discover produced them in.
	OutOfOrderTolerant bool
	// AcceptedFormats lists the segment encodings this backend can ingest.
	AcceptedFormats []Format
	// MaxObjectBytes bounds one submitted file. Zero means no bound. A file
	// over it can never be accepted, so Check reports it as a segment fault.
	MaxObjectBytes int64
	// Exports reports whether a successful Submit is a real export whose
	// source segment is safe to delete. A diagnostic backend that reads a
	// segment without sending it anywhere, such as debug, reports false so a
	// successful read never deletes the only copy of real Dynamo output.
	Exports bool
	// MaxBatchFiles bounds how many files one Submit may carry. Zero or one
	// means the backend takes one file per job, so a batcher sends batches of
	// one.
	MaxBatchFiles int
	// MaxBatchBytes bounds the total bytes of the files in one Submit. Zero
	// means no bound beyond MaxObjectBytes per file.
	MaxBatchBytes int64
	// Destinations names every place this backend writes to, such as two
	// tables written side by side. A batcher keeps one lane per destination,
	// and every SubmitRequest names one of them. Empty means the backend has
	// one unnamed destination.
	Destinations []Destination
}

// BatchFiles returns how many files one Submit may carry, at least one.
func (c Capabilities) BatchFiles() int {
	if c.MaxBatchFiles < 1 {
		return 1
	}
	return c.MaxBatchFiles
}

// AllDestinations lists the lanes a batcher keeps for this backend. A backend
// that declares no destinations has a single lane addressed by the empty
// Destination.
func (c Capabilities) AllDestinations() []Destination {
	if len(c.Destinations) == 0 {
		return []Destination{""}
	}
	return slices.Clone(c.Destinations)
}

// Check reports whether request fits these capabilities, so the core can
// enforce them the same way for every backend. A file over MaxObjectBytes is
// a segment-scope terminal Fault, because no retry can make it fit. Every
// other mismatch is a plain error: it means the caller built a request the
// backend never declared it could take.
func (c Capabilities) Check(request SubmitRequest) error {
	files := request.Transports()
	if len(files) == 0 {
		return fmt.Errorf("submit request has no files")
	}
	if request.Path != "" && len(request.Files) > 0 && (len(request.Files) != 1 || request.Files[0].Path != request.Path) {
		return fmt.Errorf("submit request path must name its only file")
	}
	if !slices.Contains(c.AllDestinations(), request.Destination) {
		return fmt.Errorf("submit request destination %q is not declared by the backend", request.Destination)
	}
	if len(files) > c.BatchFiles() {
		return fmt.Errorf("submit request has %d files, over the backend limit of %d", len(files), c.BatchFiles())
	}
	var total int64
	for _, file := range files {
		if file.Bytes < 0 {
			return fmt.Errorf("submit request has a file of negative size %d", file.Bytes)
		}
		if c.MaxObjectBytes > 0 && file.Bytes > c.MaxObjectBytes {
			return &Fault{
				Class:  ClassTerminal,
				Scope:  ScopeSegment,
				Reason: ReasonTooLarge,
				Err:    fmt.Errorf("file is %d bytes, over the backend limit of %d", file.Bytes, c.MaxObjectBytes),
			}
		}
		if total > math.MaxInt64-file.Bytes {
			return fmt.Errorf("submit request size overflows")
		}
		total += file.Bytes
	}
	if c.MaxBatchBytes > 0 && total > c.MaxBatchBytes {
		return fmt.Errorf("submit request is %d bytes, over the backend batch limit of %d", total, c.MaxBatchBytes)
	}
	return nil
}

// Format identifies a segment encoding a backend can ingest.
type Format string

const (
	// FormatGzipJSONL is the format segment.Discover already produces: a
	// gzip-compressed stream of newline-delimited JSON records.
	FormatGzipJSONL Format = "gzip+jsonl"
	// FormatParquet is a Parquet file written by a backend-selected format
	// writer.
	FormatParquet Format = "parquet"
)

// Destination names one place a backend writes to. It comes from a bounded,
// configured set, so it is safe as a log field and a metric attribute.
type Destination string

// SubmitRequest describes one job without exposing its contents.
//
// A job lists its files in Files. Segment and Path are the single-segment
// form that backends taking one raw segment per call read; when both forms
// are set, Path must name the only file. Transports returns the files either
// way.
type SubmitRequest struct {
	Segment segment.Segment
	Path    string
	// Destination is one of Capabilities.AllDestinations.
	Destination Destination
	// Files lists every file of the job, in submit order.
	Files []File
}

// File is one prepared transport file in a job.
type File struct {
	Path  string
	Bytes int64
	// Rows is the record count of the file, or zero when the format does
	// not count rows.
	Rows int64
}

// Transports lists the files of this job: Files when set, otherwise the
// single segment at Path.
func (r SubmitRequest) Transports() []File {
	if len(r.Files) > 0 {
		return r.Files
	}
	if r.Path == "" {
		return nil
	}
	return []File{{Path: r.Path, Bytes: r.Segment.Size}}
}

// Status is an upload operation state.
type Status string

const (
	// StatusPending means the job has not finished. The caller keeps
	// polling.
	StatusPending Status = "pending"
	// StatusSuccess means the job's data has landed.
	StatusSuccess Status = "success"
	// StatusFailure means the job finished without landing its data. The
	// error Status returns with it, if any, says why.
	StatusFailure Status = "failure"
	// StatusAmbiguous means the job failed at a step after which its data
	// may still land, so the backend cannot say whether it did. A job in
	// this state must not be submitted again on the strength of the
	// failure.
	StatusAmbiguous Status = "ambiguous"
	// StatusGone means the backend no longer knows the job, for example
	// because its record expired. The outcome cannot be read any more.
	StatusGone Status = "gone"
)
