/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package progress

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// JSONLRenderer streams events as JSON Lines.
//
// First line is always {"event":"schemaVersion","version":1}. Each subsequent
// event is encoded one per line. Field names follow §6.4.3's lowerCamelCase
// wire schema; Go field names in event.go remain Go-idiomatic.
//
// The schema-version line is emitted lazily on the first Emit call. If the
// orchestrator aborts before any Emit (e.g., construction succeeds but a
// pre-flight error stops the install), the stream is empty — consumers MUST
// tolerate empty streams. The schema-version line, when present, is always
// the first line.
//
// Wire schema changes require bumping version.
//
// When composeStatus is true (set via NewJSONLRendererForStatus), the renderer
// implements Plan Deviation #19: instead of emitting one line per status event,
// it buffers ComponentHealth/ClusterRow/RecentEvent events and composes them
// into a single fat snapshot object per §6.5.4 when the next Snapshot (or
// Final) arrives.
type JSONLRenderer struct {
	// mu serializes Emit calls so concurrent emitters (e.g. split-cluster
	// preflight running control-plane + compute-plane probes in parallel
	// via errgroup) can share a single sink without data-racing on the
	// underlying Writer + json.Encoder + composeStatus pending-buffers.
	mu            sync.Mutex
	w             io.Writer
	nowFunc       func() time.Time
	enc           *json.Encoder
	headerEmitted bool
	phasesDenom   int // M+11: phase denominator for totalPhases wire field; 0 → default 8

	// composeStatus (Plan Deviation #19): when true, buffer sub-snapshot events
	// and flush as a single composed snapshot on the next Snapshot/Final.
	composeStatus bool
	pendingSnap   *Snapshot
	pendingComps  []ComponentHealth
	pendingClusts []ClusterRow
	pendingEvents []RecentEvent
}

// newJSONEncoder creates a json.Encoder with HTML escaping disabled. Extracted
// so tests can point it at a local buffer without going through NewJSONLRenderer.
func newJSONEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

// NewJSONLRenderer constructs a JSONLRenderer writing to w.
func NewJSONLRenderer(w io.Writer) *JSONLRenderer {
	return &JSONLRenderer{
		w:       w,
		nowFunc: func() time.Time { return time.Now().UTC() },
		enc:     newJSONEncoder(w),
	}
}

// NewJSONLRendererWithTotalPhases constructs a JSONLRenderer with a custom
// phase denominator for the totalPhases wire field in phase_started events.
// Use for ModeDown (7) or uninstall (3/5). (M+11)
func NewJSONLRendererWithTotalPhases(w io.Writer, total int) *JSONLRenderer {
	return &JSONLRenderer{
		w:           w,
		nowFunc:     func() time.Time { return time.Now().UTC() },
		enc:         newJSONEncoder(w),
		phasesDenom: total,
	}
}

// NewJSONLRendererForStatus constructs a JSONLRenderer in status-composition
// mode (Plan Deviation #19). Instead of streaming one line per event, it
// buffers ComponentHealth/ClusterRow/RecentEvent events and emits a single
// fat snapshot object per §6.5.4 when each Snapshot event arrives.
func NewJSONLRendererForStatus(w io.Writer) *JSONLRenderer {
	return &JSONLRenderer{
		w:             w,
		nowFunc:       func() time.Time { return time.Now().UTC() },
		enc:           newJSONEncoder(w),
		composeStatus: true,
	}
}

// newJSONLRendererForTest is a test-only constructor that injects a clock.
func newJSONLRendererForTest(w io.Writer, now func() time.Time) *JSONLRenderer {
	return &JSONLRenderer{
		w:       w,
		nowFunc: now,
		enc:     newJSONEncoder(w),
	}
}

// newJSONLRendererWithTotalPhasesForTest is a test-only constructor with
// injected clock and custom phase denominator. (M+11)
func newJSONLRendererWithTotalPhasesForTest(w io.Writer, now func() time.Time, total int) *JSONLRenderer {
	return &JSONLRenderer{
		w:           w,
		nowFunc:     now,
		enc:         newJSONEncoder(w),
		phasesDenom: total,
	}
}

// newJSONLRendererForStatusTest is a test-only constructor for composed status
// mode with an injected clock.
func newJSONLRendererForStatusTest(w io.Writer, now func() time.Time) *JSONLRenderer {
	return &JSONLRenderer{
		w:             w,
		nowFunc:       now,
		enc:           newJSONEncoder(w),
		composeStatus: true,
	}
}

