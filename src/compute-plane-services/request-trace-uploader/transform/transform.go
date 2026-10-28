// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package transform prepares one closed segment for export: it reads every
// record, computes the segment fingerprint in the same pass, applies
// suppression and then redaction, and hands each record to a format writer
// that the backend chooses.
//
// The record handling here is backend-neutral. A backend that needs rows in
// a table format implements Writer; one that stores records as they are can
// use JSONLFile.
package transform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/record"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform/compat"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform/redact"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform/suppress"
)

const (
	// DefaultRawLineMax bounds one source line held in memory.
	DefaultRawLineMax = record.DefaultMaxLineBytes
	// DefaultRowMax bounds one exported record after suppression and
	// redaction. Redaction can shrink a huge media line far below it.
	DefaultRowMax = 60 << 20
	// DefaultMaxBadRecordFraction quarantines a segment only when every
	// line in it is bad.
	DefaultMaxBadRecordFraction = 1.0
)

// Quarantine reasons.
const (
	// ReasonUnsupportedVersion marks a segment written by a Dynamo release
	// older than v1.4.0.
	ReasonUnsupportedVersion = "unsupported_dynamo_version"
	// ReasonBadRecords marks a segment whose share of bad lines reached
	// MaxBadRecordFraction.
	ReasonBadRecords = "bad_records"
	// ReasonUnreadable marks a segment with bytes but no readable line.
	ReasonUnreadable = "unreadable"
)

// Config bounds preparing one segment.
type Config struct {
	// RawLineMax bounds one source line in bytes. A longer line is
	// rejected without being held in memory.
	RawLineMax int64
	// RowMax bounds one exported record in bytes, checked after
	// suppression and redaction.
	RowMax int64
	// MaxBadRecordFraction quarantines a segment when bad lines make up at
	// least this share of its lines. It must be in (0, 1].
	MaxBadRecordFraction float64
	// Redact turns multimodal redaction on.
	Redact bool
	// Suppression lists the suppression rules. No rules turns it off.
	Suppression suppress.Config
}

// DefaultConfig returns the defaults: redaction on, no suppression rules.
func DefaultConfig() Config {
	return Config{
		RawLineMax:           DefaultRawLineMax,
		RowMax:               DefaultRowMax,
		MaxBadRecordFraction: DefaultMaxBadRecordFraction,
		Redact:               true,
	}
}

// Validate rejects any setting prepare cannot run with.
func (c Config) Validate() error {
	if c.RawLineMax <= 0 {
		return fmt.Errorf("raw line max must be positive, got %d", c.RawLineMax)
	}
	if c.RowMax <= 0 {
		return fmt.Errorf("row max must be positive, got %d", c.RowMax)
	}
	if math.IsNaN(c.MaxBadRecordFraction) || c.MaxBadRecordFraction <= 0 || c.MaxBadRecordFraction > 1 {
		return fmt.Errorf("max bad record fraction must be in (0, 1], got %v", c.MaxBadRecordFraction)
	}
	if err := c.Suppression.Validate(); err != nil {
		return fmt.Errorf("suppression: %w", err)
	}
	return nil
}

// Record is one record ready for a format writer. Payload and Value are only
// valid during the Write call that receives them.
type Record struct {
	// Line is the 1-based line number in the segment.
	Line int
	// Value is the record after suppression and redaction.
	Value *compat.Value
	// Payload is the source line unchanged, or Value re-serialized the way
	// the earlier Python uploader wrote it when suppression or redaction
	// changed the record.
	Payload []byte
	// Headers holds copies of the record's header objects as they were read,
	// before any change, in lookup order. See suppress.ResolveHeader.
	Headers []*compat.Value
	// CarriesPayload reports a request_payload record.
	CarriesPayload bool
	// Suppression reports what the suppression rules did.
	Suppression suppress.Outcome
	// RedactedParts counts media parts replaced in this record.
	RedactedParts int
}

// Writer turns records into a backend's transport format.
type Writer interface {
	// Write adds one record. It must not keep Record.Payload or
	// Record.Value after it returns.
	Write(Record) error
	// Close finishes the transport after the last record.
	Close() error
	// Abort discards whatever was written. It is called instead of Close
	// when the segment is quarantined, has nothing to export, or fails, and
	// after a Close that failed.
	Abort() error
}

