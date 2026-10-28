// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package shadow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

const (
	trackerFileName = "tracker.json"
	trackerVersion  = 1
)

// trackerState is what survives a restart, so segments the owner removed
// while the shadow was down are still counted.
type trackerState struct {
	V int `json:"v"`
	// Cursor is the highest index seen per prefix.
	Cursor map[string]int `json:"cursor"`
	// Pending lists segments seen and not yet read, by file name.
	Pending []string `json:"pending"`
	// Settled lists segments read or counted missed before a scan listed
	// them, until a scan covers their index.
	Settled []settledSegment `json:"settled"`
}

type settledSegment struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	Index  int    `json:"index"`
}

// Tracker counts segments the owner removed before the shadow read them, in
// telemetry.SegmentsMissed. It is safe for concurrent use.
//
// A segment is missed when it was seen in a scan and left the spool before
// it was read, when it was gone by the time prepare opened it, or when its
// index came and went between two scans so it was never seen at all. Dynamo
// numbers each prefix without gaps, so a skipped index is a missed segment.
type Tracker struct {
	path    string
	metrics telemetry.Metrics
	logger  *slog.Logger

	mu      sync.Mutex
	cursor  map[string]int
	pending map[string]bool
	// settled holds segments read or found gone before Observe saw the scan
	// that listed them: intake hands segments to the sink during its pass,
	// before the service reports the scan.
	settled map[string]settledSegment
	// inflight holds the reads under way. An open file stays readable after
	// the owner deletes it, so such a segment is not missed, and its index is
	// not a gap.
	inflight map[string]*inflightRead
}

type inflightRead struct {
	prefix string
	index  int
	reads  int
}

// NewTracker loads the tracker state from the shadow state directory. State
// it cannot read is logged and replaced, because it only feeds a counter.
func NewTracker(cfg Config, metrics telemetry.Metrics) (*Tracker, error) {
	cfg, err := cfg.resolved()
	if err != nil {
		return nil, err
	}
	if metrics == nil {
		return nil, errors.New("shadow tracker needs a metrics recorder")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o750); err != nil {
		return nil, fmt.Errorf("create shadow state directory: %w", err)
	}
	t := &Tracker{
		path:     filepath.Join(cfg.StateDir, trackerFileName),
		metrics:  metrics,
		logger:   slog.Default(),
		cursor:   map[string]int{},
		pending:  map[string]bool{},
		settled:  map[string]settledSegment{},
		inflight: map[string]*inflightRead{},
	}
	if err := t.load(); err != nil {
		t.logger.Warn("shadow tracker state is unreadable; starting fresh, so segments missed while the shadow was down are not counted",
			"reason", reason(err))
		t.cursor, t.pending, t.settled = map[string]int{}, map[string]bool{}, map[string]settledSegment{}
	}
	metrics.Add(telemetry.SegmentsMissed, 0, nil)
	return t, nil
}

// Observe takes one intake scan. It counts as missed every segment seen
// earlier and not yet read that is no longer on the spool, and every index
// that came and went between two scans. It returns how many it counted. An
// error means the state could not be saved; the count still stands.
//
// The first scan of a prefix counts nothing: what left the spool before the
// shadow started is not the shadow's to explain. A segment that leaves the
// spool while Track is reading it is not counted here. A highest index below the
// last one seen means the writer restarted its numbering on an empty spool,
// and counts nothing either.
func (t *Tracker) Observe(scan segment.Scan) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	present := map[string]bool{}
	byPrefix := map[string][]segment.Segment{}
	for _, seg := range slices.Concat(scan.Closed, scan.Active) {
		present[nameOf(seg)] = true
		byPrefix[seg.Prefix] = append(byPrefix[seg.Prefix], seg)
	}
	missed := 0
	changed := false
	for prefix, segs := range byPrefix {
		indexes := map[int]bool{}
		highest := segs[0].Index
		for _, seg := range segs {
			indexes[seg.Index] = true
			highest = max(highest, seg.Index)
		}
		for _, done := range t.settled {
			if done.Prefix == prefix {
				indexes[done.Index] = true
			}
		}
		for _, r := range t.inflight {
			if r.prefix == prefix {
				indexes[r.index] = true
			}
		}
		last, seen := t.cursor[prefix]
		fresh := !seen || highest < last
		if !fresh {
			for index := last + 1; index < highest; index++ {
				if !indexes[index] {
					missed++
				}
			}
		}
		for _, seg := range segs {
			changed = t.markPendingLocked(nameOf(seg), seg.Index, fresh, last) || changed
		}
		// A read under way counts as seen, so its outcome settles it.
		for name, r := range t.inflight {
			if r.prefix == prefix {
				changed = t.markPendingLocked(name, r.index, fresh, last) || changed
			}
		}
		if !seen || highest != last {
			t.cursor[prefix] = highest
			changed = true
		}
		for name, done := range t.settled {
			if done.Prefix == prefix && done.Index <= highest {
				delete(t.settled, name)
				changed = true
			}
		}
	}
	for name := range t.pending {
		if !present[name] && t.inflight[name] == nil {
			delete(t.pending, name)
			missed++
			changed = true
		}
	}
	t.count(missed)
	if !changed {
		return missed, nil
	}
	if err := t.saveLocked(); err != nil {
		return missed, err
	}
	return missed, nil
}