// Emit writes the schema-version header (once) then the event as a single JSON line.
// Unknown event types are silently ignored, matching PlainRenderer's behaviour.
//
// When composeStatus is true, Snapshot/ComponentHealth/ClusterRow/RecentEvent
// events are buffered and flushed as a single fat object on each Snapshot
// or Final. All other event types are passed through unchanged.
func (r *JSONLRenderer) Emit(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.headerEmitted {
		if err := r.enc.Encode(struct {
			Event   string `json:"event"`
			Version int    `json:"version"`
		}{"schemaVersion", 1}); err != nil {
			return err
		}
		r.headerEmitted = true
	}

	if r.composeStatus {
		return r.emitComposed(e)
	}

	tsStr := r.nowFunc().UTC().Format(time.RFC3339)
	wire := r.toWire(e, tsStr)
	if wire == nil {
		return nil
	}
	return r.enc.Encode(wire)
}

// emitComposed handles event buffering for composed-status mode (Plan
// Deviation #19). ComponentHealth/ClusterRow/RecentEvent are buffered until
// a Snapshot or Final event triggers a flush. The composed object follows
// §6.5.4's fat-snapshot schema.
func (r *JSONLRenderer) emitComposed(e Event) error {
	switch ev := e.(type) {
	case Snapshot:
		// Flush any previous pending snapshot before adopting the new one.
		if err := r.flushComposed(); err != nil {
			return err
		}
		r.pendingSnap = &ev
		r.pendingComps = nil
		r.pendingClusts = nil
		r.pendingEvents = nil
		return nil

	case ComponentHealth:
		r.pendingComps = append(r.pendingComps, ev)
		return nil

	case ClusterRow:
		r.pendingClusts = append(r.pendingClusts, ev)
		return nil

	case RecentEvent:
		r.pendingEvents = append(r.pendingEvents, ev)
		return nil

	case Final:
		// Flush pending snapshot then pass through Final.
		if err := r.flushComposed(); err != nil {
			return err
		}
		tsStr := r.nowFunc().UTC().Format(time.RFC3339)
		wire := r.toWire(e, tsStr)
		if wire == nil {
			return nil
		}
		return r.enc.Encode(wire)
	}

	// All other event types (phase events, check events, etc.) pass through.
	tsStr := r.nowFunc().UTC().Format(time.RFC3339)
	wire := r.toWire(e, tsStr)
	if wire == nil {
		return nil
	}
	return r.enc.Encode(wire)
}

// flushComposed emits a fat snapshot object if there is a pending Snapshot.
// Resets the pending buffer.
func (r *JSONLRenderer) flushComposed() error {
	if r.pendingSnap == nil {
		return nil
	}
	snap := r.pendingSnap
	tsStr := r.nowFunc().UTC().Format(time.RFC3339)

	composed := wireSnapshotComposed{
		Event:           "snapshot",
		TS:              tsStr,
		Cluster:         snap.Cluster,
		Verdict:         snap.Verdict,
		ReconcileAgeSec: snap.ReconcileAgeSec,
		Identity: wireSnapshotIdentity{
			ClusterID:    snap.Identity.ClusterID,
			Target:       snap.Identity.Target,
			StackVersion: snap.Identity.StackVersion,
			StackDigest:  snap.Identity.StackDigest,
		},
	}
	if !snap.Identity.InstalledAt.IsZero() {
		composed.Identity.InstalledAt = snap.Identity.InstalledAt.UTC().Format(time.RFC3339)
	}

	for _, c := range r.pendingComps {
		inline := wireComponentInline{
			Name:      c.Name,
			Cluster:   c.Cluster,
			Role:      c.Role,
			Ready:     c.Ready,
			Total:     c.Total,
			UptimeSec: c.UptimeSec,
			Healthy:   c.Healthy,
			Message:   c.Message,
		}
		composed.Components = append(composed.Components, inline)
	}

	for _, cl := range r.pendingClusts {
		composed.ComputeClusters = append(composed.ComputeClusters, wireClusterInline{
			Name:              cl.Name,
			Context:           cl.Context,
			GPU:               cl.GPU,
			GPUCount:          cl.GPUCount,
			ActiveDeployments: cl.ActiveDeployments,
			LastSeenAgeSec:    cl.LastSeenAgeSec,
			Healthy:           cl.Healthy,
			IsCurrent:         cl.IsCurrent,
		})
	}

	for _, ev := range r.pendingEvents {
		composed.Events = append(composed.Events, wireEventInline{
			AgeSec:  ev.AgeSec,
			Kind:    ev.Kind,
			Status:  ev.Status,
			Name:    ev.Name,
			Version: ev.Version,
		})
	}

	r.pendingSnap = nil
	r.pendingComps = nil
	r.pendingClusts = nil
	r.pendingEvents = nil

	return r.enc.Encode(composed)
}

