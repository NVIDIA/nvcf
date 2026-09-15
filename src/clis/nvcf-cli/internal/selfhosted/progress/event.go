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

// Package progress implements the orchestrator-to-renderer event stream
// described in SRD/SDD §6.4.4. Events are emitted by `nvcf self-hosted up`
// during the install lifecycle and consumed by one of three renderers
// (bubbletea TTY, plain streaming, JSONL).
package progress

import "time"

// Event is the sealed interface every progress event implements. The
// `kind()` method returns a stable string used in --json mode and by the
// renderer switch. Sealed via a package-private return type so external
// packages can't pretend to be Events.
type Event interface {
	kind() eventKind
}

type eventKind string

type PhaseStarted struct {
	Num       int
	Name      string
	StartedAt time.Time
	Context   string // M+9: kubeconfig context this phase targets ("" in single-cluster mode)
}

func (PhaseStarted) kind() eventKind { return "phase_started" }

type PhaseProgress struct {
	Num      int
	Name     string
	Resource string // "namespaces" | "crds" | "deployments" | "statefulsets" | "jobs"
	Done     int
	Total    int
	Context  string // M+9: kubeconfig context this phase targets ("" in single-cluster mode)
}

func (PhaseProgress) kind() eventKind { return "phase_progress" }

type PhaseCompleted struct {
	Num      int
	Name     string
	Duration time.Duration
	Context  string // M+9: kubeconfig context this phase targets ("" in single-cluster mode)
}

func (PhaseCompleted) kind() eventKind { return "phase_completed" }

// PhaseFailed carries a structured failure (REQ-15). Renderers MUST surface
// ErrCategory + ErrMessage + Remediation; agents key off ErrCategory + RetryClass.
type PhaseFailed struct {
	Num      int
	Name     string
	Duration time.Duration
	Context  string // M+9: kubeconfig context this phase targets ("" in single-cluster mode)

	// Stable enum: auth | network | dns | token_expiry | cluster_state |
	// helm_render | helm_apply | helm_pending_upgrade | register |
	// partial_sis_write | compute_plane | cache_corruption |
	// cassandra_migration_lock | internal | unknown.
	ErrCategory string
	ErrMessage  string

	// Retry classification (REQ-15):
	//   none              — non-retryable; remediation REQUIRED before re-run
	//   immediate         — retry now (transient blip)
	//   backoff           — retry after RetryAfterSec
	//   after_remediation — retry only after a remediation step has been taken
	//   unknown           — classifier couldn't decide; treat conservatively
	RetryClass    string
	RetryAfterSec int

	Remediation []string
	Raw         RawFailure
}

func (PhaseFailed) kind() eventKind { return "phase_failed" }

// PhaseCancelled is emitted when SIGINT/SIGTERM aborts the orchestrator
// mid-phase (REQ-16). Renderers MUST treat this as terminal.
type PhaseCancelled struct {
	Num     int
	Name    string
	Reason  string
	Context string // M+9: kubeconfig context this phase targets ("" in single-cluster mode)
}

func (PhaseCancelled) kind() eventKind { return "phase_cancelled" }

// RawFailure preserves the underlying source signal for a phase_failed event.
// Optional; fill whichever fields are knowable at the failure site.
type RawFailure struct {
	Subprocess       string
	ExitCode         int
	StderrTail       string
	HTTPStatus       int
	KubernetesReason string
}

type Waiting struct {
	Num     int
	Reason  string
	Context string // M+9: kubeconfig context this phase targets ("" in single-cluster mode)
}

func (Waiting) kind() eventKind { return "waiting" }

type LastProgress struct {
	Num     int
	Detail  string
	At      time.Time
	Context string // M+9: kubeconfig context this phase targets ("" in single-cluster mode)
}

func (LastProgress) kind() eventKind { return "last_progress" }

// Planned summarizes a --plan-only dry-run: the phase sequence the orchestrator
// would execute, with P50 ETAs from the embedded telemetry table. Renderers
// surface this so agents can preview impact before committing to a real install.
type Planned struct {
	Cluster string
	Target  string
	Stack   string

	// Phases lists the planned phases in execution order; ETASec uses the
	// P50 historical mean from etas.go.
	Phases []PlannedPhase

	// TotalETASec is the sum of Phases[].ETASec for convenience. Renderers
	// SHOULD show this prominently (it's the headline number for "how long
	// will this take").
	TotalETASec int

	// WillUninstall is populated when --plan-only is used with the down
	// direction. Each entry describes one Helm release (or SIS/kubectl
	// resource) that would be removed. Empty for install plan-only. (M+11)
	WillUninstall []ReleaseDescriptor
}

// ReleaseDescriptor describes a single resource that will be removed during
// `down`. Used in Planned.WillUninstall for --plan-only dry-run output.
type ReleaseDescriptor struct {
	Kind      string // "helm" | "sis" | "kubectl"
	Name      string
	Namespace string
	Command   string // literal command that down would run, e.g. "helm uninstall foo -n bar"
}

func (Planned) kind() eventKind { return "planned" }

// PlannedPhase describes a single phase in the --plan-only output.
type PlannedPhase struct {
	Num    int
	Name   string
	ETASec int
}

