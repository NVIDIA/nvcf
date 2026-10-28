// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package segment discovers closed Dynamo request-trace segments and hands
// them to the next stage.
//
// Dynamo names a segment <prefix>.<index>.jsonl.gz with an index of at least
// six digits that grows past six, and rolls to the next index when a segment
// is full. A segment is closed only once a higher index exists for the same
// prefix, so the highest index of each prefix is always left alone.
package segment

import "time"

// Segment is one compressed Dynamo request-trace segment.
//
// Dynamo v1.4.0 writes every record type to one segment family, so a segment
// carries no capture type. Records are classified by their event_type field,
// which the parsing increment reads.
type Segment struct {
	Path string
	// Prefix is the segment family the name matched.
	Prefix  string
	Index   int
	Size    int64
	ModTime time.Time
}

// Discover returns the closed segments of one prefix in numeric index order.
// Dynamo appends gzip members to the highest indexed segment, so that segment
// is never returned.
func Discover(directory, prefix string) ([]Segment, error) {
	scan, err := ScanDir(directory, []string{prefix})
	if err != nil {
		return nil, err
	}
	return scan.Closed, nil
}