// Close is a no-op; lines are flushed eagerly via json.Encoder.
func (*JSONLRenderer) Close() error { return nil }

// effectiveTotalPhases returns the phase denominator for the totalPhases wire field.
func (r *JSONLRenderer) effectiveTotalPhases() int {
	if r.phasesDenom > 0 {
		return r.phasesDenom
	}
	return totalPhases
}

// toWire converts a typed Event to a wire-schema struct. Returns nil for
// unknown event types. The wire structs carry JSON field tags that follow
// §6.4.3's lowerCamelCase names; event.go's Go field names are intentionally
// independent so each can evolve without affecting the other.
func (r *JSONLRenderer) toWire(e Event, ts string) any {
	switch ev := e.(type) {
	case PhaseStarted:
		out := wirePhaseStarted{
			Event:       "phase_started",
			TS:          ts,
			PhaseNum:    ev.Num,
			Phase:       ev.Name,
			TotalPhases: r.effectiveTotalPhases(),
			Context:     ev.Context,
		}
		if !ev.StartedAt.IsZero() {
			out.StartedAt = ev.StartedAt.UTC().Format(time.RFC3339)
		}
		return out
	case PhaseProgress:
		return wirePhaseProgress{
			Event:    "phase_progress",
			TS:       ts,
			PhaseNum: ev.Num,
			Phase:    ev.Name,
			Resource: ev.Resource,
			Done:     ev.Done,
			Total:    ev.Total,
			Context:  ev.Context,
		}
	case PhaseCompleted:
		return wirePhaseCompleted{
			Event:       "phase_completed",
			TS:          ts,
			PhaseNum:    ev.Num,
			Phase:       ev.Name,
			DurationSec: int(ev.Duration.Seconds()),
			Context:     ev.Context,
		}
	case PhaseFailed:
		out := wirePhaseFailed{
			Event:       "phase_failed",
			TS:          ts,
			PhaseNum:    ev.Num,
			Phase:       ev.Name,
			DurationSec: int(ev.Duration.Seconds()),
			Context:     ev.Context,
			ErrCategory: ev.ErrCategory,
			ErrMessage:  ev.ErrMessage,
			RetryClass:  ev.RetryClass,
		}
		if ev.RetryClass == "backoff" && ev.RetryAfterSec > 0 {
			out.RetryAfterSec = ev.RetryAfterSec
		}
		if len(ev.Remediation) > 0 {
			out.Remediation = ev.Remediation
		}
		// All RawFailure fields are comparable (string + int); adding a non-comparable
		// field (slice/map) would break this equality and must update the guard below.
		if ev.Raw != (RawFailure{}) {
			out.Raw = &wireRaw{
				Subprocess:       ev.Raw.Subprocess,
				ExitCode:         ev.Raw.ExitCode,
				StderrTail:       ev.Raw.StderrTail,
				HTTPStatus:       ev.Raw.HTTPStatus,
				KubernetesReason: ev.Raw.KubernetesReason,
			}
		}
		return out
	case PhaseCancelled:
		return wirePhaseCancelled{
			Event:    "phase_cancelled",
			TS:       ts,
			PhaseNum: ev.Num,
			Phase:    ev.Name,
			Reason:   ev.Reason,
			Context:  ev.Context,
		}
	case Waiting:
		return wireWaiting{
			Event:    "waiting",
			TS:       ts,
			PhaseNum: ev.Num,
			Reason:   ev.Reason,
			Context:  ev.Context,
		}
	case LastProgress:
		out := wireLastProgress{
			Event:    "last_progress",
			TS:       ts,
			PhaseNum: ev.Num,
			Detail:   ev.Detail,
			Context:  ev.Context,
		}
		if !ev.At.IsZero() {
			out.At = ev.At.UTC().Format(time.RFC3339)
		}
		return out
	case Planned:
		out := wirePlanned{
			Event:       "planned",
			TS:          ts,
			Cluster:     ev.Cluster,
			Target:      ev.Target,
			Stack:       ev.Stack,
			TotalETASec: ev.TotalETASec,
		}
		for _, p := range ev.Phases {
			out.Phases = append(out.Phases, wirePlannedPhase{
				PhaseNum: p.Num, Phase: p.Name, ETASec: p.ETASec,
			})
		}
		for _, rel := range ev.WillUninstall {
			out.WillUninstall = append(out.WillUninstall, wireRelease{
				Kind:      rel.Kind,
				Name:      rel.Name,
				Namespace: rel.Namespace,
				Command:   rel.Command,
			})
		}
		return out
	case DrainProgress:
		return wireDrainProgress{
			Event:      "drain_progress",
			TS:         ts,
			PhaseNum:   ev.Num,
			Deployment: ev.Deployment,
			State:      ev.State,
		}
	case LogLine:
		return wireLogLine{
			Event:  "log_line",
			TS:     ts,
			Stream: ev.Stream,
			Source: ev.Source,
			Line:   ev.Line,
		}
	case Final:
		out := wireFinal{
			Event:                 "final",
			TS:                    ts,
			Success:               ev.Success,
			DurationSec:           int(ev.Duration.Seconds()),
			DroppedProgressEvents: ev.DroppedProgressEvents,
		}
		if ev.PlanOnly {
			out.PlanOnly = true
		}
		if ev.Cancelled {
			out.Cancelled = true
		}
		if ev.ClusterID != "" {
			out.ClusterID = ev.ClusterID
		}
		if ev.ClusterGroupID != "" {
			out.ClusterGroupID = ev.ClusterGroupID
		}
		if ev.NVCFBackendHealth != "" {
			out.NVCFBackendHealth = ev.NVCFBackendHealth
		}
		if ev.Verdict != "" {
			out.Verdict = ev.Verdict
			out.TotalChecks = ev.TotalChecks
			passed := ev.PassedCount
			failed := ev.FailedCount
			out.PassedCount = &passed
			out.FailedCount = &failed
		}
		return out
	case Snapshot:
		out := wireSnapshot{
			Event:           "snapshot",
			TS:              ts,
			Cluster:         ev.Cluster,
			Verdict:         ev.Verdict,
			ReconcileAgeSec: ev.ReconcileAgeSec,
			Identity: wireSnapshotIdentity{
				ClusterID:    ev.Identity.ClusterID,
				Target:       ev.Identity.Target,
				StackVersion: ev.Identity.StackVersion,
				StackDigest:  ev.Identity.StackDigest,
			},
		}
		if !ev.Identity.InstalledAt.IsZero() {
			out.Identity.InstalledAt = ev.Identity.InstalledAt.UTC().Format(time.RFC3339)
		}
		return out
	case ComponentHealth:
		return wireComponentHealth{
			Event:     "component_health",
			TS:        ts,
			Name:      ev.Name,
			Cluster:   ev.Cluster,
			Role:      ev.Role,
			Ready:     ev.Ready,
			Total:     ev.Total,
			UptimeSec: ev.UptimeSec,
			Healthy:   ev.Healthy,
			Message:   ev.Message,
		}
	case ClusterRow:
		return wireClusterRow{
			Event:             "cluster_row",
			TS:                ts,
			Name:              ev.Name,
			Context:           ev.Context,
			GPU:               ev.GPU,
			GPUCount:          ev.GPUCount,
			ActiveDeployments: ev.ActiveDeployments,
			LastSeenAgeSec:    ev.LastSeenAgeSec,
			Healthy:           ev.Healthy,
			IsCurrent:         ev.IsCurrent,
		}
	case RecentEvent:
		return wireRecentEvent{
			Event:   "recent_event",
			TS:      ts,
			AgeSec:  ev.AgeSec,
			Kind:    ev.Kind,
			Status:  ev.Status,
			Name:    ev.Name,
			Version: ev.Version,
		}
	case CheckStarted:
		return wireCheckStarted{
			Event:    "check_started",
			TS:       ts,
			Category: ev.Category,
			ID:       ev.ID,
			Message:  ev.Message,
		}
	case CheckCompleted:
		return wireCheckCompleted{
			Event:    "check_completed",
			TS:       ts,
			Category: ev.Category,
			ID:       ev.ID,
			Passed:   ev.Passed,
			Severity: ev.Severity,
			Message:  ev.Message,
			Detail:   ev.Detail,
			HintURL:  ev.HintURL,
		}
	case CategoryCompleted:
		return wireCategoryCompleted{
			Event:       "category_completed",
			TS:          ts,
			Category:    ev.Category,
			PassedCount: ev.PassedCount,
			FailedCount: ev.FailedCount,
			DurationSec: ev.DurationSec,
		}
	}
	return nil
}