// Final summarizes the orchestrator outcome and surfaces backpressure telemetry.
type Final struct {
	Success               bool
	Cancelled             bool // true if reached via SIGINT/SIGTERM
	PlanOnly              bool // true when reached via --plan-only short-circuit
	ClusterID             string
	ClusterGroupID        string
	NVCFBackendHealth     string
	Duration              time.Duration
	DroppedProgressEvents int // count of phase_progress events dropped under load

	// Check-mode terminal fields (M+8.11, REQ-19): set by `nvcf self-hosted check`
	// to surface the verdict + tally. Zero values for other modes (omit on wire).
	Verdict     string // "ok" | "warnings" | "failed" | "error"
	TotalChecks int
	PassedCount int
	FailedCount int
}

func (Final) kind() eventKind { return "final" }

// Snapshot is the single steady-state cluster-health event emitted by
// `nvcf self-hosted status`. One Snapshot per --watch interval (or one shot
// without --watch). Carries identity + verdict; ComponentHealth/ClusterRow/
// RecentEvent events follow as a logical "snapshot frame".
type Snapshot struct {
	Cluster         string
	Verdict         string // "healthy" | "degraded" | "failed" | "unknown"
	ReconcileAgeSec int
	Identity        SnapshotIdentity
}

func (Snapshot) kind() eventKind { return "snapshot" }

type SnapshotIdentity struct {
	ClusterID    string
	Target       string
	StackVersion string
	StackDigest  string
	InstalledAt  time.Time
}

// ComponentHealth is one row of the components table in the status snapshot.
// Cluster is set only for compute-plane components like nvca-worker; empty
// for control-plane components.
type ComponentHealth struct {
	Name      string
	Cluster   string // optional: compute-plane component cluster context
	Role      string // M+9: "control-plane" | "compute-plane" | "" (single-cluster mode)
	Ready     int
	Total     int
	UptimeSec int
	Healthy   bool
	Message   string // optional: short reason when !Healthy ("PodInitializing 6m23s")
}

func (ComponentHealth) kind() eventKind { return "component_health" }

// ClusterRow is one row of the compute-clusters table.
type ClusterRow struct {
	Name              string
	Context           string // M+9: kubeconfig context this cluster's compute plane is on
	GPU               string
	GPUCount          int
	ActiveDeployments int
	LastSeenAgeSec    int
	Healthy           bool
	IsCurrent         bool // M+9: true when this is the cluster status is currently inspecting
}

func (ClusterRow) kind() eventKind { return "cluster_row" }

// RecentEvent is one row of the recent-events table at the bottom of the
// status snapshot. Kind is one of: "function-deploy", "cluster-registered",
// "cluster-deregistered", "node-pressure", or other free-form values from
// kubectl events / NVCFBackend transitions.
type RecentEvent struct {
	AgeSec  int
	Kind    string
	Status  string // optional: ACTIVE/DELETED/etc for function-deploy events
	Name    string // resource name
	Version string // optional: function version etc.
}

func (RecentEvent) kind() eventKind { return "recent_event" }

// CheckStarted marks the start of a single pre-flight check.
// Message is the human-readable label shown in TTY in-flight rows (e.g.
// "querying API version…"). ID remains the stable identity used for
// deduplication; Message is purely for display.
type CheckStarted struct {
	Category string // e.g. "local-host-tools", "pre-kubernetes-setup"
	ID       string // e.g. "kubectl-on-path"
	Message  string // optional: human-readable in-flight label (e.g. "querying API version…")
}

func (CheckStarted) kind() eventKind { return "check_started" }

// CheckCompleted carries the result of a single check. Severity is one of
// "info" | "warning" | "error". HintURL points to remediation docs when set.
type CheckCompleted struct {
	Category string
	ID       string
	Passed   bool
	Severity string
	Message  string
	Detail   string // optional: short version string or extra context
	HintURL  string // optional: remediation-docs link
}

func (CheckCompleted) kind() eventKind { return "check_completed" }

// CategoryCompleted summarizes one category of checks. DurationSec is a
// float64 (matches §6.6.3 schema; check categories often complete sub-second).
type CategoryCompleted struct {
	Category    string
	PassedCount int
	FailedCount int
	DurationSec float64
}

func (CategoryCompleted) kind() eventKind { return "category_completed" }

// DrainProgress is emitted during the drain-active phase of `nvcf self-hosted
// down`. One event is emitted per deployment state transition (REMOVING →
// STOPPED | ERROR). (M+11)
type DrainProgress struct {
	Num        int
	Deployment string // function deployment name being drained
	State      string // "REMOVING" | "STOPPED" | "ERROR"
}

func (DrainProgress) kind() eventKind { return "drain_progress" }

// LogLine is a single line of subprocess output (helmfile, kubectl, etc.)
// surfaced through the EventSink so the TTY renderer can show a "Recent"
// tail panel. The TTY renderer keeps a small ring buffer (last N lines)
// and refreshes the panel on every Update. Plain renderer prints the line
// as-is. JSONL renderer wire-emits as `{"event":"log_line","ts":...,
// "stream":"stdout|stderr","line":...}` so consumers can choose to display
// or drop the noisy subprocess chatter.
//
// Stream is "stdout" or "stderr" — useful for consumers that want to color
// or route differently. Source is a short tag identifying which subprocess
// produced the line ("helmfile-cp", "helmfile-cwk", etc.) so a future
// multi-subprocess phase can keep the lines distinguishable.
type LogLine struct {
	Stream string // "stdout" | "stderr"
	Source string // short producer tag, e.g. "helmfile-cp"
	Line   string // raw line, no trailing newline
}

func (LogLine) kind() eventKind { return "log_line" }
