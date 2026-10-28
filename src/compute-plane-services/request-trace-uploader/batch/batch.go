// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package batch groups prepared segments into upload batches, one lane per
// backend destination, so one backend call carries many segments.
//
// A destination is whatever the backend names: one of two tables in dual
// write, or one bucket and prefix. Batching is a backend capability: a
// backend that takes one file per call gets batches of one.
package batch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
)

const (
	DefaultMaxSegments = 16
	DefaultMaxBytes    = int64(1) << 30
	DefaultMaxFiles    = 500
	DefaultMaxWait     = 60 * time.Second
)

// Metric names. Integration maps them to telemetry instruments.
const (
	MetricBatchSegments = "batch_segments"
	MetricBatchBytes    = "batch_bytes"
)

var (
	// ErrMemberTooLarge reports a member that cannot fit any batch. It is a
	// fault of that segment.
	ErrMemberTooLarge = errors.New("member larger than the batch byte limit")
	// ErrReceiptNotSaved reports a receipt the backend returned that could
	// not be written to the journal. A restart treats the batch as
	// ambiguous.
	ErrReceiptNotSaved = errors.New("batch receipt not saved")
	// ErrEmptyReceipt reports a submit that succeeded without a receipt.
	// Nothing can be polled, and the backend may have acted, so the batch
	// is ambiguous.
	ErrEmptyReceipt = errors.New("backend accepted the batch without a receipt")
)

// Config sets when a batch closes: on the first of MaxSegments members,
// MaxBytes of member transport bytes, MaxFiles files, or MaxWait after the
// oldest member was prepared.
type Config struct {
	MaxSegments int
	MaxBytes    int64
	MaxFiles    int
	MaxWait     time.Duration
}

// DefaultConfig returns the decided defaults.
func DefaultConfig() Config {
	return Config{
		MaxSegments: DefaultMaxSegments,
		MaxBytes:    DefaultMaxBytes,
		MaxFiles:    DefaultMaxFiles,
		MaxWait:     DefaultMaxWait,
	}
}

// Validate rejects values that cannot form a batch.
func (c Config) Validate() error {
	var errs []error
	if c.MaxSegments < 1 {
		errs = append(errs, fmt.Errorf("batch max segments must be at least 1, got %d", c.MaxSegments))
	}
	if c.MaxBytes < 1 {
		errs = append(errs, fmt.Errorf("batch max bytes must be at least 1, got %d", c.MaxBytes))
	}
	if c.MaxFiles < 1 {
		errs = append(errs, fmt.Errorf("batch max files must be at least 1, got %d", c.MaxFiles))
	}
	if c.MaxWait <= 0 {
		errs = append(errs, fmt.Errorf("batch max wait must be positive, got %s", c.MaxWait))
	}
	return errors.Join(errs...)
}

// Limits is what the backend declares about one call. Integration fills it
// from the backend's capabilities.
type Limits struct {
	// Batching reports whether one call accepts many files.
	Batching bool
	// MaxBytes bounds the total source bytes of one call. Zero means no
	// bound.
	MaxBytes int64
	// MaxFiles bounds the files in one call. Zero means no bound.
	MaxFiles int
}

// Resolve applies the backend limits to cfg. A setting above a backend limit
// is lowered to it, and each change is returned as a warning.
func Resolve(cfg Config, limits Limits) (Config, []string) {
	var warnings []string
	if limits.MaxBytes > 0 && cfg.MaxBytes > limits.MaxBytes {
		warnings = append(warnings, fmt.Sprintf("batch max bytes %d is above the backend limit, using %d", cfg.MaxBytes, limits.MaxBytes))
		cfg.MaxBytes = limits.MaxBytes
	}
	if limits.MaxFiles > 0 && cfg.MaxFiles > limits.MaxFiles {
		warnings = append(warnings, fmt.Sprintf("batch max files %d is above the backend limit, using %d", cfg.MaxFiles, limits.MaxFiles))
		cfg.MaxFiles = limits.MaxFiles
	}
	return cfg, warnings
}

// File is one prepared transport file.
type File struct {
	Path   string
	SHA256 string
	Bytes  int64
	Rows   int64
}

// Member is one segment's transport for one destination.
type Member struct {
	SegmentID   string
	Destination string
	File        File
	PreparedAt  time.Time
}

// CloseReason says which trigger closed a batch.
type CloseReason string