// Wire structs — JSON field tags live here, NOT on event.go's Event types.
// This decouples §6.4.3's wire schema from Go-idiomatic field names so each
// can evolve independently. A rename or restructure on the wire side requires
// a version bump (version: 2 in the header line); Go-side renames do not.

type wirePhaseStarted struct {
	Event       string `json:"event"`
	TS          string `json:"ts"`
	PhaseNum    int    `json:"phaseNum"`
	Phase       string `json:"phase"`
	TotalPhases int    `json:"totalPhases"`
	Context     string `json:"context,omitempty"` // M+9: omitted in single-cluster mode
	StartedAt   string `json:"startedAt,omitempty"`
}

type wirePhaseProgress struct {
	Event    string `json:"event"`
	TS       string `json:"ts"`
	PhaseNum int    `json:"phaseNum"`
	Phase    string `json:"phase"`
	Resource string `json:"resource"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Context  string `json:"context,omitempty"` // M+9: omitted in single-cluster mode
}

type wirePhaseCompleted struct {
	Event       string `json:"event"`
	TS          string `json:"ts"`
	PhaseNum    int    `json:"phaseNum"`
	Phase       string `json:"phase"`
	DurationSec int    `json:"durationSec"`
	Context     string `json:"context,omitempty"` // M+9: omitted in single-cluster mode
}

type wirePhaseFailed struct {
	Event         string   `json:"event"`
	TS            string   `json:"ts"`
	PhaseNum      int      `json:"phaseNum"`
	Phase         string   `json:"phase"`
	DurationSec   int      `json:"durationSec"`
	Context       string   `json:"context,omitempty"` // M+9: omitted in single-cluster mode
	ErrCategory   string   `json:"errCategory"`
	ErrMessage    string   `json:"errMessage"`
	RetryClass    string   `json:"retryClass"`
	RetryAfterSec int      `json:"retryAfterSec,omitempty"`
	Remediation   []string `json:"remediation,omitempty"`
	Raw           *wireRaw `json:"raw,omitempty"`
}

type wireRaw struct {
	Subprocess       string `json:"subprocess,omitempty"`
	ExitCode         int    `json:"exitCode,omitempty"`
	StderrTail       string `json:"stderrTail,omitempty"`
	HTTPStatus       int    `json:"httpStatus,omitempty"`
	KubernetesReason string `json:"kubernetesReason,omitempty"`
}

type wirePhaseCancelled struct {
	Event    string `json:"event"`
	TS       string `json:"ts"`
	PhaseNum int    `json:"phaseNum"`
	Phase    string `json:"phase"`
	Reason   string `json:"reason"`
	Context  string `json:"context,omitempty"` // M+9: omitted in single-cluster mode
}

type wireWaiting struct {
	Event    string `json:"event"`
	TS       string `json:"ts"`
	PhaseNum int    `json:"phaseNum"`
	Reason   string `json:"reason"`
	Context  string `json:"context,omitempty"` // M+9: omitted in single-cluster mode
}

type wireLastProgress struct {
	Event    string `json:"event"`
	TS       string `json:"ts"`
	PhaseNum int    `json:"phaseNum"`
	Detail   string `json:"detail"`
	At       string `json:"at,omitempty"`
	Context  string `json:"context,omitempty"` // M+9: omitted in single-cluster mode
}

// wireFinal encodes the final event.
//
// Omitempty notes:
//   - Success: NO omitempty — false must appear on the wire (consumers check it).
//   - Cancelled: omitempty — omit when false; include when true (JS-boolean signal).
//   - PlanOnly: omitempty — omit when false; include when true (plan-only short-circuit).
//   - DurationSec: NO omitempty — 0 is a valid value for very fast installs.
//   - DroppedProgressEvents: NO omitempty — consumers need the backpressure count.
//   - Verdict/TotalChecks/PassedCount/FailedCount: omitempty — check-mode only (M+8.11).
type wireFinal struct {
	Event                 string `json:"event"`
	TS                    string `json:"ts"`
	Success               bool   `json:"success"`
	Cancelled             bool   `json:"cancelled,omitempty"`
	PlanOnly              bool   `json:"planOnly,omitempty"`
	ClusterID             string `json:"clusterId,omitempty"`
	ClusterGroupID        string `json:"clusterGroupId,omitempty"`
	NVCFBackendHealth     string `json:"nvcfBackendHealth,omitempty"`
	DurationSec           int    `json:"durationSec"`
	DroppedProgressEvents int    `json:"droppedProgressEvents"`
	Verdict               string `json:"verdict,omitempty"`
	TotalChecks           int    `json:"totalChecks,omitempty"`
	// PassedCount/FailedCount are *int so a check-final with all-fail
	// (PassedCount=0) still emits `"passedCount":0` per §6.6.3 / M+8.11.
	// Plain `int` + omitempty would silently drop the zero, breaking
	// downstream tooling that asserts the field is always present on a
	// check-typed Final. Nil on non-check Final keeps install/up/down
	// goldens unchanged.
	PassedCount *int `json:"passedCount,omitempty"`
	FailedCount *int `json:"failedCount,omitempty"`
}

// Wire structs for status + check event types (M+8.3).

type wireSnapshot struct {
	Event           string               `json:"event"`
	TS              string               `json:"ts"`
	Cluster         string               `json:"cluster"`
	Verdict         string               `json:"verdict"`
	ReconcileAgeSec int                  `json:"reconcileAgeSec"`
	Identity        wireSnapshotIdentity `json:"identity"`
}

type wireSnapshotIdentity struct {
	ClusterID    string `json:"clusterId"`
	Target       string `json:"target"`
	StackVersion string `json:"stackVersion"`
	StackDigest  string `json:"stackDigest"`
	InstalledAt  string `json:"installedAt,omitempty"`
}

type wireComponentHealth struct {
	Event     string `json:"event"`
	TS        string `json:"ts"`
	Name      string `json:"name"`
	Cluster   string `json:"cluster,omitempty"`
	Role      string `json:"role,omitempty"` // M+9: omitted in single-cluster mode
	Ready     int    `json:"ready"`
	Total     int    `json:"total"`
	UptimeSec int    `json:"uptimeSec"`
	Healthy   bool   `json:"healthy"`
	Message   string `json:"message,omitempty"`
}

type wireClusterRow struct {
	Event             string `json:"event"`
	TS                string `json:"ts"`
	Name              string `json:"name"`
	Context           string `json:"context,omitempty"`   // M+9: omitted when unknown
	GPU               string `json:"gpu"`
	GPUCount          int    `json:"gpuCount"`
	ActiveDeployments int    `json:"activeDeployments"`
	LastSeenAgeSec    int    `json:"lastSeenAgeSec"`
	Healthy           bool   `json:"healthy"`
	IsCurrent         bool   `json:"isCurrent,omitempty"` // M+9: omitted when false
}

type wireRecentEvent struct {
	Event   string `json:"event"`
	TS      string `json:"ts"`
	AgeSec  int    `json:"ageSec"`
	Kind    string `json:"kind"`
	Status  string `json:"status,omitempty"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type wireCheckStarted struct {
	Event    string `json:"event"`
	TS       string `json:"ts"`
	Category string `json:"category"`
	ID       string `json:"id"`
	Message  string `json:"message,omitempty"`
}

type wireCheckCompleted struct {
	Event    string `json:"event"`
	TS       string `json:"ts"`
	Category string `json:"category"`
	ID       string `json:"id"`
	Passed   bool   `json:"passed"`
	Severity string `json:"severity"`
	Message  string `json:"message,omitempty"`
	Detail   string `json:"detail,omitempty"`
	HintURL  string `json:"hintURL,omitempty"`
}

type wireCategoryCompleted struct {
	Event       string  `json:"event"`
	TS          string  `json:"ts"`
	Category    string  `json:"category"`
	PassedCount int     `json:"passedCount"`
	FailedCount int     `json:"failedCount"`
	DurationSec float64 `json:"durationSec"`
}

// Wire structs for composed status snapshot (Plan Deviation #19 / §6.5.4).
// These "Inline" variants drop the per-event "event" and "ts" fields since
// those are redundant inside the parent composed object.

// wireSnapshotComposed is the fat snapshot object emitted in status-compose
// mode. It carries the Snapshot identity plus all ComponentHealth, ClusterRow,
// and RecentEvent sub-objects inlined.
type wireSnapshotComposed struct {
	Event           string               `json:"event"`
	TS              string               `json:"ts"`
	Cluster         string               `json:"cluster"`
	Verdict         string               `json:"verdict"`
	ReconcileAgeSec int                  `json:"reconcileAgeSec"`
	Identity        wireSnapshotIdentity `json:"identity"`
	Components      []wireComponentInline `json:"components,omitempty"`
	ComputeClusters []wireClusterInline   `json:"computeClusters,omitempty"`
	Events          []wireEventInline     `json:"events,omitempty"`
}

// wireComponentInline is a ComponentHealth without event/ts fields.
type wireComponentInline struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster,omitempty"`
	Role      string `json:"role,omitempty"`    // M+9: omitted in single-cluster mode
	Ready     int    `json:"ready"`
	Total     int    `json:"total"`
	UptimeSec int    `json:"uptimeSec"`
	Healthy   bool   `json:"healthy"`
	Message   string `json:"message,omitempty"`
}

