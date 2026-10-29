// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package journalfs stores the journal as one JSON file per segment and per
// batch under the state directory on the spool volume.
//
// Every write goes to a temporary file, is synced, renamed over the old file,
// and the directory is synced, so a crash leaves either the old or the new
// version and never a partial one. The in-memory copy changes only after the
// write is durable.
package journalfs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

// ErrClosed reports a write after Close released the spool lock, when
// another uploader may already own the journal.
var ErrClosed = errors.New("journal store is closed")

// Counter names. Integration maps them to telemetry instruments.
const (
	CounterUnreadable       = "journal_unreadable"
	CounterBatchesAmbiguous = "batches_ambiguous"
	CounterBackendResets    = "journal_backend_resets"
)

// Counter receives event counts.
type Counter interface {
	Add(name string, delta int64)
}

// Options holds the store's collaborators. Zero values are safe.
type Options struct {
	Now     func() time.Time
	Counter Counter
	Logger  *slog.Logger
}

// hooks lets tests stop a write at a chosen point, as a crash would, and
// observe the order of durable steps.
type hooks struct {
	beforeRename  func(file string) error
	beforeSyncDir func(dir string) error
	// event reports each completed step: "sync-file", "rename" or
	// "sync-dir", with the path it applied to.
	event func(kind, path string)
}

func (h hooks) emit(kind, path string) {
	if h.event != nil {
		h.event(kind, path)
	}
}

// Store is the file-backed journal. It is safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	cfg      Config
	layout   layout
	now      func() time.Time
	counter  Counter
	logger   *slog.Logger
	lock     *spoolLock
	segments map[string]*journal.Segment
	batches  map[string]*journal.Batch
	hooks    hooks
}

// Open takes the spool lock, then loads the journal. An entry it cannot read
// is fenced with its source. An entry whose source it cannot identify stops
// Open with journal.ErrUnreadableState; the caller reports not ready with
// journal.NotReadyReason and processes nothing.
func Open(ctx context.Context, cfg Config, opts Options) (*Store, error) {
	return openStore(ctx, cfg, opts, hooks{})
}

