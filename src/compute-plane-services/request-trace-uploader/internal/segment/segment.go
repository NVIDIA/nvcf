// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package segment discovers closed Dynamo request-trace segments.
package segment

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// CaptureType identifies the current Dynamo deployment's two trace streams.
type CaptureType string

const (
	CaptureTypeTrace CaptureType = "trace"
	CaptureTypeAudit CaptureType = "audit"
)

// Segment is one closed, compressed Dynamo request-trace segment.
type Segment struct {
	CaptureType CaptureType
	Path        string
	Index       int
	Size        int64
	ModTime     time.Time
}

// Discover returns segments that are safe for a future uploader to process.
// Dynamo appends gzip members to the highest indexed segment for each prefix,
// so the scanner always leaves that segment untouched.
func Discover(directory, tracePrefix, auditPrefix string) ([]Segment, error) {
	trace, err := discoverPrefix(directory, tracePrefix, CaptureTypeTrace)
	if err != nil {
		return nil, err
	}
	audit, err := discoverPrefix(directory, auditPrefix, CaptureTypeAudit)
	if err != nil {
		return nil, err
	}
	segments := append(trace, audit...)
	sort.Slice(segments, func(i, j int) bool {
		if segments[i].ModTime.Equal(segments[j].ModTime) {
			return segments[i].Path < segments[j].Path
		}
		return segments[i].ModTime.Before(segments[j].ModTime)
	})
	return segments, nil
}

func discoverPrefix(directory, prefix string, captureType CaptureType) ([]Segment, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read request trace directory: %w", err)
	}
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + `\.(\d{6})\.jsonl\.gz$`)
	segments := make([]Segment, 0)
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		matches := pattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		index, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, fmt.Errorf("parse request trace segment index %q: %w", matches[1], err)
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat request trace segment %q: %w", entry.Name(), err)
		}
		segments = append(segments, Segment{
			CaptureType: captureType,
			Path:        filepath.Join(directory, entry.Name()),
			Index:       index,
			Size:        info.Size(),
			ModTime:     info.ModTime(),
		})
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].Index < segments[j].Index })
	if len(segments) < 2 {
		return nil, nil
	}
	return segments[:len(segments)-1], nil
}