const (
	ReasonSegments CloseReason = "segments"
	ReasonBytes    CloseReason = "bytes"
	ReasonFiles    CloseReason = "files"
	ReasonMaxWait  CloseReason = "max_wait"
	// ReasonSolo closes a batch of one: batching is off, or a failed batch
	// was split to find a bad member.
	ReasonSolo CloseReason = "solo"
)

// Batch is a closed batch, ready to be recorded and submitted.
type Batch struct {
	ID          string
	Destination string
	Members     []Member
	Bytes       int64
	Reason      CloseReason
	ClosedAt    time.Time
}

// Metrics receives one observation per closed batch.
type Metrics interface {
	Observe(name string, value float64)
}

// Options holds the batcher's collaborators. Zero values are safe.
type Options struct {
	Metrics Metrics
	Logger  *slog.Logger
	// NewID returns a unique batch id of letters, digits, '-' and '_'.
	NewID func() string
}

// Batcher keeps one open lane per destination. It is safe for concurrent
// use. Time is passed in, so the caller owns the clock.
type Batcher struct {
	mu       sync.Mutex
	cfg      Config
	batching bool
	metrics  Metrics
	newID    func() string
	lanes    map[string]*lane
}

// lane is the open batch for one destination.
type lane struct {
	members []Member
	bytes   int64
	oldest  time.Time
	queued  map[string]bool
}

// New builds a batcher. Settings above the backend limits are lowered with a
// warning in the log.
func New(cfg Config, limits Limits, opts Options) (*Batcher, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cfg, warnings := Resolve(cfg, limits)
	for _, warning := range warnings {
		logger.Warn("batch setting lowered to the backend limit", "detail", warning)
	}
	b := &Batcher{
		cfg:      cfg,
		batching: limits.Batching,
		metrics:  opts.Metrics,
		newID:    opts.NewID,
		lanes:    map[string]*lane{},
	}
	if b.newID == nil {
		b.newID = randomID
	}
	return b, nil
}

// Add puts a member in the lane for its destination and returns the batches
// that closed because of it. A member that would push the lane past
// MaxBytes closes the lane first and starts the next one. A member too large
// for any batch is ErrMemberTooLarge.
func (b *Batcher) Add(m Member, now time.Time) ([]Batch, error) {
	if err := validMember(m); err != nil {
		return nil, err
	}
	if m.File.Bytes > b.cfg.MaxBytes {
		return nil, fmt.Errorf("%w: segment %s has %d bytes, limit %d", ErrMemberTooLarge, m.SegmentID, m.File.Bytes, b.cfg.MaxBytes)
	}
	if !b.batching {
		return []Batch{b.seal(m.Destination, []Member{m}, m.File.Bytes, ReasonSolo, now)}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.lane(m.Destination)
	if l.queued[m.SegmentID] {
		return nil, fmt.Errorf("segment %s is already waiting for destination %s", m.SegmentID, m.Destination)
	}
	var closed []Batch
	if len(l.members) > 0 && l.bytes+m.File.Bytes > b.cfg.MaxBytes {
		closed = append(closed, b.closeLane(m.Destination, ReasonBytes, now))
		l = b.lane(m.Destination)
	}
	l.add(m)
	if reason, full := b.full(l, now); full {
		closed = append(closed, b.closeLane(m.Destination, reason, now))
	}
	return closed, nil
}

func (b *Batcher) lane(dest string) *lane {
	l := b.lanes[dest]
	if l == nil {
		l = &lane{queued: map[string]bool{}}
		b.lanes[dest] = l
	}
	return l
}

func (l *lane) add(m Member) {
	if len(l.members) == 0 || m.PreparedAt.Before(l.oldest) {
		l.oldest = m.PreparedAt
	}
	l.members = append(l.members, m)
	l.bytes += m.File.Bytes
	l.queued[m.SegmentID] = true
}

func (b *Batcher) full(l *lane, now time.Time) (CloseReason, bool) {
	switch {
	case len(l.members) >= b.cfg.MaxSegments:
		return ReasonSegments, true
	case len(l.members) >= b.cfg.MaxFiles:
		return ReasonFiles, true
	case l.bytes >= b.cfg.MaxBytes:
		return ReasonBytes, true
	case !now.Before(l.oldest.Add(b.cfg.MaxWait)):
		return ReasonMaxWait, true
	}
	return "", false
}

// Due closes every lane whose oldest member has waited MaxWait.
func (b *Batcher) Due(now time.Time) []Batch {
	b.mu.Lock()
	defer b.mu.Unlock()
	var closed []Batch
	for dest, l := range b.lanes {
		if len(l.members) > 0 && !now.Before(l.oldest.Add(b.cfg.MaxWait)) {
			closed = append(closed, b.closeLane(dest, ReasonMaxWait, now))
		}
	}
	return closed
}

// NextDue reports when the next lane reaches MaxWait.
func (b *Batcher) NextDue() (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var next time.Time
	found := false
	for _, l := range b.lanes {
		if len(l.members) == 0 {
			continue
		}
		due := l.oldest.Add(b.cfg.MaxWait)
		if !found || due.Before(next) {
			next, found = due, true
		}
	}
	return next, found
}

// Pending reports how many members wait in open lanes.
func (b *Batcher) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, l := range b.lanes {
		n += len(l.members)
	}
	return n
}

