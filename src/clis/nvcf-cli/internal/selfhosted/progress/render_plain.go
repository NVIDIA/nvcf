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
	"fmt"
	"io"
	"time"
)

const totalPhases = 8

// PlainRenderer streams one RFC3339-prefixed line per event to w.
// It implements EventSink. Suitable for CI logs / piped output where
// ANSI escapes from the bubbletea renderer would be noise.
//
// Each emitted event ends with a newline; the renderer writes lines
// eagerly with no buffering. A truncated stream (e.g. from a closed
// pipe) is recoverable up to the last \n.
type PlainRenderer struct {
	w           io.Writer
	nowFunc     func() time.Time
	phasesDenom int // M+11: phase denominator for [NN/M] prefix; 0 → default 8
}

// NewPlainRenderer constructs a PlainRenderer writing to w.
func NewPlainRenderer(w io.Writer) *PlainRenderer {
	return &PlainRenderer{
		w:       w,
		nowFunc: func() time.Time { return time.Now().UTC() },
	}
}

// NewPlainRendererWithTotalPhases constructs a PlainRenderer with a custom
// phase denominator. Use for ModeDown (7) or uninstall (3/5). (M+11)
func NewPlainRendererWithTotalPhases(w io.Writer, total int) *PlainRenderer {
	return &PlainRenderer{
		w:           w,
		nowFunc:     func() time.Time { return time.Now().UTC() },
		phasesDenom: total,
	}
}

// newPlainRendererForTest is a test-only constructor that injects a clock.
func newPlainRendererForTest(w io.Writer, now func() time.Time) *PlainRenderer {
	return &PlainRenderer{w: w, nowFunc: now}
}

// newPlainRendererWithTotalPhasesForTest is a test-only constructor with
// injected clock and custom phase denominator. (M+11)
func newPlainRendererWithTotalPhasesForTest(w io.Writer, now func() time.Time, total int) *PlainRenderer {
	return &PlainRenderer{w: w, nowFunc: now, phasesDenom: total}
}

// effectiveTotalPhases returns the phase denominator used in [NN/M] prefixes.
func (r *PlainRenderer) effectiveTotalPhases() int {
	if r.phasesDenom > 0 {
		return r.phasesDenom
	}
	return totalPhases
}

// phasePrefix returns "<ts> [NN/M]" or "<ts> [NN/M] ctx=<ctx>" depending on
// whether a kubeconfig context is set. The ctx=… segment is appended in
// split-cluster mode (M+9) so operators can grep ctx=admin@gpu1 to isolate
// one side of a split deploy. Single-cluster mode (ctx=="") emits the shorter
// form to keep existing output byte-identical. M is the effective total phases
// (8 for install, 7 for down, 3-5 for uninstall). (M+11)
func (r *PlainRenderer) phasePrefix(ts string, num int, ctx string) string {
	n := r.effectiveTotalPhases()
	if ctx == "" {
		return fmt.Sprintf("%s [%02d/%d]", ts, num, n)
	}
	return fmt.Sprintf("%s [%02d/%d] ctx=%s", ts, num, n, ctx)
}

