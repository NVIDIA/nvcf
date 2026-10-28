// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package journal defines the durable state the uploader keeps for every
// segment and every upload batch: the records, the state machine, and the
// actions a restart takes. The file-backed store lives in internal/journalfs.
//
// A segment is identified by its content fingerprint, not its file name,
// because Dynamo reuses file names across pod lifetimes. Upload state that
// several segments share (submitting, the backend receipt, retries) lives in
// one batch record, so every member of a batch moves together.
package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"
)

// SchemaVersion is written as "v" in every record. A record with any other
// version is never acted on.
const SchemaVersion = 1

// State is a segment or batch lifecycle state.
type State string

const (
	StateDiscovered  State = "discovered"
	StatePreparing   State = "preparing"
	StatePrepared    State = "prepared"
	StateSubmitting  State = "submitting"
	StateSubmitted   State = "submitted"
	StateRetryWait   State = "retry_wait"
	StateAmbiguous   State = "ambiguous"
	StateConfirmed   State = "confirmed"
	StateCleaning    State = "cleaning"
	StateDone        State = "done"
	StateEmpty       State = "empty"
	StateQuarantined State = "quarantined"
	StateStranded    State = "stranded"
	// StateReleased ends a batch whose members go back to Prepared so they
	// can be batched again: a split into solo jobs, or a member quarantined
	// from a solo job.
	StateReleased State = "released"
)

// Reason codes recorded on terminal entries. They are bounded codes, never
// error text, so they are safe to log and count.
const (
	ReasonUploaded = "uploaded"
	ReasonEmpty    = "empty"
	ReasonEvicted  = "evicted"
)

var (
	// ErrInvalidTransition reports a state change the state machine forbids.
	ErrInvalidTransition = errors.New("invalid journal state transition")
	// ErrNotFound reports an unknown segment or batch id.
	ErrNotFound = errors.New("journal entry not found")
	// ErrReceiptExists reports an attempt to overwrite a batch receipt. A
	// receipt is written once; a resubmit after a receipt is a new batch.
	ErrReceiptExists = errors.New("batch already has a different receipt")
	// ErrUnreadableState reports a journal file the uploader cannot read
	// and whose source it cannot identify. The spool stops and reports not
	// ready instead of guessing.
	ErrUnreadableState = errors.New("unreadable journal state")
	// ErrLocked reports that another uploader holds the spool lock.
	ErrLocked = errors.New("spool lock held by another uploader")
)

// NotReadyUnreadableState is the readiness reason while the spool is stopped
// by ErrUnreadableState.
const NotReadyUnreadableState = "unreadable_state"

// NotReadyReason returns the readiness reason for an error that stops the
// spool, or "" when the error does not.
func NotReadyReason(err error) string {
	if errors.Is(err, ErrUnreadableState) {
		return NotReadyUnreadableState
	}
	return ""
}

// SegmentInfo is what discovery knows about one closed segment.
type SegmentInfo struct {
	Name    string
	Prefix  string
	Index   int
	Size    int64
	ModTime time.Time
	// SHA256 is the hex sha256 of the whole source file.
	SHA256 string
}

// Transport is one prepared file for one destination.
type Transport struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Rows   int64  `json:"rows"`
	Bytes  int64  `json:"bytes"`
}

// Destination is one segment's progress toward one backend destination, for
// example one of two tables in dual write, or one bucket and prefix.
type Destination struct {
	State       State     `json:"state"`
	Transport   Transport `json:"transport"`
	BatchID     string    `json:"batch_id,omitempty"`
	ConfirmedAt time.Time `json:"confirmed_at,omitzero"`
}

// Segment is the journal record for one source segment.
type Segment struct {
	V       int       `json:"v"`
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Prefix  string    `json:"prefix"`
	Index   int       `json:"index"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	SHA256  string    `json:"sha256"`
	State   State     `json:"state"`
	// Backend names the backend whose format the transports were written
	// for. A different backend never submits them.
	Backend      string                  `json:"backend,omitempty"`
	Destinations map[string]*Destination `json:"destinations,omitempty"`
	Reason       string                  `json:"reason,omitempty"`

	DiscoveredAt     time.Time `json:"discovered_at,omitzero"`
	PrepareStartedAt time.Time `json:"prepare_started_at,omitzero"`
	PreparedAt       time.Time `json:"prepared_at,omitzero"`
	ConfirmedAt      time.Time `json:"confirmed_at,omitzero"`
	CleaningAt       time.Time `json:"cleaning_at,omitzero"`
	DoneAt           time.Time `json:"done_at,omitzero"`
	UpdatedAt        time.Time `json:"updated_at,omitzero"`
}

// Receipt is what a backend returned for an accepted upload: a job id for an
// asynchronous backend or an object key for an immediate one. Backend names
// the backend that issued it, so a receipt is never read as another
// backend's.
type Receipt struct {
	Backend string    `json:"backend"`
	ID      string    `json:"id"`
	At      time.Time `json:"at,omitzero"`
}

// Member is one segment's transport inside a batch.
type Member struct {
	SegmentID string `json:"segment_id"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	Rows      int64  `json:"rows"`
}