func openStore(ctx context.Context, cfg Config, opts Options, h hooks) (*Store, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Store{
		cfg:      cfg,
		layout:   layout{root: cfg.StateDir},
		now:      opts.Now,
		counter:  opts.Counter,
		logger:   opts.Logger,
		segments: map[string]*journal.Segment{},
		batches:  map[string]*journal.Batch{},
		hooks:    h,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.counter == nil {
		s.counter = discardCounter{}
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	for _, name := range []string{CounterUnreadable, CounterBatchesAmbiguous, CounterBackendResets} {
		s.counter.Add(name, 0)
	}
	for _, dir := range s.layout.dirs() {
		if err := mkdirDurable(s.hooks, dir); err != nil {
			return nil, err
		}
	}
	if err := sameVolume(cfg.SpoolDir, cfg.StateDir); err != nil {
		return nil, err
	}
	lock, err := acquireLock(ctx, s.layout.lockFile(), cfg.Holder, cfg.LockWait, s.now)
	if err != nil {
		return nil, err
	}
	s.lock = lock
	if previous := lock.previous; previous.Holder != "" && previous.Holder != cfg.Holder {
		s.logger.Info("took spool lock from an earlier uploader",
			"previous_holder", previous.Holder,
			"previous_since", previous.Since)
	}
	if err := s.load(); err != nil {
		_ = s.lock.release()
		return nil, err
	}
	return s, nil
}

// Close releases the spool lock. Every later write returns ErrClosed. It is
// safe to call more than once.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	err := s.lock.release()
	s.lock = nil
	return err
}

// StateDir returns the journal directory.
func (s *Store) StateDir() string { return s.layout.root }

// TransportDir returns the directory transport writers put their files in.
// Files there must be named by TransportPath, so recovery can find them.
func (s *Store) TransportDir() string { return s.layout.transport() }

// Discover records a closed segment at Discovered, keyed by its content
// fingerprint. It returns the existing entry, in whatever state, when the
// segment is already known; created reports whether a new entry was written.
func (s *Store) Discover(info journal.SegmentInfo) (journal.Segment, bool, error) {
	if err := validateInfo(info); err != nil {
		return journal.Segment{}, false, err
	}
	id := journal.SegmentID(info.Prefix, info.Index, info.Size, info.SHA256)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.segments[id]; ok {
		return copySegment(existing), false, nil
	}
	now := s.now()
	seg := &journal.Segment{
		V:            journal.SchemaVersion,
		ID:           id,
		Name:         info.Name,
		Prefix:       info.Prefix,
		Index:        info.Index,
		Size:         info.Size,
		ModTime:      info.ModTime,
		SHA256:       info.SHA256,
		State:        journal.StateDiscovered,
		DiscoveredAt: now,
		UpdatedAt:    now,
	}
	if err := s.writeSegment(seg); err != nil {
		return journal.Segment{}, false, err
	}
	s.segments[id] = seg
	return copySegment(seg), true, nil
}

func validateInfo(info journal.SegmentInfo) error {
	var errs []error
	if !validSegmentName(info.Name) {
		errs = append(errs, errors.New("segment name must be a file name"))
	}
	if info.Prefix == "" {
		errs = append(errs, errors.New("segment prefix is required"))
	}
	if info.Index < 0 || info.Size < 0 {
		errs = append(errs, errors.New("segment index and size must not be negative"))
	}
	if !segmentIDPattern.MatchString(info.SHA256) {
		errs = append(errs, errors.New("segment content sha256 must be 64 lowercase hex characters"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("discover segment: %w", err)
	}
	return nil
}

// Segment returns a copy of one segment entry.
func (s *Store) Segment(id string) (journal.Segment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seg, ok := s.segments[id]
	if !ok {
		return journal.Segment{}, false
	}
	return copySegment(seg), true
}

// Segments returns copies of every segment entry, ordered by prefix and
// index.
func (s *Store) Segments() []journal.Segment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]journal.Segment, 0, len(s.segments))
	for _, seg := range s.segments {
		out = append(out, copySegment(seg))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Prefix != out[j].Prefix {
			return out[i].Prefix < out[j].Prefix
		}
		if out[i].Index != out[j].Index {
			return out[i].Index < out[j].Index
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// BeginPrepare moves a segment from Discovered to Preparing.
func (s *Store) BeginPrepare(id string) error {
	return s.updateSegment(id, func(seg *journal.Segment) error {
		if err := transition(seg, journal.StatePreparing); err != nil {
			return err
		}
		seg.PrepareStartedAt = s.now()
		return nil
	})
}

// MarkPrepared records the transports written for each destination, in the
// format of the named backend, and moves the segment to Prepared.
func (s *Store) MarkPrepared(id, backendName string, transports map[string]journal.Transport) error {
	if backendName == "" || len(transports) == 0 {
		return errors.New("mark prepared: a backend name and at least one transport are required")
	}
	for dest, transport := range transports {
		if dest == "" {
			return errors.New("mark prepared: every transport needs a destination")
		}
		if err := ownedTransport(s.layout.transport(), id, transport.Path); err != nil {
			return fmt.Errorf("mark prepared: %w", err)
		}
	}
	return s.updateSegment(id, func(seg *journal.Segment) error {
		if err := transition(seg, journal.StatePrepared); err != nil {
			return err
		}
		seg.Backend = backendName
		seg.Destinations = make(map[string]*journal.Destination, len(transports))
		for dest, transport := range transports {
			seg.Destinations[dest] = &journal.Destination{State: journal.StatePrepared, Transport: transport}
		}
		seg.PreparedAt = s.now()
		return nil
	})
}

// Reprepare sends a prepared segment whose transports were never put in a
// batch back to Discovered and removes the transports, so it is prepared
// again, for example under a changed record-handling policy. A segment with
// any destination in a batch is refused: that work may already be sent.
func (s *Store) Reprepare(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	seg, ok := s.segments[id]
	if !ok {
		return fmt.Errorf("%w: segment %s", journal.ErrNotFound, shortID(id))
	}
	if seg.State != journal.StatePrepared {
		return fmt.Errorf("%w: segment %s is %s, not prepared", journal.ErrInvalidTransition, shortID(id), seg.State)
	}
	for _, dest := range seg.Destinations {
		if dest.State != journal.StatePrepared || dest.BatchID != "" {
			return fmt.Errorf("%w: segment %s is in a batch", journal.ErrInvalidTransition, shortID(id))
		}
	}
	return s.resetSegmentLocked(id)
}

// MarkEmpty records that a segment held no records.
func (s *Store) MarkEmpty(id string) error {
	return s.updateSegment(id, func(seg *journal.Segment) error {
		return transition(seg, journal.StateEmpty)
	})
}

// Quarantine records that a segment was moved to quarantine. reason is a
// bounded code, never error text.
func (s *Store) Quarantine(id, reason string) error {
	return s.updateSegment(id, func(seg *journal.Segment) error {
		if err := transition(seg, journal.StateQuarantined); err != nil {
			return err
		}
		seg.Reason = reason
		return nil
	})
}

// updateSegment applies change to a copy of the entry, writes the copy, and
// only then replaces the in-memory entry.
func (s *Store) updateSegment(id string, change func(*journal.Segment) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateSegmentLocked(id, change)
}

func (s *Store) updateSegmentLocked(id string, change func(*journal.Segment) error) error {
	current, ok := s.segments[id]
	if !ok {
		return fmt.Errorf("%w: segment %s", journal.ErrNotFound, shortID(id))
	}
	next := copySegment(current)
	if err := change(&next); err != nil {
		return err
	}
	next.UpdatedAt = s.now()
	if err := s.writeSegment(&next); err != nil {
		return err
	}
	s.segments[id] = &next
	return nil
}

// openLocked refuses writes once Close released the spool lock.
func (s *Store) openLocked() error {
	if s.lock == nil {
		return ErrClosed
	}
	return nil
}

func (s *Store) writeSegment(seg *journal.Segment) error {
	if err := s.openLocked(); err != nil {
		return err
	}
	return writeJSONAtomic(s.layout.segments(), segmentFileName(seg.Name, seg.ID), seg, s.hooks)
}

func (s *Store) writeBatch(b *journal.Batch) error {
	if err := s.openLocked(); err != nil {
		return err
	}
	return writeJSONAtomic(s.layout.batches(), batchFileName(b.ID), b, s.hooks)
}

func transition(seg *journal.Segment, to journal.State) error {
	if !journal.CanTransition(seg.State, to) {
		return fmt.Errorf("%w: segment %s from %s to %s", journal.ErrInvalidTransition, shortID(seg.ID), seg.State, to)
	}
	seg.State = to
	return nil
}

// shortID keeps log lines and errors short; the full id is in the journal.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

type discardCounter struct{}

func (discardCounter) Add(string, int64) {}

func copySegment(seg *journal.Segment) journal.Segment {
	out := *seg
	if seg.Destinations != nil {
		out.Destinations = make(map[string]*journal.Destination, len(seg.Destinations))
		for name, dest := range seg.Destinations {
			d := *dest
			out.Destinations[name] = &d
		}
	}
	return out
}

func copyBatch(b *journal.Batch) journal.Batch {
	out := *b
	out.Members = append([]journal.Member(nil), b.Members...)
	if b.Receipt != nil {
		r := *b.Receipt
		out.Receipt = &r
	}
	return out
}
