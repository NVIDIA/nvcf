// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DefaultPrepareAhead bounds how many handed-off segments may wait for
	// submission. Each one costs disk for its transport file.
	DefaultPrepareAhead = 16
	// DefaultBatchSegments is the batcher's default segment count per
	// upload. Prepare-ahead below it could never fill a batch.
	DefaultBatchSegments = 16
)

// Sink receives closed segments. The prepare stage implements it. Accept
// returning nil means the sink owns the segment until it calls
// Intake.Release; an error leaves the segment for the next pass.
type Sink interface {
	Accept(context.Context, Segment) error
}

// Gate is the free-space guard. Admit reports whether there is room to
// prepare the segment now. When it refuses, the pass stops handing off, so
// already prepared work can drain first.
type Gate interface {
	Admit(Segment) bool
}

// Config bounds spool intake.
type Config struct {
	// Prefixes lists the segment families to scan. See ValidatePrefixes.
	Prefixes []string
	// PrepareAhead bounds how many segments may be handed off and not yet
	// released at once.
	PrepareAhead int
	// BatchSegments is the batcher's segment count per upload. PrepareAhead
	// must be at least this, or a full batch can never form.
	BatchSegments int
}

// DefaultConfig returns the defaults for the given prefixes.
func DefaultConfig(prefixes ...string) Config {
	return Config{
		Prefixes:      prefixes,
		PrepareAhead:  DefaultPrepareAhead,
		BatchSegments: DefaultBatchSegments,
	}
}

// Validate rejects any setting intake cannot run with.
func (c Config) Validate() error {
	if err := ValidatePrefixes(c.Prefixes); err != nil {
		return err
	}
	if c.BatchSegments < 1 {
		return fmt.Errorf("batch segments must be at least 1, got %d", c.BatchSegments)
	}
	if c.PrepareAhead < c.BatchSegments {
		return fmt.Errorf("prepare ahead (%d) must be at least the batch segment count (%d)", c.PrepareAhead, c.BatchSegments)
	}
	return nil
}

// identity tells one file from a later file that reuses its name. A writer
// restarts its index at the highest existing index plus one, so a name comes
// back only after every higher file is gone; the reused file is new content.
type identity struct {
	name    string
	size    int64
	modTime time.Time
}

func identify(item Segment) identity {
	return identity{name: filepath.Base(item.Path), size: item.Size, modTime: item.ModTime}
}

// Intake scans the spool and hands each closed segment to the sink once.
// It never deletes or changes a file.
type Intake struct {
	directory string
	cfg       Config
	sink      Sink
	gate      Gate

	mu sync.Mutex
	// handed maps every segment handed off and still on the spool to
	// whether it still holds a prepare-ahead slot.
	handed map[identity]bool
	ahead  int
}

// PassResult summarizes one intake pass.
type PassResult struct {
	Scan Scan
	// Handed counts segments accepted by the sink in this pass.
	Handed int
	// Deferred counts closed segments left for a later pass because the
	// prepare-ahead bound or the gate stopped the pass.
	Deferred int
	// GateClosed reports that the gate refused a segment in this pass.
	GateClosed bool
}

// NewIntake builds an intake for directory. The configuration is validated
// here, so a bad setting fails at startup.
func NewIntake(directory string, cfg Config, sink Sink, gate Gate) (*Intake, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("spool intake config: %w", err)
	}
	if sink == nil {
		return nil, errors.New("spool intake needs a sink")
	}
	if gate == nil {
		return nil, errors.New("spool intake needs a free-space gate")
	}
	return &Intake{
		directory: directory,
		cfg:       cfg,
		sink:      sink,
		gate:      gate,
		handed:    map[identity]bool{},
	}, nil
}

// Pass scans the spool once and hands off closed segments in order until
// the prepare-ahead bound or the gate stops it.
func (i *Intake) Pass(ctx context.Context) (PassResult, error) {
	if err := ctx.Err(); err != nil {
		return PassResult{}, err
	}
	scan, err := ScanDir(i.directory, i.cfg.Prefixes)
	if err != nil {
		return PassResult{}, err
	}
	result := PassResult{Scan: scan}
	i.forgetRemoved(scan)

	pending := make([]Segment, 0, len(scan.Closed))
	for _, item := range scan.Closed {
		if !i.isHanded(item) {
			pending = append(pending, item)
		}
	}
	for n, item := range pending {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !i.takeSlot() {
			result.Deferred = len(pending) - n
			break
		}
		if !i.gate.Admit(item) {
			i.returnSlot()
			result.GateClosed = true
			result.Deferred = len(pending) - n
			break
		}
		// Record the hand-off before the sink sees the segment, so a worker
		// that finishes and calls Release before Accept returns frees the slot.
		i.markHanded(item)
		if err := i.sink.Accept(ctx, item); err != nil {
			i.unmarkHanded(item)
			return result, fmt.Errorf("hand off request trace segment %d: %w", item.Index, err)
		}
		result.Handed++
		slog.Info("request trace segment found",
			"prefix", item.Prefix,
			"segment", item.Index,
			"bytes", item.Size)
	}
	return result, nil
}

// Release frees the prepare-ahead slot of a handed-off segment. The next
// stage calls it once the segment no longer waits for submission. Releasing
// twice, or releasing a segment that was never handed off, does nothing.
func (i *Intake) Release(item Segment) {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := identify(item)
	if holds, ok := i.handed[key]; ok && holds {
		i.handed[key] = false
		i.ahead--
	}
}

func (i *Intake) isHanded(item Segment) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	_, ok := i.handed[identify(item)]
	return ok
}

func (i *Intake) takeSlot() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.ahead >= i.cfg.PrepareAhead {
		return false
	}
	i.ahead++
	return true
}

func (i *Intake) returnSlot() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ahead--
}

func (i *Intake) markHanded(item Segment) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.handed[identify(item)] = true
}

// unmarkHanded undoes markHanded for a segment the sink refused, returning
// its slot unless a Release already did.
func (i *Intake) unmarkHanded(item Segment) {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := identify(item)
	if i.handed[key] {
		i.ahead--
	}
	delete(i.handed, key)
}

// forgetRemoved drops segments that left the spool, so a later file that
// reuses a name is new work, and frees any slot a removed segment still held.
func (i *Intake) forgetRemoved(scan Scan) {
	present := make(map[identity]struct{}, len(scan.Closed)+len(scan.Active))
	for _, item := range scan.Closed {
		present[identify(item)] = struct{}{}
	}
	for _, item := range scan.Active {
		present[identify(item)] = struct{}{}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for key, holds := range i.handed {
		if _, ok := present[key]; ok {
			continue
		}
		if holds {
			i.ahead--
		}
		delete(i.handed, key)
	}
}
