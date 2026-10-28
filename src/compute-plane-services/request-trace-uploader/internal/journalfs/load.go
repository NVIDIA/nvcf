// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

// load reads every segment and batch entry. Temporary files are what a crash
// left before a rename, so they are removed: the previous version is the
// state of record. An entry that cannot be read or has an unknown version is
// fenced, never acted on.
func (s *Store) load() error {
	if err := s.loadSegments(); err != nil {
		return err
	}
	return s.loadBatches()
}

func (s *Store) loadSegments() error {
	entries, err := os.ReadDir(s.layout.segments())
	if err != nil {
		return fmt.Errorf("read journal segments: %w", err)
	}
	for _, entry := range entries {
		file := entry.Name()
		path := filepath.Join(s.layout.segments(), file)
		if strings.HasSuffix(file, tmpExt) {
			if err := removeDurably(path); err != nil {
				return err
			}
			continue
		}
		name, id, ok := parseSegmentFileName(file)
		if !ok || !entry.Type().IsRegular() {
			// The source cannot be identified, so nothing is moved: the
			// spool stops and reports not ready until an operator acts.
			return fmt.Errorf("%w: journal file %q does not name its segment", journal.ErrUnreadableState, file)
		}
		seg, err := readSegment(path, name, id, s.layout.transport())
		if err != nil {
			if err := s.fenceSegment(file, name, id); err != nil {
				return err
			}
			continue
		}
		s.segments[id] = seg
	}
	return nil
}

func readSegment(path, name, id, transportDir string) (*journal.Segment, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var seg journal.Segment
	if err := json.Unmarshal(raw, &seg); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if seg.V != journal.SchemaVersion {
		return nil, fmt.Errorf("unknown schema version %d", seg.V)
	}
	if seg.ID != id || seg.Name != name {
		return nil, fmt.Errorf("content does not match its file name")
	}
	if err := validSegment(&seg, transportDir); err != nil {
		return nil, err
	}
	return &seg, nil
}

func (s *Store) loadBatches() error {
	entries, err := os.ReadDir(s.layout.batches())
	if err != nil {
		return fmt.Errorf("read journal batches: %w", err)
	}
	for _, entry := range entries {
		file := entry.Name()
		path := filepath.Join(s.layout.batches(), file)
		if strings.HasSuffix(file, tmpExt) {
			if err := removeDurably(path); err != nil {
				return err
			}
			continue
		}
		id, ok := parseBatchFileName(file)
		if !ok || !entry.Type().IsRegular() {
			return fmt.Errorf("%w: batch file %q", journal.ErrUnreadableState, file)
		}
		b, err := readBatch(path, id, s.layout.transport())
		if err != nil {
			if err := s.fenceBatch(file); err != nil {
				return err
			}
			continue
		}
		s.batches[id] = b
	}
	return nil
}

func readBatch(path, id, transportDir string) (*journal.Batch, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var b journal.Batch
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if b.V != journal.SchemaVersion {
		return nil, fmt.Errorf("unknown schema version %d", b.V)
	}
	if b.ID != id {
		return nil, fmt.Errorf("content does not match its file name")
	}
	if err := validBatch(&b, transportDir); err != nil {
		return nil, err
	}
	return &b, nil
}