// wireClusterInline is a ClusterRow without event/ts fields.
type wireClusterInline struct {
	Name              string `json:"name"`
	Context           string `json:"context,omitempty"`   // M+9: omitted when unknown
	GPU               string `json:"gpu"`
	GPUCount          int    `json:"gpuCount"`
	ActiveDeployments int    `json:"activeDeployments"`
	LastSeenAgeSec    int    `json:"lastSeenAgeSec"`
	Healthy           bool   `json:"healthy"`
	IsCurrent         bool   `json:"isCurrent,omitempty"` // M+9: omitted when false
}

// wireEventInline is a RecentEvent without event/ts fields.
type wireEventInline struct {
	AgeSec  int    `json:"ageSec"`
	Kind    string `json:"kind"`
	Status  string `json:"status,omitempty"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Wire structs for the planned (--plan-only) event (M+8.8).

// wirePlanned encodes the planned event emitted by --plan-only.
type wirePlanned struct {
	Event         string             `json:"event"`
	TS            string             `json:"ts"`
	Cluster       string             `json:"cluster"`
	Target        string             `json:"target"`
	Stack         string             `json:"stack"`
	TotalETASec   int                `json:"totalETASec"`
	Phases        []wirePlannedPhase `json:"phases"`
	WillUninstall []wireRelease      `json:"willUninstall,omitempty"` // M+11: populated for down --plan-only
}

// wirePlannedPhase encodes a single phase entry in the planned event.
type wirePlannedPhase struct {
	PhaseNum int    `json:"phaseNum"`
	Phase    string `json:"phase"`
	ETASec   int    `json:"etaSec"`
}

// wireRelease encodes a single release entry in wirePlanned.WillUninstall. (M+11)
type wireRelease struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Command   string `json:"command"`
}

// wireDrainProgress encodes a drain_progress event emitted during `down`. (M+11)
type wireDrainProgress struct {
	Event      string `json:"event"`
	TS         string `json:"ts"`
	PhaseNum   int    `json:"phaseNum"`
	Deployment string `json:"deployment"`
	State      string `json:"state"` // "REMOVING" | "STOPPED" | "ERROR"
}

// wireLogLine encodes a single line of subprocess output (helmfile, kubectl,
// etc.) plumbed through the event sink. JSONL consumers can drop these lines
// to keep their stream clean, or display them inline. The TTY renderer keeps
// a small ring buffer (last 8 lines) and shows them as a "Recent" panel.
type wireLogLine struct {
	Event  string `json:"event"`
	TS     string `json:"ts"`
	Stream string `json:"stream"` // "stdout" | "stderr"
	Source string `json:"source"` // short producer tag, e.g. "helmfile-cp"
	Line   string `json:"line"`
}