// Result summarizes one prepared segment.
type Result struct {
	// Fingerprint is the SHA-256 of the whole source file.
	Fingerprint []byte
	// Received counts non-empty source lines.
	Received int
	// Written counts records handed to the writer.
	Written int
	// Dropped counts records removed by a dropRecord rule.
	Dropped int
	// Suppressed counts records replaced by the suppression stub.
	Suppressed int
	// Evaluated counts records checked against suppression rules.
	Evaluated int
	// HeaderPresent counts evaluated records that carried a rule's header.
	HeaderPresent int
	// RedactedParts counts media parts replaced.
	RedactedParts int
	// Unparseable counts lines that are not valid JSON in UTF-8.
	Unparseable int
	// LineTooLong counts lines over RawLineMax.
	LineTooLong int
	// RowTooLarge counts records over RowMax after redaction.
	RowTooLarge int
	// Cut counts partial lines lost to a damaged gzip member.
	Cut int
	// LostBytes counts compressed bytes that could not be read.
	LostBytes int64
	// Resyncs counts restarts after a damaged gzip member.
	Resyncs int
	// Quarantine is the reason the segment must be quarantined, or empty.
	Quarantine string
	// Rules counts, per named suppression rule, what the rule saw.
	Rules map[string]RuleCount
	// Incomplete counts request_payload records whose source says
	// payload_complete is false.
	Incomplete int
	// HeaderlessDropped counts, by record type, records removed by the
	// headerless suppression policy. A type outside Dynamo v1.4.0's set is
	// counted as "unknown".
	HeaderlessDropped map[string]int
}

// RuleCount is what one named suppression rule saw in a segment.
type RuleCount struct {
	// HeaderPresent counts evaluated records on which the rule's header
	// group had a value, whether or not it matched.
	HeaderPresent int
	// Matched counts records the rule acted on.
	Matched int
}

// Bad counts lines that could not be exported.
func (r Result) Bad() int { return r.Unparseable + r.LineTooLong + r.RowTooLarge + r.Cut }

// Preparer prepares segments with one validated configuration. It is safe
// for concurrent use.
type Preparer struct {
	cfg    Config
	engine *suppress.Engine
}

// New validates cfg and builds a preparer.
func New(cfg Config) (*Preparer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("prepare config: %w", err)
	}
	engine, err := suppress.New(cfg.Suppression)
	if err != nil {
		return nil, err
	}
	return &Preparer{cfg: cfg, engine: engine}, nil
}

// SuppressionActive reports whether any suppression rule is configured.
func (p *Preparer) SuppressionActive() bool { return p.engine.Active() }

// Prepare reads the segment at path and writes its records to w. Raw bytes
// of rejected lines go to rejects, which may be nil; they can hold user data
// and must stay local. The writer is closed when records were written and
// the segment is not quarantined, and aborted otherwise. An error means the
// segment was not prepared and can be retried.
func (p *Preparer) Prepare(ctx context.Context, path string, w Writer, rejects io.Writer) (result Result, err error) {
	if rejects == nil {
		rejects = io.Discard
	}
	rejectSink := &switchableWriter{w: rejects}
	lines, err := record.OpenLines(path, record.LineOptions{MaxLineBytes: p.cfg.RawLineMax, Reject: rejectSink})
	if err != nil {
		return result, errors.Join(err, w.Abort())
	}
	defer func() {
		if closeErr := lines.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	// legacy stops record handling at the first record from an unsupported
	// Dynamo release. The pass still reads to the end of the file, so the
	// fingerprint covers the whole file and the losses are counted.
	legacy := false
	for lines.Next() {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(err, w.Abort())
		}
		result.Received++
		if legacy {
			continue
		}
		line := lines.Line()
		value, decodeErr := compat.Decode(line)
		if decodeErr != nil {
			result.Unparseable++
			if err := reject(rejectSink, line); err != nil {
				return result, errors.Join(err, w.Abort())
			}
			continue
		}
		if isLegacyRecord(value) {
			legacy = true
			// The whole segment is quarantined, so nothing after this
			// record is rejected line by line.
			rejectSink.off = true
			continue
		}
		out, ok := p.transform(value, line, &result)
		if !ok {
			continue
		}
		if int64(len(out.Payload)) > p.cfg.RowMax {
			result.RowTooLarge++
			if err := reject(rejectSink, line); err != nil {
				return result, errors.Join(err, w.Abort())
			}
			continue
		}
		out.Line = lines.Number()
		if err := w.Write(out); err != nil {
			return result, errors.Join(fmt.Errorf("write record: %w", err), w.Abort())
		}
		result.Written++
		result.RedactedParts += out.RedactedParts
	}
	if err := lines.Err(); err != nil {
		return result, errors.Join(err, w.Abort())
	}
	if err := ctx.Err(); err != nil {
		return result, errors.Join(err, w.Abort())
	}

	stats := lines.Stats()
	result.Fingerprint = lines.Sum()
	result.LineTooLong = stats.Oversized
	result.Cut = stats.Cut
	result.LostBytes = stats.LostBytes
	result.Resyncs = stats.Resyncs
	if legacy {
		result.Quarantine = ReasonUnsupportedVersion
	} else {
		result.Quarantine = p.quarantineReason(result)
	}
	if result.Quarantine != "" || result.Written == 0 {
		return result, w.Abort()
	}
	if err := w.Close(); err != nil {
		return result, errors.Join(fmt.Errorf("close transport: %w", err), w.Abort())
	}
	return result, nil
}