// Emit writes one or more lines to the underlying writer.
// Multi-line outputs (e.g. PhaseFailed remediation) are emitted as multiple
// lines, each prefixed with the same timestamp + phase tag.
func (r *PlainRenderer) Emit(_ context.Context, e Event) error {
	ts := r.nowFunc().UTC().Format(time.RFC3339)
	switch ev := e.(type) {
	case PhaseStarted:
		return r.writef("%s %s: starting\n", r.phasePrefix(ts, ev.Num, ev.Context), ev.Name)
	case PhaseProgress:
		return r.writef("%s %s: %s %d/%d\n", r.phasePrefix(ts, ev.Num, ev.Context), ev.Name, ev.Resource, ev.Done, ev.Total)
	case PhaseCompleted:
		return r.writef("%s %s: complete (%s)\n", r.phasePrefix(ts, ev.Num, ev.Context), ev.Name, ev.Duration)
	case PhaseFailed:
		// Build the retry/category suffix. When RetryClass is "backoff", include
		// the retry=Ns qualifier so operators know when to re-run. Other retry
		// classes (none, immediate, after_remediation, unknown) only show
		// [errCategory retryClass] without a time qualifier.
		suffix := fmt.Sprintf("[%s %s]", ev.ErrCategory, ev.RetryClass)
		if ev.RetryClass == "backoff" && ev.RetryAfterSec > 0 {
			suffix = fmt.Sprintf("[%s backoff retry=%ds]", ev.ErrCategory, ev.RetryAfterSec)
		}
		prefix := r.phasePrefix(ts, ev.Num, ev.Context)
		if err := r.writef("%s %s: failed %s %s\n", prefix, ev.Name, suffix, ev.ErrMessage); err != nil {
			return err
		}
		for _, line := range ev.Remediation {
			if err := r.writef("%s %s: → %s\n", prefix, ev.Name, line); err != nil {
				return err
			}
		}
		return nil
	case PhaseCancelled:
		return r.writef("%s %s: cancelled (%s)\n", r.phasePrefix(ts, ev.Num, ev.Context), ev.Name, ev.Reason)
	case Waiting:
		// Waiting and LastProgress carry no Name field. We use "(current)" as the
		// phase label so the line still grep-aligns with [NN/8] but doesn't
		// fabricate a phase name. Option (b) — threading the running phase name
		// through renderer state — was considered but deferred: it would couple
		// the renderer to orchestrator sequencing assumptions and complicate
		// out-of-order or replayed event streams.
		return r.writef("%s (current): waiting on %s\n", r.phasePrefix(ts, ev.Num, ev.Context), ev.Reason)
	case LastProgress:
		return r.writef("%s (current): %s\n", r.phasePrefix(ts, ev.Num, ev.Context), ev.Detail)
	case Planned:
		if err := r.writef("%s plan: cluster=%s target=%s stack=%s totalETA=%ds\n",
			ts, ev.Cluster, ev.Target, ev.Stack, ev.TotalETASec); err != nil {
			return err
		}
		n := r.effectiveTotalPhases()
		for _, p := range ev.Phases {
			if err := r.writef("%s plan: [%02d/%d] %s eta=%ds\n",
				ts, p.Num, n, p.Name, p.ETASec); err != nil {
				return err
			}
		}
		return nil
	case Final:
		if ev.PlanOnly {
			return r.writef("%s final: plan-only=true success=%v duration=%s\n",
				ts, ev.Success, ev.Duration)
		}
		if ev.Cancelled {
			return r.writef("%s final: cancelled=true cluster=%s duration=%s dropped=%d\n",
				ts, ev.ClusterID, ev.Duration, ev.DroppedProgressEvents)
		}
		if ev.Verdict != "" {
			return r.writef("%s final  verdict=%s totalChecks=%d passed=%d failed=%d\n",
				ts, ev.Verdict, ev.TotalChecks, ev.PassedCount, ev.FailedCount)
		}
		return r.writef("%s final: success=%v cluster=%s backend=%s duration=%s dropped=%d\n",
			ts, ev.Success, ev.ClusterID, ev.NVCFBackendHealth, ev.Duration, ev.DroppedProgressEvents)
	case Snapshot:
		return r.writef("%s status snapshot cluster=%s verdict=%s reconcile-age=%ds\n",
			ts, ev.Cluster, ev.Verdict, ev.ReconcileAgeSec)
	case ComponentHealth:
		uptime := humanizeSec(ev.UptimeSec)
		suffix := ""
		if !ev.Healthy && ev.Message != "" {
			suffix = fmt.Sprintf(` reason="%s"`, ev.Message)
		}
		if ev.Cluster != "" && ev.Role != "" {
			return r.writef("%s component %-18s cluster=%s role=%s ready=%d/%d uptime=%s%s\n",
				ts, ev.Name, ev.Cluster, ev.Role, ev.Ready, ev.Total, uptime, suffix)
		}
		if ev.Cluster != "" {
			return r.writef("%s component %-18s cluster=%s ready=%d/%d uptime=%s%s\n",
				ts, ev.Name, ev.Cluster, ev.Ready, ev.Total, uptime, suffix)
		}
		if ev.Role != "" {
			return r.writef("%s component %-18s role=%s ready=%d/%d  uptime=%s%s\n",
				ts, ev.Name, ev.Role, ev.Ready, ev.Total, uptime, suffix)
		}
		return r.writef("%s component %-18s ready=%d/%d  uptime=%s%s\n",
			ts, ev.Name, ev.Ready, ev.Total, uptime, suffix)
	case ClusterRow:
		age := humanizeSec(ev.LastSeenAgeSec)
		suffix := ""
		if ev.Context != "" {
			suffix += fmt.Sprintf(" ctx=%s", ev.Context)
		}
		if ev.IsCurrent {
			suffix += " ◄ this"
		}
		return r.writef("%s compute-cluster %-15sgpu=%s count=%d active-deployments=%d  last-seen-age=%s%s\n",
			ts, ev.Name, ev.GPU, ev.GPUCount, ev.ActiveDeployments, age, suffix)
	case RecentEvent:
		if err := r.writef("%s event age=%s kind=%s", ts, humanizeSec(ev.AgeSec), ev.Kind); err != nil {
			return err
		}
		if ev.Status != "" {
			if err := r.writef(" status=%s", ev.Status); err != nil {
				return err
			}
		}
		if err := r.writef(" name=%s", ev.Name); err != nil {
			return err
		}
		if ev.Version != "" {
			if err := r.writef(" version=%s", ev.Version); err != nil {
				return err
			}
		}
		return r.writef("\n")
	case CheckStarted:
		return r.writef("%s [pre] check_started        %s/%s\n", ts, ev.Category, ev.ID)
	case CheckCompleted:
		// Build category/ID column, pad to 43 chars per §6.6.2 mock.
		catID := fmt.Sprintf("%s/%s", ev.Category, ev.ID)
		padded := fmt.Sprintf("%-43s", catID)
		if err := r.writef("%s [pre] check_completed      %spassed=%v", ts, padded, ev.Passed); err != nil {
			return err
		}
		if !ev.Passed {
			if err := r.writef(` severity=%s message="%s"`, ev.Severity, ev.Message); err != nil {
				return err
			}
		}
		if ev.Detail != "" {
			if err := r.writef(` detail="%s"`, ev.Detail); err != nil {
				return err
			}
		}
		if ev.HintURL != "" {
			if err := r.writef(` hintURL="%s"`, ev.HintURL); err != nil {
				return err
			}
		}
		return r.writef("\n")
	case CategoryCompleted:
		return r.writef("%s [pre] category_completed   %-25spassed=%d failed=%d duration=%gs\n",
			ts, ev.Category, ev.PassedCount, ev.FailedCount, ev.DurationSec)
	case DrainProgress:
		return r.writef("%s [%02d/%d] drain-active: %s → %s\n",
			ts, ev.Num, r.effectiveTotalPhases(), ev.Deployment, ev.State)
	case LogLine:
		// Plain mode prefixes the source/stream so consumers tailing the
		// stream can distinguish helmfile chatter from progress events. No
		// timestamp — keeps the line short and matches the upstream
		// subprocess output as closely as possible.
		return r.writef("[%s] %s\n", ev.Source, ev.Line)
	}
	return nil
}

// humanizeSec converts a duration in seconds to a human-readable string.
// < 60 → Ns, < 3600 → 02m02s (zero-padded), < 86400 → Nh, else Nd.
func humanizeSec(sec int) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%02dm%02ds", sec/60, sec%60)
	case sec < 86400:
		return fmt.Sprintf("%dh", sec/3600)
	default:
		return fmt.Sprintf("%dd", sec/86400)
	}
}

// writef wraps fmt.Fprintf with a format string that callers MUST hard-code.
// Never pass user-supplied or pre-formatted strings as `format`; doing so
// could allow event-field values to re-introduce %-verb interpretation.
// Pass user data only as args.
func (r *PlainRenderer) writef(format string, args ...any) error {
	_, err := fmt.Fprintf(r.w, format, args...)
	return err
}

// Close is a no-op for the plain renderer; lines are flushed eagerly.
func (*PlainRenderer) Close() error { return nil }
