// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package record

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrLegacyAuditRecord reports a Dynamo v1.3.x AuditRecord. That type was
// removed in v1.4.0, and its shape differs enough that parsing it as a request
// trace record would silently produce empty fields rather than fail.
var ErrLegacyAuditRecord = errors.New("record: Dynamo v1.3.x AuditRecord found, minimum supported version is v1.4.0")

// Stats summarizes one pass over a segment.
type Stats struct {
	Records     int
	Unparseable int
	Oversized   int
	ByEventType map[EventType]int
	Unknown     int
	Bytes       int64
}

// Reader streams records out of a gzipped JSON-lines segment.
//
// It never holds more than one record in memory, so segment size does not
// bound memory. A record that fails to parse, including one that exceeds the
// size bound, is counted and skipped; it does not end the scan, because one
// bad line must not discard the other records in a segment.
type Reader struct {
	file    *os.File
	gz      *gzip.Reader
	buf     *bufio.Reader
	current *Record
	err     error
	line    int
	stats   Stats
}

// maxLineBytes bounds a single record. Payload records carry entire request
// and response bodies, so the default scanner limit is far too small.
const maxLineBytes = 64 << 20

// Open starts reading the segment at path.
func Open(path string) (*Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open segment: %w", err)
	}
	gz, err := gzip.NewReader(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("read segment gzip header: %w", err)
	}
	// Dynamo appends gzip members as it rolls, so a segment is a concatenated
	// stream rather than a single member.
	gz.Multistream(true)

	return &Reader{
		file:  file,
		gz:    gz,
		buf:   bufio.NewReaderSize(gz, 64<<10),
		stats: Stats{ByEventType: map[EventType]int{}},
	}, nil
}

// readLine returns the next line with its terminator stripped.
//
// A line longer than maxLineBytes is drained to its newline and reported as
// oversized, so the reader resumes at the following record instead of stopping
// at the first one it cannot hold. bufio.Scanner cannot do this: it fails
// permanently once a token exceeds its buffer.
func (r *Reader) readLine() (line []byte, oversized bool, err error) {
	var acc []byte
	for {
		chunk, chunkErr := r.buf.ReadSlice('\n')
		if errors.Is(chunkErr, bufio.ErrBufferFull) {
			if len(acc)+len(chunk) > maxLineBytes {
				oversized = true
				acc = nil
				// Keep draining until the newline so the next read starts on a
				// record boundary.
				continue
			}
			acc = append(acc, chunk...)
			continue
		}
		if chunkErr != nil {
			if len(acc) == 0 && len(chunk) == 0 {
				return nil, oversized, chunkErr
			}
			if !oversized {
				acc = append(acc, chunk...)
			}
			return trimEOL(acc), oversized, nil
		}
		if oversized {
			return nil, true, nil
		}
		if len(acc)+len(chunk) > maxLineBytes {
			return nil, true, nil
		}
		acc = append(acc, chunk...)
		return trimEOL(acc), false, nil
	}
}

func trimEOL(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r"))
}

// Next advances to the next record, reporting whether one was read.
func (r *Reader) Next() bool {
	for {
		line, oversized, err := r.readLine()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				r.err = fmt.Errorf("read segment: %w", err)
			}
			return false
		}
		r.line++
		if oversized {
			r.stats.Unparseable++
			r.stats.Oversized++
			continue
		}
		if len(line) == 0 {
			continue
		}

		rec, err := parseLine(line)
		if err != nil {
			if errors.Is(err, ErrLegacyAuditRecord) {
				r.err = fmt.Errorf("line %d: %w", r.line, err)
				return false
			}
			r.stats.Unparseable++
			continue
		}

		r.stats.Records++
		r.stats.Bytes += int64(len(line))
		r.stats.ByEventType[rec.EventType]++
		if !rec.EventType.Known() {
			r.stats.Unknown++
		}
		r.current = rec
		return true
	}
}

// Record returns the record read by the last successful Next.
func (r *Reader) Record() *Record { return r.current }

// Stats returns the running totals for this pass.
func (r *Reader) Stats() Stats { return r.stats }

// Err returns the error that ended the scan, if any. A scan that ended at the
// end of the segment returns nil. Unparseable records are counted in Stats
// rather than reported here.
func (r *Reader) Err() error { return r.err }

// Close releases the segment.
func (r *Reader) Close() error {
	gzErr := r.gz.Close()
	fileErr := r.file.Close()
	if gzErr != nil {
		return fmt.Errorf("close segment gzip: %w", gzErr)
	}
	if fileErr != nil {
		return fmt.Errorf("close segment: %w", fileErr)
	}
	return nil
}

// envelope is the timestamp-wrapped form some Dynamo sinks emit. The file sink
// writes bare records, so both shapes have to parse.
type envelope struct {
	Timestamp *uint64         `json:"timestamp"`
	Event     json.RawMessage `json:"event"`
}

// legacyAudit is the Dynamo v1.3.x AuditRecord. It is detected, never parsed.
// It has schema_version rather than schema, and no event_type at all.
type legacyAudit struct {
	SchemaVersion *uint32 `json:"schema_version"`
	EventType     *string `json:"event_type"`
}

func parseLine(line []byte) (*Record, error) {
	body := line

	var env envelope
	if err := json.Unmarshal(line, &env); err == nil && len(env.Event) > 0 {
		body = env.Event
	}

	var legacy legacyAudit
	if err := json.Unmarshal(body, &legacy); err == nil {
		if legacy.SchemaVersion != nil && legacy.EventType == nil {
			return nil, ErrLegacyAuditRecord
		}
	}

	var rec Record
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, fmt.Errorf("parse record: %w", err)
	}
	if rec.EventType == "" {
		return nil, errors.New("parse record: no event_type")
	}

	rec.Raw = append([]byte(nil), body...)
	return &rec, nil
}