// switchableWriter passes writes through until it is switched off, then
// discards them.
type switchableWriter struct {
	w   io.Writer
	off bool
}

func (s *switchableWriter) Write(p []byte) (int, error) {
	if s.off {
		return len(p), nil
	}
	return s.w.Write(p)
}

// transform applies suppression, then redaction, and reports false when the
// record is dropped. A suppressed record is not redacted: the stub keeps no
// body. The record is re-serialized only when something changed, so other
// records keep their source bytes.
func (p *Preparer) transform(value *compat.Value, line []byte, result *Result) (Record, bool) {
	event := suppress.EventOf(value)
	out := Record{
		Headers:        cloneAll(suppress.HeaderSets(event)),
		CarriesPayload: suppress.CarriesPayload(event),
		Payload:        line,
	}
	if out.CarriesPayload && payloadIncomplete(event) {
		result.Incomplete++
	}
	kept, outcome := p.engine.Apply(value)
	out.Suppression = outcome
	if outcome.Evaluated {
		result.Evaluated++
	}
	if outcome.HeaderPresent {
		result.HeaderPresent++
	}
	for _, name := range outcome.Present {
		count := result.Rules[name]
		count.HeaderPresent++
		result.countRule(name, count)
	}
	if outcome.Rule != "" {
		count := result.Rules[outcome.Rule]
		count.Matched++
		result.countRule(outcome.Rule, count)
	}
	switch outcome.Action {
	case suppress.DropRecord:
		if outcome.Headerless {
			if result.HeaderlessDropped == nil {
				result.HeaderlessDropped = map[string]int{}
			}
			result.HeaderlessDropped[recordType(event)]++
			return Record{}, false
		}
		result.Dropped++
		return Record{}, false
	case suppress.StripPayload:
		result.Suppressed++
		out.Value = kept
		out.Payload = compat.Encode(kept)
		return out, true
	}
	out.Value = value
	if p.cfg.Redact {
		redacted, parts := redact.Record(value)
		if parts > 0 {
			out.Value = redacted
			out.Payload = compat.Encode(redacted)
			out.RedactedParts = parts
		}
	}
	return out, true
}

func (r *Result) countRule(name string, count RuleCount) {
	if r.Rules == nil {
		r.Rules = map[string]RuleCount{}
	}
	r.Rules[name] = count
}

// payloadIncomplete reports a payload whose payload_complete is JSON false.
// A missing or non-boolean flag is not counted: the flag is parsed, never
// assumed.
func payloadIncomplete(event *compat.Value) bool {
	payload := event.Get("payload")
	if payload == nil || payload.Kind() != compat.Object {
		return false
	}
	complete, ok := payload.Get("payload_complete").Bool()
	return ok && !complete
}

// recordType returns event's type when Dynamo v1.4.0 defines it, and
// "unknown" otherwise, so it is safe as a metric label.
func recordType(event *compat.Value) string {
	if event != nil && event.Kind() == compat.Object {
		if text, ok := event.Get("event_type").String(); ok && record.EventType(text).Known() {
			return text
		}
	}
	return "unknown"
}

func (p *Preparer) quarantineReason(result Result) string {
	bad := result.Bad()
	total := result.Received + result.Cut + result.LineTooLong
	if total == 0 {
		if result.LostBytes > 0 {
			return ReasonUnreadable
		}
		return ""
	}
	if bad > 0 && float64(bad)/float64(total) >= p.cfg.MaxBadRecordFraction {
		return ReasonBadRecords
	}
	return ""
}

// isLegacyRecord detects a Dynamo v1.3.x AuditRecord: schema_version and no
// event_type, bare or under "event". Parsing one as a v1.4.0 record would
// produce empty fields rather than fail.
func isLegacyRecord(value *compat.Value) bool {
	body := value
	if value.Kind() == compat.Object && value.Has("event") {
		body = value.Get("event")
	}
	if body.Kind() != compat.Object {
		return false
	}
	version := body.Get("schema_version")
	eventType := body.Get("event_type")
	return version != nil && version.Kind() == compat.Int && (eventType == nil || eventType.Kind() == compat.Null)
}

func cloneAll(values []*compat.Value) []*compat.Value {
	if len(values) == 0 {
		return nil
	}
	out := make([]*compat.Value, len(values))
	for i, v := range values {
		out[i] = v.Clone()
	}
	return out
}

func reject(w io.Writer, line []byte) error {
	if _, err := w.Write(line); err != nil {
		return fmt.Errorf("write rejected line: %w", err)
	}
	if _, err := w.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("write rejected line: %w", err)
	}
	return nil
}