// Track runs read, the step that opens and reads seg, such as hashing and
// prepare, and records the outcome. A source gone when read opened it is
// counted as missed if the shadow had not read it before; Track then returns
// true and a nil error, and the caller releases the segment instead of
// retrying. Any other error is returned and the segment stays unread.
func (t *Tracker) Track(seg segment.Segment, read func() error) (bool, error) {
	name := nameOf(seg)
	t.mu.Lock()
	r := t.inflight[name]
	if r == nil {
		r = &inflightRead{prefix: seg.Prefix, index: seg.Index}
		t.inflight[name] = r
	}
	r.reads++
	t.mu.Unlock()
	finished := false
	defer func() {
		if !finished {
			t.mu.Lock()
			t.endReadLocked(name)
			t.mu.Unlock()
		}
	}()
	err := read()
	finished = true

	// Ending the read and recording its outcome happen under one lock, so
	// no scan can see the segment gone, unread and no longer being read.
	t.mu.Lock()
	defer t.mu.Unlock()
	t.endReadLocked(name)
	switch {
	case err == nil:
		t.settleLocked(seg, false)
		return false, nil
	case IsGone(err, seg.Path):
		t.settleLocked(seg, true)
		return true, nil
	default:
		return false, err
	}
}

func (t *Tracker) endReadLocked(name string) {
	r := t.inflight[name]
	if r == nil {
		return
	}
	if r.reads--; r.reads <= 0 {
		delete(t.inflight, name)
	}
}

// markPendingLocked starts waiting for a segment newer than the last scan,
// and reports whether it did.
func (t *Tracker) markPendingLocked(name string, index int, fresh bool, last int) bool {
	_, done := t.settled[name]
	if (fresh || index > last) && !done && !t.pending[name] {
		t.pending[name] = true
		return true
	}
	return false
}

// settleLocked records that seg was read, or was gone when it was opened.
// A gone segment is counted unless it was accounted for before: it is not
// pending, and an earlier scan already listed its index. After the writer
// restarts its numbering, a gone segment whose index an earlier scan listed
// is not counted, so that case can only undercount.
func (t *Tracker) settleLocked(seg segment.Segment, gone bool) {
	name := nameOf(seg)
	if t.pending[name] {
		delete(t.pending, name)
		if gone {
			t.count(1)
		}
		t.persistLocked()
		return
	}
	if _, done := t.settled[name]; done {
		return
	}
	if gone && !t.unseenLocked(seg) {
		return
	}
	t.settled[name] = settledSegment{Name: name, Prefix: seg.Prefix, Index: seg.Index}
	if gone {
		t.count(1)
	}
	t.persistLocked()
}

// IsGone reports whether err says the file at path did not exist when it was
// opened. It looks only for that open, so a missing transport directory or
// any other failure is never mistaken for a missed source.
func IsGone(err error, path string) bool {
	path = filepath.Clean(path)
	return anyError(err, func(err error) bool {
		pathErr, ok := err.(*fs.PathError)
		return ok && pathErr.Op == "open" && filepath.Clean(pathErr.Path) == path && errors.Is(pathErr.Err, fs.ErrNotExist)
	})
}

// anyError walks err's whole tree, joined errors included, since prepare
// joins the open failure with the writer's abort.
func anyError(err error, match func(error) bool) bool {
	if err == nil {
		return false
	}
	if match(err) {
		return true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() error }:
		return anyError(wrapped.Unwrap(), match)
	case interface{ Unwrap() []error }:
		for _, inner := range wrapped.Unwrap() {
			if anyError(inner, match) {
				return true
			}
		}
	}
	return false
}

func nameOf(seg segment.Segment) string { return filepath.Base(seg.Path) }

// unseenLocked reports whether seg is newer than every scan Observe took.
func (t *Tracker) unseenLocked(seg segment.Segment) bool {
	last, seen := t.cursor[seg.Prefix]
	return !seen || seg.Index > last
}

func (t *Tracker) count(missed int) {
	if missed > 0 {
		t.metrics.Add(telemetry.SegmentsMissed, float64(missed), nil)
	}
}

func (t *Tracker) load() error {
	data, err := os.ReadFile(t.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state trackerState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.V != trackerVersion {
		return fmt.Errorf("tracker state version %d is not %d", state.V, trackerVersion)
	}
	for prefix, index := range state.Cursor {
		t.cursor[prefix] = index
	}
	for _, name := range state.Pending {
		t.pending[name] = true
	}
	for _, done := range state.Settled {
		t.settled[done.Name] = done
	}
	return nil
}

// persistLocked saves the state where the caller has no error to return. A
// failed save is logged and repeated on the next change.
func (t *Tracker) persistLocked() {
	if err := t.saveLocked(); err != nil {
		t.logger.Warn("could not save shadow tracker state", "reason", reason(err))
	}
}

func (t *Tracker) saveLocked() error {
	state := trackerState{V: trackerVersion, Cursor: t.cursor, Pending: make([]string, 0, len(t.pending))}
	for name := range t.pending {
		state.Pending = append(state.Pending, name)
	}
	sort.Strings(state.Pending)
	for _, done := range t.settled {
		state.Settled = append(state.Settled, done)
	}
	sort.Slice(state.Settled, func(i, j int) bool { return state.Settled[i].Name < state.Settled[j].Name })
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode shadow tracker state: %w", err)
	}
	if err := writeFileAtomic(t.path, data); err != nil {
		return fmt.Errorf("save shadow tracker state: %w", err)
	}
	return nil
}

// writeFileAtomic replaces path with data, so a crash leaves the old or the
// new version and never a partial one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err == nil {
		err = syncDir(dir)
	}
	if err != nil {
		return errors.Join(err, removeIfPresent(file.Name()))
	}
	return nil
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(handle.Sync(), handle.Close())
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// reason is a log-safe description of err: the system error without the
// path, which logs must not carry.
func reason(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return "malformed state"
	}
	return "unreadable state"
}