// Solo closes a batch of one member without touching the lanes. A split
// uses it to resubmit each member of a failed batch alone.
func (b *Batcher) Solo(m Member, now time.Time) (Batch, error) {
	if err := validMember(m); err != nil {
		return Batch{}, err
	}
	return b.seal(m.Destination, []Member{m}, m.File.Bytes, ReasonSolo, now), nil
}

func (b *Batcher) closeLane(dest string, reason CloseReason, now time.Time) Batch {
	l := b.lanes[dest]
	delete(b.lanes, dest)
	return b.seal(dest, l.members, l.bytes, reason, now)
}

func (b *Batcher) seal(dest string, members []Member, bytes int64, reason CloseReason, now time.Time) Batch {
	if b.metrics != nil {
		b.metrics.Observe(MetricBatchSegments, float64(len(members)))
		b.metrics.Observe(MetricBatchBytes, float64(bytes))
	}
	return Batch{
		ID:          b.newID(),
		Destination: dest,
		Members:     members,
		Bytes:       bytes,
		Reason:      reason,
		ClosedAt:    now,
	}
}

func validMember(m Member) error {
	if m.SegmentID == "" || m.Destination == "" || m.File.Path == "" || m.File.Bytes < 0 || m.PreparedAt.IsZero() {
		return errors.New("batch member needs a segment id, a destination, a file path, a non-negative size and a prepared time")
	}
	return nil
}

// randomID returns a sortable, unique id: the UTC time and 8 random bytes.
func randomID() string {
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix[:])
}

// Journal records batches durably. The journal store implements it through
// a small adapter.
type Journal interface {
	// OpenBatch records a closed batch before the backend is called.
	OpenBatch(b Batch, backend string) error
	// SaveReceipt records the id the backend returned for a batch.
	SaveReceipt(batchID, backend, receiptID string) error
}

// SubmitFunc sends one batch to the backend in one call that lists every
// member file. It returns the receipt the backend gave, which can be
// non-empty even when the error is not nil.
type SubmitFunc func(ctx context.Context, b Batch) (string, error)

// Submit records a new batch in the journal, then sends it. Nothing is sent
// if the record cannot be written.
func Submit(ctx context.Context, j Journal, backend string, b Batch, submit SubmitFunc) (string, error) {
	if err := j.OpenBatch(b, backend); err != nil {
		return "", fmt.Errorf("record batch before submit: %w", err)
	}
	return Send(ctx, j, backend, b, submit)
}

// Send submits a batch that is already in the journal and saves any receipt
// before the submit error is returned. A submit that succeeds without a
// receipt returns an ambiguous fault wrapping ErrEmptyReceipt.
func Send(ctx context.Context, j Journal, backend string, b Batch, submit SubmitFunc) (string, error) {
	receipt, submitErr := submit(ctx, b)
	if receipt == "" && submitErr == nil {
		return "", emptyReceiptFault()
	}
	if receipt == "" {
		return "", submitErr
	}
	if err := j.SaveReceipt(b.ID, backend, receipt); err != nil {
		return receipt, errors.Join(fmt.Errorf("%w: %w", ErrReceiptNotSaved, err), submitErr)
	}
	return receipt, submitErr
}

func emptyReceiptFault() error {
	return &backend.Fault{Class: backend.ClassAmbiguous, Scope: backend.ScopeBackend, Reason: backend.ReasonUnknown, Err: ErrEmptyReceipt}
}