// Batch is the journal record for one upload batch. It is written before the
// backend is called, and it is the source of truth for membership.
type Batch struct {
	V           int       `json:"v"`
	ID          string    `json:"id"`
	Backend     string    `json:"backend"`
	Destination string    `json:"destination"`
	State       State     `json:"state"`
	Members     []Member  `json:"members"`
	Receipt     *Receipt  `json:"receipt,omitempty"`
	Attempts    int       `json:"attempts"`
	NextRetryAt time.Time `json:"next_retry_at,omitzero"`
	// LastErrorClass is a bounded error class, never error text.
	LastErrorClass string    `json:"last_error_class,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitzero"`
	SubmittedAt    time.Time `json:"submitted_at,omitzero"`
	ConfirmedAt    time.Time `json:"confirmed_at,omitzero"`
	UpdatedAt      time.Time `json:"updated_at,omitzero"`
}

// SegmentID derives the journal key from the segment's identity: prefix,
// index, size and the sha256 of its content. A reused file name with new
// content gets a new key.
func SegmentID(prefix string, index int, size int64, contentSHA256 string) string {
	hash := sha256.New()
	// NUL never appears in a file name prefix, so fields cannot run together.
	for _, field := range []string{prefix, strconv.Itoa(index), strconv.FormatInt(size, 10), contentSHA256} {
		hash.Write([]byte(field))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// segmentTransitions is the segment state machine. Upload progress between
// Submitting and Confirmed is held by the batch record, so a segment only
// records that it is in flight. Eviction is the one loss path outside this
// table; the store applies it explicitly.
var segmentTransitions = map[State][]State{
	StateDiscovered: {StatePreparing},
	StatePreparing:  {StatePrepared, StateEmpty, StateQuarantined, StateDiscovered},
	StatePrepared:   {StateSubmitting, StateQuarantined, StateDiscovered},
	// Submitting goes back to Prepared when its batch is released, and
	// to Discovered when its backend is no longer linked.
	StateSubmitting:  {StatePrepared, StateConfirmed, StateQuarantined, StateDiscovered},
	StateConfirmed:   {StateCleaning},
	StateCleaning:    {StateDone},
	StateEmpty:       {StateDone},
	StateQuarantined: {StateDone},
}

var batchTransitions = map[State][]State{
	StateSubmitting: {StateSubmitted, StateRetryWait, StateAmbiguous, StateReleased},
	StateRetryWait:  {StateSubmitting, StateReleased},
	// An ambiguous batch with a receipt is polled, so it can still succeed.
	// One without a receipt waits in RetryWait when it is retained.
	StateAmbiguous: {StateSubmitting, StateReleased, StateConfirmed, StateRetryWait},
	StateSubmitted: {StateConfirmed, StateAmbiguous, StateStranded, StateReleased},
	StateStranded:  {StateConfirmed},
}

// CanTransition reports whether a segment may move from one state to another.
func CanTransition(from, to State) bool {
	return allowed(segmentTransitions, from, to)
}

// CanTransitionBatch reports whether a batch may move from one state to
// another.
func CanTransitionBatch(from, to State) bool {
	return allowed(batchTransitions, from, to)
}

func allowed(table map[State][]State, from, to State) bool {
	for _, next := range table[from] {
		if next == to {
			return true
		}
	}
	return false
}

// Action is what a restart does next for one segment or batch.
type Action string

const (
	ActionNone    Action = "none"
	ActionPrepare Action = "prepare"
	// ActionBatch puts prepared destinations into a batch.
	ActionBatch Action = "batch"
	// ActionSubmit submits a batch that has no receipt. When Ambiguous is
	// set, an earlier create may have committed, so the resubmit can
	// duplicate rows and is counted.
	ActionSubmit Action = "submit"
	// ActionRetry submits a batch again once NextRetryAt has passed.
	ActionRetry Action = "retry"
	// ActionPoll reads the status of a batch receipt. It never resubmits.
	ActionPoll    Action = "poll"
	ActionCleanup Action = "cleanup"
)

// SegmentAction is the restart action for one segment.
type SegmentAction struct {
	ID     string
	State  State
	Action Action
}

// BatchAction is the restart action for one batch.
type BatchAction struct {
	ID        string
	State     State
	Action    Action
	Ambiguous bool
	// Receipt is set for ActionPoll. Its Backend says which backend reads
	// it, which may be an older linked backend after a switch.
	Receipt     *Receipt
	NextRetryAt time.Time
}

// Plan is the result of recovery at startup.
type Plan struct {
	Segments []SegmentAction
	Batches  []BatchAction
	// PreparesReset counts segments found Preparing and sent back to
	// Discovered.
	PreparesReset int
	// BackendResets counts segments sent back to Discovered because their
	// transports or receipt belong to a backend that is no longer linked.
	BackendResets int
}

// RestartAction maps a segment state to what a restart does with it, for a
// segment that is not waiting on a batch.
func RestartAction(state State) Action {
	switch state {
	case StateDiscovered, StatePreparing:
		return ActionPrepare
	case StatePrepared:
		return ActionBatch
	case StateConfirmed, StateCleaning, StateEmpty:
		return ActionCleanup
	default:
		return ActionNone
	}
}
