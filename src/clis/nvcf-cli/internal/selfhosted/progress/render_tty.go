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
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// RenderMode discriminates the layout the bubbletea Model produces.
type RenderMode int

const (
	ModeInstall RenderMode = iota // existing 8-phase install dashboard
	ModeStatus                    // steady-state status snapshot (M+8)
	ModeCheck                     // pre-flight check stream (M+8)
	ModeDown                      // per-plane uninstall (3 or 5 phases) or full down (7 phases) (M+11)
)

// ─── NVIDIA palette (mirrors progress-mock/main.go) ───────────────────────────

const (
	nvGreenHex     = "#76B900" // primary brand
	nvGreenDimHex  = "#5A8C00" // completed phase markers
	nvLightGrayHex = "#9E9E9E" // pending / muted
	nvRedHex       = "#E53935" // failure
	nvWhiteHex     = "#FFFFFF"
	nvAmberHex     = "#F9A825" // waiting (reserved)
)

// glyph markers — chosen so an ASCII-stripped frame still encodes phase state.
const (
	glyphDone    = "[✓]"
	glyphRun     = "[▶]"
	glyphPending = "[ ]"
	glyphFailed  = "[✘]"
)

// phaseStatus enumerates the lifecycle of a phase as observed by the renderer.
type phaseStatus int

const (
	phasePending phaseStatus = iota
	phaseRunning
	phaseCompleted
	phaseFailed
	phaseCancelled
)

// phaseNames lists the 8 install phases in execution order. The phase numbers
// in events are 1-based; index 0-based here.
var phaseNames = [totalPhases]string{
	"Preflight checks",
	"Resolve stack bundle",
	"Render control plane",
	"Apply control plane",
	"Check control plane health",
	"Register compute cluster",
	"Apply compute plane",
	"Final health check",
}

// downPhaseNames are the 7 down-orchestrator phases (full teardown). (M+11)
var downPhaseNames = []string{
	"Pre-flight checks",
	"Drain active deployments",
	"Uninstall compute plane",
	"Remove cluster row",
	"Uninstall control plane",
	"Remove namespaces",
	"Verify clean",
}

// uninstallPhaseNames are the 3 basic per-plane teardown phases. (M+11)
var uninstallPhaseNames = []string{
	"Pre-flight checks",
	"Render uninstall manifests",
	"Helmfile destroy",
}

// subProgress is the per-resource bar inside a long apply phase.
//
// We render the bar via progress.Model.ViewAs(pct) — a pure function that
// produces a static frame. This sidesteps the bubbles/progress animation
// pipeline (SetPercent → FrameMsg → View) which would only matter for live
// terminals; tests need deterministic frames and View-pure semantics.
type subProgress struct {
	resource    string
	done, total int
	pct         float64
	bar         progress.Model
}

// phaseState tracks the dynamic state of a single phase. Sub-progress entries
// are stored in insertion order so the dashboard renders them deterministically.
type phaseState struct {
	num         int
	name        string
	state       phaseStatus
	startedAt   time.Time
	completedAt time.Time
	duration    time.Duration

	subs    []subProgress
	subKeys map[string]int

	waiting    string
	lastProg   string
	lastProgAt time.Time

	ctx string // M+9: kubeconfig context for this phase ("" in single-cluster mode)
}

// ModelOpts captures Model construction parameters. NowFunc is the clock seam
// used by tests; production passes time.Now.
type ModelOpts struct {
	Cluster string
	Target  string
	Stack   string

	// Mode selects which layout View() produces. Zero value (ModeInstall)
	// preserves existing M+7 behaviour so all callers remain compatible.
	Mode RenderMode

	// TotalChecks is the expected total number of checks, used by ModeCheck
	// to render the "N/total passed" tally before all rows have been
	// accumulated. Driven by the check command which knows the full check set.
	TotalChecks int

	// TotalPhases overrides the default 8 phases for ModeDown / ModeUninstall.
	// Zero or unset → default 8 (ModeInstall behaviour preserved). For ModeDown,
	// defaults to 7 (full teardown) when left zero. For uninstall, callers pass
	// 3 (basic) or 5 (with drain + persistent removal). (M+11)
	TotalPhases int

	NowFunc func() time.Time

	// Started, when non-zero, pins the install start time. This lets tests
	// produce deterministic elapsed durations without sequencing PhaseStarted
	// events. Production leaves it zero and the first PhaseStarted seeds it.
	Started time.Time

	// AsciiOnly forces View() to render through a termenv.Ascii profile so
	// the output contains no ANSI escape sequences. Used for the
	// tty_ascii_frame golden.
	AsciiOnly bool

	// Output is the writer used for color-capability detection by the
	// lipgloss.Renderer. Defaults to os.Stderr in production (matches what
	// tea.Program writes to) — NewTTYRenderer threads its stderr arg here.
	// Tests can leave this nil; goldens stay deterministic because
	// `go test`'s stdout is non-TTY → Ascii profile.
	Output io.Writer

	// ForceColorProfile, when non-nil, overrides whatever profile the
	// lipgloss.Renderer auto-detects from Output. Test-only seam used by
	// TestTTY_ColorToggleProvable to assert that AsciiOnly toggles ANSI
	// emission without depending on the host terminal's capabilities.
	// Production callers leave this nil.
	ForceColorProfile *termenv.Profile

	// ControlPlaneContext and ComputePlaneContext are the kubeconfig contexts
	// for the control and compute planes in split-cluster mode (M+9, REQ-20).
	// When both are non-empty and differ, the install header gains "Control:"
	// and "Compute:" lines and per-phase rows show a "→ <ctx>" annotation.
	// Leave both empty (the default) for single-cluster mode.
	ControlPlaneContext string
	ComputePlaneContext string
}

// checkCategoryState holds the accumulated state for one pre-flight check
// category (ModeCheck).
type checkCategoryState struct {
	name     string
	checks   []checkRow
	keys     map[string]int // id → index in checks
	duration time.Duration
	final    bool // CategoryCompleted received
}

// checkRow tracks a single check within a category.
type checkRow struct {
	id       string
	started  bool
	finished bool
	passed   bool
	severity string
	message  string
	startMsg string // human label from CheckStarted.Message, shown while in-flight
	detail   string
	hintURL  string
}

// Model is the bubbletea Elm-architecture model that backs the TTY renderer.
//
// Tests exercise Update/View directly; production wraps it in a tea.Program
// owned by TTYRenderer.
type Model struct {
	// identity (set at construction)
	cluster    string
	target     string
	stack      string
	controlCtx string // M+9: control-plane kubeconfig context (empty → single-cluster)
	computeCtx string // M+9: compute-plane kubeconfig context (empty → single-cluster)
	nowFunc    func() time.Time

	// dynamic state (mutated by Update)
	started  time.Time
	now      time.Time
	phases   [totalPhases]phaseState
	finished bool
	failed   bool

	// activePhases is the number of phases used in the current mode (M+11).
	// 8 for ModeInstall, 7 for ModeDown full teardown, 3-5 for uninstall.
	activePhases int

	// post-success summary fields
	clusterID         string
	clusterGroupID    string
	nvcfBackendHealth string
	finalDuration     time.Duration
	planOnly          bool     // true when reached via --plan-only short-circuit
	planned           *Planned // non-nil when --plan-only emitted a Planned event

	// failure summary
	failedPhase   int
	errCategory   string
	errMessage    string
	retryClass    string
	retryAfterSec int
	remediation   []string

	// cancellation
	cancelReason string

	// bubbletea components
	spinner spinner.Model

	// terminal geometry
	width  int
	height int

	// rendering
	renderer  *lipgloss.Renderer
	asciiOnly bool

	// status mode (mode == ModeStatus)
	mode         RenderMode
	snapshot     *Snapshot
	components   []ComponentHealth
	clusters     []ClusterRow
	recentEvents []RecentEvent

	// check mode (mode == ModeCheck)
	checkCategories  []checkCategoryState
	checkCategoryIdx map[string]int
	totalChecks      int // hint from ModelOpts; 0 means derive from accumulated rows
	checkFinalTotal  int
	checkFinalPassed int
	checkFinalFailed int

	// log tail (LogLine ring buffer; rendered as a "Recent" panel during
	// long phases like apply-cp). Capacity is fixed; new lines push older
	// ones out FIFO. Empty when no LogLine events have arrived yet, in
	// which case the panel is hidden entirely (no blank space reserved).
	logTail [recentLogCapacity]string
	logHead int // next write index (wraps mod recentLogCapacity)
	logLen  int // populated entries (≤ recentLogCapacity)
}

// recentLogCapacity caps the LogLine ring buffer. 8 lines fits a typical
// phase's most recent helmfile chatter (Pulling/Comparing/Upgrading) without
// dominating the dashboard or pushing the phase list off-screen on small
// terminals.
const recentLogCapacity = 8

// tickMsg drives the Elapsed counter; sent by tickEvery() at 250ms cadence.
type tickMsg time.Time

// eventMsg is the bubbletea wrapper for a progress.Event so a single message
// path threads orchestrator events into Update.
type eventMsg struct{ e Event }

// NewModel constructs a Model. In ModeInstall (the default), all 8 phases
// are pre-populated as pending. In ModeStatus and ModeCheck, no phases are
// populated; mode-specific state is initialised instead.
func NewModel(opts ModelOpts) Model {
	now := opts.NowFunc
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	// Color-capability detection runs against the actual output writer so
	// production renders color when stderr is a TTY. Tests that don't pass
	// Output get io.Discard, which auto-detects to termenv.Ascii — keeping
	// goldens deterministic.
	out := opts.Output
	if out == nil {
		out = io.Discard
	}
	r := lipgloss.NewRenderer(out)
	switch {
	case opts.ForceColorProfile != nil:
		r.SetColorProfile(*opts.ForceColorProfile)
		if opts.AsciiOnly {
			r.SetColorProfile(termenv.Ascii)
		}
	case opts.AsciiOnly:
		r.SetColorProfile(termenv.Ascii)
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = r.NewStyle().Foreground(lipgloss.Color(nvGreenHex))

	m := Model{
		cluster:     opts.Cluster,
		target:      opts.Target,
		stack:       opts.Stack,
		controlCtx:  opts.ControlPlaneContext,
		computeCtx:  opts.ComputePlaneContext,
		nowFunc:     now,
		started:     opts.Started,
		spinner:     sp,
		renderer:    r,
		asciiOnly:   opts.AsciiOnly,
		mode:        opts.Mode,
		totalChecks: opts.TotalChecks,
	}

	switch opts.Mode {
	case ModeStatus:
		m.components = []ComponentHealth{}
		m.clusters = []ClusterRow{}
		m.recentEvents = []RecentEvent{}
	case ModeCheck:
		m.checkCategoryIdx = map[string]int{}
	case ModeDown: // M+11: teardown mode (full down = 7 phases, uninstall = 3-5 phases)
		n := opts.TotalPhases
		if n <= 0 {
			n = len(downPhaseNames) // default: 7 for full teardown
		}
		m.activePhases = n
		// Populate the shared phases array up to n entries using downPhaseNames
		// or uninstallPhaseNames as the source depending on total count. Callers
		// can always override via PhaseStarted.Name events (ev.Name != "" path).
		names := downPhaseNames
		if n <= len(uninstallPhaseNames) {
			names = uninstallPhaseNames
		}
		for i := 0; i < n && i < totalPhases; i++ {
			name := ""
			if i < len(names) {
				name = names[i]
			}
			m.phases[i] = phaseState{
				num:     i + 1,
				name:    name,
				state:   phasePending,
				subKeys: map[string]int{},
			}
		}
	default: // ModeInstall
		m.activePhases = totalPhases
		for i := 0; i < totalPhases; i++ {
			m.phases[i] = phaseState{
				num:     i + 1,
				name:    phaseNames[i],
				state:   phasePending,
				subKeys: map[string]int{},
			}
		}
	}
	return m
}

// SetSize is a test seam for forcing terminal dimensions without sending
// tea.WindowSizeMsg through the bubbletea event loop.
func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// colorProfile returns the termenv profile the Model uses for styling.
// Mirrors what NewModel set on the lipgloss.Renderer so sub-progress bars
// (constructed via bubbles/progress, which reads from the global termenv
// by default) honor the same AsciiOnly flag as the rest of View.
func (m Model) colorProfile() termenv.Profile {
	if m.asciiOnly {
		return termenv.Ascii
	}
	return m.renderer.ColorProfile()
}

// Init returns the initial Cmd batch — spinner ticks + 250ms wall-clock ticks.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tickEvery())
}

// Update is the bubbletea reducer. See SRD/SDD §6.4.1 for the event semantics.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
		if m.finished {
			return m, nil
		}
		return m, tickEvery()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case eventMsg:
		return m.applyEvent(msg.e)
	}
	return m, nil
}

// applyEvent translates a single progress.Event into Model state mutations.
// Routes to mode-specific helpers for ModeStatus and ModeCheck; falls through
// to the existing install/down logic for ModeInstall and ModeDown.
//
// LogLine is handled here (mode-agnostic) before the mode dispatch because
// any mode that spawns a subprocess — install, down, uninstall — can emit
// helmfile/kubectl chatter that should reach the "Recent" tail panel.
func (m Model) applyEvent(e Event) (tea.Model, tea.Cmd) {
	if ev, ok := e.(LogLine); ok {
		line := strings.TrimRight(ev.Line, " \t\r\n")
		if line != "" {
			m.logTail[m.logHead] = line
			m.logHead = (m.logHead + 1) % recentLogCapacity
			if m.logLen < recentLogCapacity {
				m.logLen++
			}
		}
		return m, nil
	}
	if m.mode == ModeStatus {
		return m.applyStatusEvent(e)
	}
	if m.mode == ModeCheck {
		return m.applyCheckEvent(e)
	}
	if m.mode == ModeDown {
		return m.applyDownEvent(e)
	}
	return m.applyInstallEvent(e)
}

// applyInstallEvent is the original install-mode event handler (formerly the
// body of applyEvent). Unchanged from M+7.
func (m Model) applyInstallEvent(e Event) (tea.Model, tea.Cmd) {
	switch ev := e.(type) {

	case PhaseStarted:
		idx := ev.Num - 1
		if idx < 0 || idx >= totalPhases {
			return m, nil
		}
		m.phases[idx].state = phaseRunning
		if ev.Name != "" {
			m.phases[idx].name = ev.Name
		}
		m.phases[idx].startedAt = ev.StartedAt
		m.phases[idx].ctx = ev.Context // M+9: record the target context for this phase
		if m.started.IsZero() {
			m.started = ev.StartedAt
		}
		return m, nil

	case PhaseProgress:
		idx := ev.Num - 1
		if idx < 0 || idx >= totalPhases {
			return m, nil
		}
		ph := &m.phases[idx]
		if ph.subKeys == nil {
			ph.subKeys = map[string]int{}
		}
		pct := percentOf(ev.Done, ev.Total)
		if i, ok := ph.subKeys[ev.Resource]; ok {
			ph.subs[i].done = ev.Done
			ph.subs[i].total = ev.Total
			ph.subs[i].pct = pct
		} else {
			bar := progress.New(
				progress.WithSolidFill(nvGreenHex),
				progress.WithoutPercentage(),
				progress.WithWidth(18),
				progress.WithColorProfile(m.colorProfile()),
			)
			ph.subKeys[ev.Resource] = len(ph.subs)
			ph.subs = append(ph.subs, subProgress{
				resource: ev.Resource,
				done:     ev.Done,
				total:    ev.Total,
				pct:      pct,
				bar:      bar,
			})
		}
		return m, nil

	case PhaseCompleted:
		idx := ev.Num - 1
		if idx < 0 || idx >= totalPhases {
			return m, nil
		}
		m.phases[idx].state = phaseCompleted
		m.phases[idx].duration = ev.Duration
		m.phases[idx].completedAt = m.nowFunc()
		// Drop the noise-y status lines now that the phase has succeeded.
		m.phases[idx].waiting = ""
		m.phases[idx].lastProg = ""
		return m, nil

	case PhaseFailed:
		idx := ev.Num - 1
		if idx < 0 || idx >= totalPhases {
			return m, nil
		}
		m.phases[idx].state = phaseFailed
		m.phases[idx].duration = ev.Duration
		m.failed = true
		m.finished = true
		m.failedPhase = ev.Num
		m.errCategory = ev.ErrCategory
		m.errMessage = ev.ErrMessage
		m.retryClass = ev.RetryClass
		m.retryAfterSec = ev.RetryAfterSec
		m.remediation = append([]string(nil), ev.Remediation...)
		return m, nil

	case PhaseCancelled:
		idx := ev.Num - 1
		if idx < 0 || idx >= totalPhases {
			return m, nil
		}
		m.phases[idx].state = phaseCancelled
		m.cancelReason = ev.Reason
		m.finished = true
		return m, nil

	case Waiting:
		idx := ev.Num - 1
		if idx < 0 || idx >= totalPhases {
			return m, nil
		}
		m.phases[idx].waiting = ev.Reason
		return m, nil

	case LastProgress:
		idx := ev.Num - 1
		if idx < 0 || idx >= totalPhases {
			return m, nil
		}
		at := ev.At
		if at.IsZero() {
			at = m.nowFunc()
		}
		m.phases[idx].lastProg = ev.Detail
		m.phases[idx].lastProgAt = at
		// Forward progress invalidates any prior "waiting" reason.
		m.phases[idx].waiting = ""
		return m, nil

	case Planned:
		// --plan-only: record the plan so viewFull can surface it, then mark
		// finished so the dashboard moves to the summary block and quits cleanly.
		// NOTE: the Final{PlanOnly:true} that follows on the bus is swallowed by
		// bubbletea's post-Quit drain — m.planOnly is never set on this path. The
		// dry-run summary keys off m.planned != nil instead, which IS set here.
		m.planned = &ev
		m.finished = true
		return m, tea.Quit

	case Final:
		m.finished = true
		m.clusterID = ev.ClusterID
		m.clusterGroupID = ev.ClusterGroupID
		m.nvcfBackendHealth = ev.NVCFBackendHealth
		m.finalDuration = ev.Duration
		m.planOnly = ev.PlanOnly
		return m, tea.Quit
	}
	return m, nil
}

// applyDownEvent handles events in ModeDown (teardown / uninstall). It extends
// applyInstallEvent with a DrainProgress arm and uses activePhases for bounds
// checking instead of the fixed totalPhases constant. (M+11)
func (m Model) applyDownEvent(e Event) (tea.Model, tea.Cmd) {
	// DrainProgress is ModeDown-specific — update lastProg on the drain phase.
	if ev, ok := e.(DrainProgress); ok {
		idx := ev.Num - 1
		if idx >= 0 && idx < m.activePhases && idx < totalPhases {
			m.phases[idx].lastProg = fmt.Sprintf("%s → %s", ev.Deployment, ev.State)
			m.phases[idx].lastProgAt = m.nowFunc()
		}
		return m, nil
	}

	// PhaseStarted / PhaseCompleted / PhaseFailed / PhaseCancelled / Waiting /
	// LastProgress / Planned / Final all use the install handler, but we need to
	// enforce activePhases bounds where applyInstallEvent uses totalPhases.
	// Simplest: delegate to applyInstallEvent then post-process — however the
	// bounds checks in applyInstallEvent use the totalPhases const. Because
	// activePhases <= totalPhases, the phases array is always large enough;
	// out-of-range phase events are silently dropped by applyInstallEvent (which
	// checks idx < totalPhases). That is the correct behaviour for ModeDown too.
	return m.applyInstallEvent(e)
}

// applyStatusEvent handles events in ModeStatus.
//
// Each Snapshot opens a fresh tick: the prior tick's per-component /
// per-cluster / recent-event rows are cleared so --watch mode shows the
// current state of the cluster on every redraw rather than accumulating
// duplicates. Without this reset, a `status --watch` session at 5s
// intervals over an hour grows the components list to 9·720 ≈ 6500 rows.
func (m Model) applyStatusEvent(e Event) (tea.Model, tea.Cmd) {
	switch ev := e.(type) {
	case Snapshot:
		m.snapshot = &ev
		// Reset slices in place — keeps allocations stable across long
		// watch sessions instead of churning a new backing array per tick.
		m.components = m.components[:0]
		m.clusters = m.clusters[:0]
		m.recentEvents = m.recentEvents[:0]
		return m, nil
	case ComponentHealth:
		m.components = append(m.components, ev)
		return m, nil
	case ClusterRow:
		m.clusters = append(m.clusters, ev)
		return m, nil
	case RecentEvent:
		m.recentEvents = append(m.recentEvents, ev)
		return m, nil
	case Final:
		m.finished = true
		m.cancelReason = "stopped"
		return m, tea.Quit
	}
	return m, nil
}

// applyCheckEvent handles events in ModeCheck.
func (m Model) applyCheckEvent(e Event) (tea.Model, tea.Cmd) {
	switch ev := e.(type) {
	case CheckStarted:
		i, ok := m.checkCategoryIdx[ev.Category]
		if !ok {
			i = len(m.checkCategories)
			m.checkCategories = append(m.checkCategories, checkCategoryState{
				name: ev.Category,
				keys: map[string]int{},
			})
			m.checkCategoryIdx[ev.Category] = i
		}
		cat := &m.checkCategories[i]
		if _, exists := cat.keys[ev.ID]; !exists {
			cat.keys[ev.ID] = len(cat.checks)
			cat.checks = append(cat.checks, checkRow{id: ev.ID, started: true, startMsg: ev.Message})
		}
		return m, nil

	case CheckCompleted:
		ci, ok := m.checkCategoryIdx[ev.Category]
		if !ok {
			return m, nil
		}
		cat := &m.checkCategories[ci]
		ri, exists := cat.keys[ev.ID]
		if !exists {
			// CheckStarted may have been suppressed; insert a row in finished state.
			ri = len(cat.checks)
			cat.keys[ev.ID] = ri
			cat.checks = append(cat.checks, checkRow{id: ev.ID})
		}
		c := &cat.checks[ri]
		c.started = true
		c.finished = true
		c.passed = ev.Passed
		c.severity = ev.Severity
		c.message = ev.Message
		c.detail = ev.Detail
		c.hintURL = ev.HintURL
		return m, nil

	case CategoryCompleted:
		ci, ok := m.checkCategoryIdx[ev.Category]
		if !ok {
			return m, nil
		}
		cat := &m.checkCategories[ci]
		cat.duration = time.Duration(ev.DurationSec * float64(time.Second))
		cat.final = true
		return m, nil

	case Final:
		m.finished = true
		m.checkFinalTotal = ev.TotalChecks
		m.checkFinalPassed = ev.PassedCount
		m.checkFinalFailed = ev.FailedCount
		return m, tea.Quit
	}
	return m, nil
}

// percentOf returns done/total clamped to [0, 1]. A zero Total renders an
// empty bar rather than panicking on divide-by-zero.
func percentOf(done, total int) float64 {
	if total <= 0 {
		return 0
	}
	if done >= total {
		return 1.0
	}
	if done <= 0 {
		return 0
	}
	return float64(done) / float64(total)
}

// tickEvery returns a Cmd that re-fires tickMsg every 250ms. The bubbletea
// runtime guarantees the produced message lands in Update before the next
// frame, so the Elapsed clock advances smoothly without flooding.
func tickEvery() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// ─── View ─────────────────────────────────────────────────────────────────────

// View renders the dashboard. View is pure: every state lives on the receiver,
// so the same Model produces the same string regardless of when View is called.
func (m Model) View() string {
	now := m.now
	if now.IsZero() {
		now = m.nowFunc()
	}

	if m.mode == ModeStatus {
		return m.viewStatus(now)
	}
	if m.mode == ModeCheck {
		return m.viewCheck(now)
	}
	if m.mode == ModeDown {
		if m.compact() {
			return m.viewDownCompact(now)
		}
		return m.viewDown(now)
	}

	if m.compact() {
		return m.viewCompact(now)
	}
	return m.viewFull(now)
}

// viewFull renders the wide-terminal dashboard.
func (m Model) viewFull(now time.Time) string {
	parts := []string{
		m.renderHeader(now),
		"",
		m.styleSection().Render("Progress"),
		"",
		m.renderChecklist(now),
	}

	if m.shouldRenderCurrentPanel() {
		parts = append(parts, "", m.renderCurrentPhase(now))
	}

	if !m.finished {
		if next := m.nextPendingName(); next != "" {
			parts = append(parts, "",
				m.styleHeaderLabel().Render("Next: ")+m.styleHeaderValue().Render(next),
			)
		}
		if recent := m.renderRecentPanel(); recent != "" {
			parts = append(parts, "", recent)
		}
	}

	if m.finished && m.planOnly && m.planned != nil {
		parts = append(parts, "", m.renderPlanOnly())
	} else if m.finished && !m.failed && !m.isCancelled() {
		parts = append(parts, "", m.renderFinalSuccess())
	}
	if m.finished && m.failed {
		parts = append(parts, "", m.renderFinalFailure())
	}

	parts = append(parts, "", m.styleMuted().Render("press q to quit"))
	return strings.Join(parts, "\n") + "\n"
}

// renderRecentPanel returns the "Recent" tail panel showing the last N
// LogLine events captured by the ring buffer. Returns "" when no log
// lines have arrived yet (panel hidden, no blank space reserved).
//
// Lines are rendered in chronological order (oldest first → newest last)
// and truncated to fit the current terminal width. When width is zero
// (size-not-yet-detected) we use a generous 200-col cap so we don't
// pre-wrap legitimately long lines on the first frame.
func (m Model) renderRecentPanel() string {
	if m.logLen == 0 {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 200
	}
	// Reserve a 2-space indent for readability; truncate any single line
	// that exceeds the available column count with a trailing ellipsis.
	lineCap := width - 2
	if lineCap < 20 {
		lineCap = 20 // pathological small terminal — show something
	}

	lines := make([]string, 0, m.logLen+1)
	lines = append(lines, m.styleSection().Render("Recent:"))

	// Walk the ring buffer oldest-first. With logLen entries and write
	// head at logHead, the oldest is at (logHead - logLen) mod cap.
	start := (m.logHead - m.logLen + recentLogCapacity) % recentLogCapacity
	for i := 0; i < m.logLen; i++ {
		idx := (start + i) % recentLogCapacity
		line := m.logTail[idx]
		if len(line) > lineCap {
			line = line[:lineCap-1] + "…"
		}
		lines = append(lines, "  "+m.styleMuted().Render(line))
	}
	return strings.Join(lines, "\n")
}

// viewCompact renders the narrow-terminal dashboard. Sub-progress bars and
// the next-up section are dropped; the header collapses to two lines.
func (m Model) viewCompact(now time.Time) string {
	parts := []string{
		m.styleHeaderLabel().Render("NVCF self-hosted install: ") + m.styleClusterName().Render(m.cluster),
		m.styleHeaderLabel().Render("Elapsed: ") + m.styleHeaderValue().Render(formatDuration(m.elapsed(now))),
		"",
		m.styleSection().Render("Progress"),
		"",
		m.renderChecklist(now),
	}

	// Title-only current-phase panel (sub-progress + status lines hidden).
	if m.shouldRenderCurrentPanel() {
		ph := m.runningPhase()
		if ph != nil {
			parts = append(parts, "",
				m.styleSection().Render(fmt.Sprintf("Current phase: %s", ph.name)),
			)
		}
	}

	if m.finished && !m.failed && !m.isCancelled() {
		parts = append(parts, "", m.renderFinalSuccessCompact())
	}
	if m.finished && m.failed {
		parts = append(parts, "", m.renderFinalFailureCompact())
	}

	parts = append(parts, "", m.styleMuted().Render("press q to quit"))
	return strings.Join(parts, "\n") + "\n"
}

// ─── ModeDown views (M+11) ────────────────────────────────────────────────────

// viewDown renders the full teardown/uninstall dashboard. Mirrors viewFull but
// uses the "teardown" header and restricts the checklist to activePhases rows.
func (m Model) viewDown(now time.Time) string {
	parts := []string{
		m.renderDownHeader(now),
		"",
		m.styleSection().Render("Reverse-pipeline (helmfile destroy)"),
		"",
		m.renderDownChecklist(now),
	}

	if m.shouldRenderCurrentPanel() {
		parts = append(parts, "", m.renderCurrentPhase(now))
	}

	if !m.finished {
		if next := m.nextDownPendingName(); next != "" {
			parts = append(parts, "",
				m.styleHeaderLabel().Render("Next: ")+m.styleHeaderValue().Render(next),
			)
		}
		if recent := m.renderRecentPanel(); recent != "" {
			parts = append(parts, "", recent)
		}
	}

	if m.finished && m.planOnly && m.planned != nil {
		parts = append(parts, "", m.renderDownPlanOnly())
	} else if m.finished && !m.failed && !m.isCancelled() {
		parts = append(parts, "", m.renderDownFinalSuccess())
	}
	if m.finished && m.failed {
		parts = append(parts, "", m.renderFinalFailure())
	}

	parts = append(parts, "", m.styleMuted().Render("press q to quit"))
	return strings.Join(parts, "\n") + "\n"
}

// viewDownCompact renders the narrow-terminal teardown dashboard.
func (m Model) viewDownCompact(now time.Time) string {
	parts := []string{
		m.styleHeaderLabel().Render("NVCF self-hosted teardown: ") + m.styleClusterName().Render(m.cluster),
		m.styleHeaderLabel().Render("Elapsed: ") + m.styleHeaderValue().Render(formatDuration(m.elapsed(now))),
		"",
		m.styleSection().Render("Reverse-pipeline (helmfile destroy)"),
		"",
		m.renderDownChecklist(now),
	}

	if m.shouldRenderCurrentPanel() {
		ph := m.runningPhase()
		if ph != nil {
			parts = append(parts, "",
				m.styleSection().Render(fmt.Sprintf("Current phase: %s", ph.name)),
			)
		}
	}

	if m.finished && !m.failed && !m.isCancelled() {
		parts = append(parts, "", m.styleMuted().Render(fmt.Sprintf("%s teardown complete in %s", m.cluster, formatDuration(m.finalDuration))))
	}
	if m.finished && m.failed {
		parts = append(parts, "", m.renderFinalFailureCompact())
	}

	parts = append(parts, "", m.styleMuted().Render("press q to quit"))
	return strings.Join(parts, "\n") + "\n"
}

// renderDownHeader produces the teardown header (mirrors renderSingleHeader
// but with "teardown" instead of "install").
func (m Model) renderDownHeader(now time.Time) string {
	rows := [][2]string{
		{"NVCF self-hosted teardown:", m.styleClusterName().Render(m.cluster)},
		{"Elapsed:                  ", m.styleHeaderValue().Render(formatDuration(m.elapsed(now)))},
		{"Target:                   ", m.styleHeaderValue().Render(m.target)},
		{"Stack:                    ", m.styleHeaderValue().Render(m.stack)},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(m.styleHeaderLabel().Render(r[0]))
		b.WriteString(" ")
		b.WriteString(r[1])
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderDownChecklist produces the phase checklist bounded to activePhases rows.
func (m Model) renderDownChecklist(now time.Time) string {
	var b strings.Builder
	limit := m.activePhases
	if limit == 0 || limit > totalPhases {
		limit = totalPhases
	}
	for i := 0; i < limit; i++ {
		ph := &m.phases[i]
		marker, dur, nameStyle := m.phaseLine(ph, now)
		name := nameStyle.Render(fmt.Sprintf("%d. %-32s", ph.num, ph.name))
		b.WriteString("    ")
		b.WriteString(marker)
		b.WriteString("  ")
		b.WriteString(name)
		b.WriteString("    ")
		b.WriteString(dur)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// nextDownPendingName returns the name of the first pending teardown phase.
func (m Model) nextDownPendingName() string {
	limit := m.activePhases
	if limit == 0 || limit > totalPhases {
		limit = totalPhases
	}
	for i := 0; i < limit; i++ {
		if m.phases[i].state == phasePending {
			return m.phases[i].name
		}
	}
	return ""
}

// renderDownPlanOnly produces the --plan-only summary for teardown.
func (m Model) renderDownPlanOnly() string {
	p := m.planned
	var b strings.Builder
	b.WriteString(m.styleSection().Render("Plan (dry run — teardown)"))
	b.WriteString("\n")
	b.WriteString(m.styleHeaderLabel().Render(fmt.Sprintf("  cluster=%s", p.Cluster)))
	b.WriteString("\n\n")
	nph := m.activePhases
	if nph == 0 {
		nph = totalPhases
	}
	for _, ph := range p.Phases {
		b.WriteString(fmt.Sprintf("  [%02d/%d] %-34s ~%ds\n", ph.Num, nph, ph.Name, ph.ETASec))
	}
	if len(p.WillUninstall) > 0 {
		b.WriteString("\n")
		b.WriteString(m.styleSection().Render("  Releases to remove:"))
		b.WriteString("\n")
		for _, rel := range p.WillUninstall {
			b.WriteString(fmt.Sprintf("    %s  %s\n", m.styleHeaderLabel().Render(rel.Kind+"/"+rel.Name), m.styleMuted().Render(rel.Command)))
		}
	}
	b.WriteString("\n")
	b.WriteString(m.styleHeaderValue().Render(fmt.Sprintf("  Total estimated time: ~%ds", p.TotalETASec)))
	return b.String()
}

// renderDownFinalSuccess produces the post-teardown success block.
func (m Model) renderDownFinalSuccess() string {
	return m.styleHeaderValue().Render(
		fmt.Sprintf("Teardown complete in %s", formatDuration(m.finalDuration)),
	)
}

// compact is the layout-mode predicate. It mirrors SelectRenderer's threshold
// via the shared compactThresholdCols / compactThresholdRows constants.
func (m Model) compact() bool {
	if m.width == 0 && m.height == 0 {
		return false
	}
	return m.width < compactThresholdCols || m.height < compactThresholdRows
}

// isSplitCluster reports whether we're in split-cluster mode: both contexts
// are set and differ. Single-cluster mode (either empty or both equal) renders
// the compact single-context header.
func (m Model) isSplitCluster() bool {
	return m.controlCtx != "" && m.computeCtx != "" && m.controlCtx != m.computeCtx
}

// renderHeader produces the install header. In single-cluster mode this is the
// existing 4-line layout. In split-cluster mode (M+9, REQ-20) it expands to
// a 5-line layout per §6.4.1 adding Control: and Compute: lines.
func (m Model) renderHeader(now time.Time) string {
	if m.isSplitCluster() {
		return m.renderSplitHeader(now)
	}
	return m.renderSingleHeader(now)
}

// renderSingleHeader produces the original 4-line install header.
func (m Model) renderSingleHeader(now time.Time) string {
	rows := [][2]string{
		{"NVCF self-hosted install:", m.styleClusterName().Render(m.cluster)},
		{"Elapsed:                 ", m.styleHeaderValue().Render(formatDuration(m.elapsed(now)))},
		{"Target:                  ", m.styleHeaderValue().Render(m.target)},
		{"Stack:                   ", m.styleHeaderValue().Render(m.stack)},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(m.styleHeaderLabel().Render(r[0]))
		b.WriteString(" ")
		b.WriteString(r[1])
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderSplitHeader produces the split-cluster header per §6.4.1:
//
//	NVCF self-hosted install: <cluster>
//	Elapsed:   <elapsed>
//	Control:   <controlCtx>          Stack:  <stack>
//	Compute:   <computeCtx>
func (m Model) renderSplitHeader(now time.Time) string {
	var b strings.Builder

	// Line 1: title + cluster name
	b.WriteString(m.styleHeaderLabel().Render("NVCF self-hosted install:"))
	b.WriteString(" ")
	b.WriteString(m.styleClusterName().Render(m.cluster))
	b.WriteString("\n")

	// Line 2: elapsed
	b.WriteString(m.styleHeaderLabel().Render("Elapsed:                 "))
	b.WriteString(" ")
	b.WriteString(m.styleHeaderValue().Render(formatDuration(m.elapsed(now))))
	b.WriteString("\n")

	// Line 3: Control: <ctx>   Stack: <stack>  (two-column layout)
	controlLine := "Control: " + m.styleHeaderValue().Render(m.controlCtx)
	stackSuffix := m.styleHeaderLabel().Render("Stack:  ") + m.styleHeaderValue().Render(m.stack)
	b.WriteString(twoColLine(controlLine, stackSuffix, 40))
	b.WriteString("\n")

	// Line 4: Compute: <ctx>
	b.WriteString(m.styleHeaderLabel().Render("Compute: "))
	b.WriteString(m.styleHeaderValue().Render(m.computeCtx))

	return b.String()
}

// renderChecklist produces the 8-line phase list. Each line is of the form
//
//	<marker>  <num>. <name padded>          <duration>
//
// The marker carries the phase state in glyph form so an ASCII-only render
// retains all distinctions.
//
// In split-cluster mode (M+9), phases that have a non-empty ctx field append
// a "→ <ctx>" annotation in NVIDIA Green so operators can see which cluster
// each phase targets at a glance.
func (m Model) renderChecklist(now time.Time) string {
	var b strings.Builder
	for i := range m.phases {
		ph := &m.phases[i]
		marker, dur, nameStyle := m.phaseLine(ph, now)
		name := nameStyle.Render(fmt.Sprintf("%d. %-32s", ph.num, ph.name))
		b.WriteString("    ")
		b.WriteString(marker)
		b.WriteString("  ")
		b.WriteString(name)
		// M+9: when in split-cluster mode and this phase has a context, emit
		// "→ <ctx>" between the name and the duration so the operator can see
		// which cluster each phase targets.
		if ph.ctx != "" {
			b.WriteString("  ")
			b.WriteString(m.styleSection().Render("→ " + ph.ctx))
		}
		b.WriteString("    ")
		b.WriteString(dur)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// phaseLine returns the styled (marker, duration, name-style) tuple for a
// single checklist row.
func (m Model) phaseLine(ph *phaseState, now time.Time) (marker, dur string, nameStyle lipgloss.Style) {
	switch ph.state {
	case phaseCompleted:
		marker = m.styleDoneMark().Render(glyphDone)
		dur = m.styleMuted().Render(formatDuration(ph.duration))
		nameStyle = m.styleWhite()
	case phaseRunning:
		marker = m.styleRunMark().Render(glyphRun)
		elapsed := time.Duration(0)
		if !ph.startedAt.IsZero() {
			elapsed = now.Sub(ph.startedAt)
			if elapsed < 0 {
				elapsed = 0
			}
		}
		dur = m.styleRunDuration().Render(formatDuration(elapsed) + " running")
		nameStyle = m.styleWhite()
	case phaseFailed:
		marker = m.styleFailMark().Render(glyphFailed)
		dur = m.styleMuted().Render(formatDuration(ph.duration) + " failed")
		nameStyle = m.styleWhite()
	case phaseCancelled:
		marker = m.styleFailMark().Render(glyphFailed)
		dur = m.styleMuted().Render("cancelled")
		nameStyle = m.styleWhite()
	default:
		marker = m.stylePendMark().Render(glyphPending)
		dur = m.styleMuted().Render("-")
		nameStyle = m.styleMuted()
	}
	return marker, dur, nameStyle
}

// shouldRenderCurrentPanel returns true when there's a running phase with at
// least one sub-progress bar to display.
func (m Model) shouldRenderCurrentPanel() bool {
	if m.finished {
		return false
	}
	ph := m.runningPhase()
	return ph != nil && len(ph.subs) > 0
}

// runningPhase returns a pointer to the first phase whose state is phaseRunning,
// or nil if none.
func (m Model) runningPhase() *phaseState {
	for i := range m.phases {
		if m.phases[i].state == phaseRunning {
			return &m.phases[i]
		}
	}
	return nil
}

// renderCurrentPhase renders the per-resource sub-progress panel + the
// waiting / last-progress status lines.
func (m Model) renderCurrentPhase(now time.Time) string {
	ph := m.runningPhase()
	if ph == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.styleSection().Render(fmt.Sprintf("Current phase: %s ", ph.name)))
	b.WriteString(m.spinner.View())
	b.WriteString("\n\n")

	for _, s := range ph.subs {
		bar := s.bar.ViewAs(s.pct)
		fmt.Fprintf(&b, "        %s  %s  %s  %s\n",
			m.styleResourceName().Render(padRight(s.resource, 14)),
			bar,
			m.styleMuted().Render(fmt.Sprintf("%2d/%-2d", s.done, s.total)),
			m.styleResourceLabel().Render(resourceLabelFor(s.resource)),
		)
	}

	if ph.waiting != "" {
		b.WriteString("\n        ")
		b.WriteString(m.styleWaitingTag().Render("waiting:"))
		b.WriteString(" " + m.styleWhite().Render(ph.waiting))
		b.WriteString("\n")
	}
	if ph.lastProg != "" {
		ago := time.Duration(0)
		if !ph.lastProgAt.IsZero() {
			ago = now.Sub(ph.lastProgAt)
			if ago < 0 {
				ago = 0
			}
		}
		b.WriteString("        ")
		b.WriteString(m.styleProgressTag().Render("→ last progress:"))
		b.WriteString(" " + m.styleMuted().Render(
			fmt.Sprintf("%s ago, %s", formatDuration(ago), ph.lastProg),
		))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// nextPendingName returns the human-friendly name of the first pending phase,
// or "" if none.
func (m Model) nextPendingName() string {
	for i := range m.phases {
		if m.phases[i].state == phasePending {
			return m.phases[i].name
		}
	}
	return ""
}

// renderPlanOnly produces the --plan-only summary panel. Lists each phase with
// its P50 ETA, followed by the total estimated install time.
func (m Model) renderPlanOnly() string {
	p := m.planned
	var b strings.Builder
	b.WriteString(m.styleSection().Render("Plan (dry run)"))
	b.WriteString("\n")
	b.WriteString(m.styleHeaderLabel().Render(fmt.Sprintf("  cluster=%s  target=%s  stack=%s", p.Cluster, p.Target, p.Stack)))
	b.WriteString("\n\n")
	for _, ph := range p.Phases {
		b.WriteString(fmt.Sprintf("  [%02d/%d] %-34s ~%ds\n", ph.Num, totalPhases, ph.Name, ph.ETASec))
	}
	b.WriteString("\n")
	b.WriteString(m.styleHeaderValue().Render(fmt.Sprintf("  Total estimated time: ~%ds", p.TotalETASec)))
	return b.String()
}

// renderFinalSuccess produces the post-success summary block.
func (m Model) renderFinalSuccess() string {
	var b strings.Builder
	b.WriteString(m.styleHeaderLabel().Render("Cluster ID:   ") + " " + m.styleHeaderValue().Render(m.clusterID))
	b.WriteString("\n")
	b.WriteString(m.styleHeaderLabel().Render("Group ID:     ") + " " + m.styleHeaderValue().Render(m.clusterGroupID))
	b.WriteString("\n")
	b.WriteString(m.styleHeaderLabel().Render("NVCFBackend:  ") + " " +
		m.styleClusterName().Render(m.nvcfBackendHealth) + " " +
		m.styleSection().Render("(healthy)"))
	b.WriteString("\n")
	b.WriteString(m.styleHeaderLabel().Render("Duration:     ") + " " +
		m.styleHeaderValue().Render(formatDuration(m.finalDuration)))
	return b.String()
}

// renderFinalSuccessCompact produces the compact "<cluster> ready in <dur> ✓" line.
func (m Model) renderFinalSuccessCompact() string {
	return m.styleSection().Render(fmt.Sprintf("%s ready in %s ✓", m.cluster, formatDuration(m.finalDuration)))
}

// effectivePhaseCount returns the number of active phases for the current mode.
// For ModeInstall this is totalPhases (8). For ModeDown this is activePhases.
func (m Model) effectivePhaseCount() int {
	if m.activePhases > 0 {
		return m.activePhases
	}
	return totalPhases
}

// renderFinalFailure produces the structured-failure summary block (REQ-15).
func (m Model) renderFinalFailure() string {
	idx := m.failedPhase - 1
	name := ""
	if idx >= 0 && idx < m.effectivePhaseCount() {
		name = m.phases[idx].name
	}
	tag := fmt.Sprintf("[%s %s]", m.errCategory, m.retryClass)
	if m.retryClass == "backoff" && m.retryAfterSec > 0 {
		tag = fmt.Sprintf("[%s backoff retry=%ds]", m.errCategory, m.retryAfterSec)
	}

	var b strings.Builder
	b.WriteString(m.styleFailMark().Render(glyphFailed) + " " +
		m.styleWhite().Render(fmt.Sprintf("Phase %d: %s failed", m.failedPhase, name)) +
		" " + m.styleMuted().Render(tag))
	b.WriteString("\n    ")
	b.WriteString(m.styleWhite().Render(m.errMessage))

	if len(m.remediation) > 0 {
		b.WriteString("\n\n    ")
		b.WriteString(m.styleSection().Render("Remediation:"))
		b.WriteString("\n")
		for _, line := range m.remediation {
			b.WriteString("        ")
			b.WriteString(m.styleSection().Render("→") + " " + m.styleWhite().Render(line))
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderFinalFailureCompact condenses the failure summary to two lines.
func (m Model) renderFinalFailureCompact() string {
	idx := m.failedPhase - 1
	name := ""
	if idx >= 0 && idx < m.effectivePhaseCount() {
		name = m.phases[idx].name
	}
	first := m.styleFailMark().Render(glyphFailed) + " " +
		m.styleWhite().Render(
			fmt.Sprintf("Phase %d %s failed: [%s] %s", m.failedPhase, name, m.errCategory, m.errMessage),
		)
	if len(m.remediation) == 0 {
		return first
	}
	rem := m.styleMuted().Render("Remediation: " + strings.Join(m.remediation, "; "))
	return first + "\n" + rem
}

// elapsed returns wall-clock elapsed since install start. Returns zero if
// install hasn't started yet.
func (m Model) elapsed(now time.Time) time.Duration {
	if m.started.IsZero() {
		return 0
	}
	d := now.Sub(m.started)
	if d < 0 {
		return 0
	}
	return d
}

// isCancelled reports whether the install was terminated by SIGINT/SIGTERM.
func (m Model) isCancelled() bool {
	return m.cancelReason != ""
}

// ─── status mode (ModeStatus) ─────────────────────────────────────────────────

// prettyComponentName maps raw component identifiers to human-readable labels.
func prettyComponentName(raw string) string {
	names := map[string]string{
		"sis":           "SIS",
		"nats":          "NATS",
		"cassandra":     "Cassandra",
		"openbao":       "OpenBao",
		"api-keys":      "API Keys",
		"nvcf-api":      "NVCF API",
		"reval":         "Reval",
		"gateway":       "Gateway",
		"nvca-operator": "NVCA Operator",
		"nvca-worker":   "NVCA Worker",
	}
	if n, ok := names[raw]; ok {
		return n
	}
	// Title-case the raw name as a fallback.
	if len(raw) == 0 {
		return raw
	}
	return strings.ToUpper(raw[:1]) + raw[1:]
}

// commonRemediation is a small embedded table of suggested commands keyed by
// component name (raw identifier).
var commonRemediation = map[string][]string{
	"cassandra": {
		"kubectl describe pod -n cassandra-system cassandra-0",
		"kubectl logs -n cassandra-system cassandra-0",
		"See https://docs.nvidia.com/nvcf/self-hosted/troubleshooting#cassandra",
	},
}

// formatUptime converts an uptime in seconds to a human-readable string like
// "7d", "14d", "3h", or "45m".
func formatUptime(sec int) string {
	d := time.Duration(sec) * time.Second
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", sec)
}

// formatAgeShort formats a duration as "02m41s" or "12m38s" for recent-events.
func formatAgeShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	mins := int(d.Minutes())
	secs := int(d.Seconds()) - mins*60
	if secs < 0 {
		secs = 0
	}
	return fmt.Sprintf("%02dm%02ds", mins, secs)
}

// formatInstalledAt formats InstalledAt for the status header as "2026-04-25 14:02 UTC".
func formatInstalledAt(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// statusVerdict returns verdict glyph and text for the status header line.
// AsciiOnly does NOT strip the unicode glyphs on the verdict line — the spec's
// §6.5.1 static mock uses bare ✓/‼/✘/? without brackets, and those are the
// same in both TTY and ASCII-only renders (AsciiOnly only strips ANSI color
// codes, not unicode characters). This matches the install-mode convention
// where [✓]/[▶]/[ ] glyphs survive AsciiOnly unchanged.
func statusVerdict(verdict string, notReadyCount int) string {
	switch verdict {
	case "healthy":
		return "✓ healthy"
	case "degraded":
		comp := "component"
		if notReadyCount != 1 {
			comp = "components"
		}
		return fmt.Sprintf("‼ degraded — %d %s not ready", notReadyCount, comp)
	case "failed":
		return "✘ failed"
	default:
		return "? unknown"
	}
}

// componentGlyph returns the bracketed glyph for a component health row.
// Matches the install-mode convention: bracket + unicode, unchanged in
// ASCII-only mode (AsciiOnly only strips ANSI color codes).
func componentGlyph(healthy bool) string {
	if healthy {
		return "[✓]"
	}
	return "[✘]"
}

// viewStatus renders the ModeStatus dashboard per §6.5.1 / §6.5.2.
func (m Model) viewStatus(now time.Time) string {
	var b strings.Builder

	// ── header ───────────────────────────────────────────────────────────────
	clusterHeader := "NVCF self-hosted: " + m.cluster

	var snap *Snapshot
	if m.snapshot != nil {
		snap = m.snapshot
	}

	// Count not-ready components for degraded verdict text.
	notReadyCount := 0
	for _, c := range m.components {
		if !c.Healthy {
			notReadyCount++
		}
	}

	verdict := "unknown"
	reconcileAge := 0
	target := m.target
	stack := m.stack
	var installedAt time.Time
	if snap != nil {
		verdict = snap.Verdict
		reconcileAge = snap.ReconcileAgeSec
		if snap.Identity.Target != "" {
			target = snap.Identity.Target
		}
		if snap.Identity.StackVersion != "" || snap.Identity.StackDigest != "" {
			stack = snap.Identity.StackVersion + "@" + snap.Identity.StackDigest
		}
		installedAt = snap.Identity.InstalledAt
	}

	verdictStr := statusVerdict(verdict, notReadyCount)
	reconcileAgeStr := fmt.Sprintf("%ds ago", reconcileAge)
	installedStr := formatInstalledAt(installedAt)

	// Two-column header layout matching §6.5.1 / §6.5.2.
	// Both header rows (Status/Target) share the same right-column anchor so
	// the right-side labels ("Last reconcile:" / "Installed:") are vertically
	// aligned. The anchor is max(43, len(longestLeft)+4) where +4 is the
	// minimum separation between left and right content.
	b.WriteString(clusterHeader)
	b.WriteString("\n")

	statusLine := "Status: " + verdictStr
	targetLine := "Target: " + target
	rightReconcile := "Last reconcile: " + reconcileAgeStr
	rightInstalled := "Installed: " + installedStr

	const headerMinWidth = 43
	const headerMinSpaces = 4
	statusLen := len([]rune(statusLine))
	targetLen := len([]rune(targetLine))
	maxLeftLen := statusLen
	if targetLen > maxLeftLen {
		maxLeftLen = targetLen
	}
	colAnchor := maxLeftLen + headerMinSpaces
	if colAnchor < headerMinWidth {
		colAnchor = headerMinWidth
	}

	b.WriteString(twoColLine(statusLine, rightReconcile, colAnchor))
	b.WriteString("\n")
	b.WriteString(twoColLine(targetLine, rightInstalled, colAnchor))
	b.WriteString("\n")

	// "Stack:  {stack}"
	b.WriteString("Stack:  " + stack)
	b.WriteString("\n")

	// ── components panel ─────────────────────────────────────────────────────
	b.WriteString("\n")
	b.WriteString(m.styleSection().Render("Components"))
	b.WriteString("\n")
	if len(m.components) > 0 {
		b.WriteString("\n")
		for _, c := range m.components {
			glyph := componentGlyph(c.Healthy)
			name := prettyComponentName(c.Name)
			if c.Cluster != "" {
				name = name + " (" + c.Cluster + ")"
			}
			readyCounts := fmt.Sprintf("%d/%d", c.Ready, c.Total)

			// Component row target: ready-counts land at display column 33.
			// Prefix = "   " (3) + "[✓]" (3 display) + " " (1) = 7 display cols.
			// Name field display width = 33 - 7 - 1(separator) = 25 display cols.
			// Since component names are always ASCII, byte width == display width,
			// so %-25s gives correct alignment regardless of the glyph's byte size.
			const nameFieldWidth = 25
			// Uptime is only shown in healthy-verdict snapshots (§6.5.2: healthy
			// components in a degraded/failed snapshot do NOT show uptime).
			showUptime := snap != nil && snap.Verdict == "healthy"
			if c.Healthy {
				if showUptime {
					uptime := formatUptime(c.UptimeSec)
					// Format: "   [✓] {name:<25} {N/N} ready    {uptime:>3} uptime"
					// 4 spaces between "ready" and uptime field (right-aligned to 3).
					b.WriteString(fmt.Sprintf("   %s %-*s %s ready    %3s uptime\n",
						glyph, nameFieldWidth, name, readyCounts, uptime))
				} else {
					b.WriteString(fmt.Sprintf("   %s %-*s %s ready\n",
						glyph, nameFieldWidth, name, readyCounts))
				}
			} else {
				// not-ready: show message, omit uptime
				msgSuffix := ""
				if c.Message != "" {
					msgSuffix = "    " + c.Message
				}
				b.WriteString(fmt.Sprintf("   %s %-*s %s not ready%s\n",
					glyph, nameFieldWidth, name, readyCounts, msgSuffix))
			}
		}
	}

	// ── compute clusters panel ────────────────────────────────────────────────
	if len(m.clusters) > 0 {
		b.WriteString("\n")
		b.WriteString(m.styleSection().Render("Compute Clusters"))
		b.WriteString("\n\n")
		for _, cl := range m.clusters {
			glyph := componentGlyph(cl.Healthy)
			lastSeen := fmt.Sprintf("%ds ago", cl.LastSeenAgeSec)
			// Format mirrors §6.5.1: name (%-15s), GPU ×count, active-deploy count, "last seen".
			// Display col for H100 = 23: "   " (3) + "[✓]" (3 display) + " " (1) + 15-char field + " " (1) = 23.
			// %-15s gives "yotta-east-1" (12) + 3 spaces = 15. OK.
			// %2d active-deployments count starts at display col 38 (3 spaces after ×count field).
			b.WriteString(fmt.Sprintf("   %s %-15s %s ×%-6d   %2d active deployments    last seen %s\n",
				glyph, cl.Name, cl.GPU, cl.GPUCount, cl.ActiveDeployments, lastSeen))
		}
	}

	// ── recent events panel ────────────────────────────────────────────────────
	if len(m.recentEvents) > 0 {
		b.WriteString("\n")
		b.WriteString(m.styleSection().Render("Recent events  (5m window)"))
		b.WriteString("\n\n")
		for _, ev := range m.recentEvents {
			age := formatAgeShort(time.Duration(ev.AgeSec) * time.Second)
			var glyph string
			var line string
			switch ev.Kind {
			case "function-deploy":
				glyph = "[Δ]"
				humanKind := "function deploy"
				statusPart := ""
				if ev.Status != "" {
					statusPart = " " + ev.Status
				}
				versionPart := ""
				if ev.Version != "" {
					versionPart = " (" + ev.Version + ")"
				}
				line = fmt.Sprintf("   %s %s ago    %-29s%s%s",
					glyph, age, humanKind+statusPart, ev.Name, versionPart)
			case "cluster-registered":
				glyph = "[+]"
				humanKind := "cluster registered"
				line = fmt.Sprintf("   %s %s ago    %-29s%s",
					glyph, age, humanKind, ev.Name)
			case "cluster-deregistered":
				glyph = "[-]"
				humanKind := "cluster deregistered"
				line = fmt.Sprintf("   %s %s ago    %-29s%s",
					glyph, age, humanKind, ev.Name)
			default:
				// Warning events (readiness-lost, node-pressure, etc.):
				// format: "   [!] <age> ago    <Name> <human-kind>   <Status>"
				// human-kind: replace hyphens with spaces.
				glyph = "[!]"
				humanKind := strings.ReplaceAll(ev.Kind, "-", " ")
				statusPart := ""
				if ev.Status != "" {
					statusPart = "   " + ev.Status
				}
				line = fmt.Sprintf("   %s %s ago    %s %s%s",
					glyph, age, ev.Name, humanKind, statusPart)
			}
			b.WriteString(line + "\n")
		}
	}

	// ── recommended actions ────────────────────────────────────────────────────
	if verdict == "degraded" || verdict == "failed" {
		// Collect remediation commands from not-ready components.
		var remediations []string
		seen := map[string]bool{}
		for _, c := range m.components {
			if !c.Healthy {
				if steps, ok := commonRemediation[c.Name]; ok {
					for _, step := range steps {
						if !seen[step] {
							seen[step] = true
							remediations = append(remediations, step)
						}
					}
				}
			}
		}
		if len(remediations) > 0 {
			b.WriteString("\n")
			b.WriteString(m.styleSection().Render("Recommended actions"))
			b.WriteString("\n\n")
			for _, step := range remediations {
				b.WriteString("   →  " + step + "\n")
			}
		}
	}

	// ── footer ────────────────────────────────────────────────────────────────
	// TODO(M+8.7): wire `w` keybinding in Update to toggle watch mode when
	// the status command lands; the affordance is advertised per §6.5.1.
	b.WriteString("\npress q to quit · w to toggle watch mode\n")

	return b.String()
}

// ─── check mode (ModeCheck) ───────────────────────────────────────────────────

// humanCategoryName maps raw category identifiers to human-readable labels.
func humanCategoryName(raw string) string {
	names := map[string]string{
		"local-host-tools":     "Local host",
		"kubernetes-api":       "Kubernetes API",
		"pre-kubernetes-setup": "Cluster setup",
		"cluster":              "Cluster",
		"control-plane":        "Control plane",
		"compute-plane":        "Compute plane",
	}
	if n, ok := names[raw]; ok {
		return n
	}
	return raw
}

// checkGlyph returns the bracketed glyph for a check row. Matches install-mode
// convention: bracket + unicode, unchanged in ASCII-only mode (AsciiOnly only
// strips ANSI color codes, not unicode characters).
func checkGlyph(row checkRow) string {
	switch {
	case row.finished && row.passed:
		return "[✓]"
	case row.finished && !row.passed:
		return "[✘]"
	case row.started && !row.finished:
		return "[▶]"
	default:
		return "[ ]"
	}
}

// viewCheck renders the ModeCheck dashboard per §6.6.1.
func (m Model) viewCheck(now time.Time) string {
	var b strings.Builder

	b.WriteString("Pre-flight checks for NVCF self-hosted install\n")

	for _, cat := range m.checkCategories {
		b.WriteString("\n")
		b.WriteString(m.styleSection().Render(humanCategoryName(cat.name)))
		b.WriteString("\n\n")
		for _, row := range cat.checks {
			glyph := checkGlyph(row)
			var label string
			if row.finished {
				// Completed rows: use the completion message, fall back to ID.
				if row.message != "" {
					label = row.message
				} else {
					label = row.id
				}
			} else {
				// In-flight / pending rows: prefer the start-time human label,
				// fall back to the check ID.
				if row.startMsg != "" {
					label = row.startMsg
				} else {
					label = row.id
				}
			}
			detail := ""
			if row.detail != "" {
				detail = " (" + row.detail + ")"
			}
			b.WriteString(fmt.Sprintf("   %s %s%s\n", glyph, label, detail))
		}
		if len(cat.checks) == 0 {
			// Category header seen but no checks emitted yet.
			b.WriteString("   ...\n")
		}
	}

	// ── status line ───────────────────────────────────────────────────────────
	passed, failed, total := m.checkTally()
	if m.finished && m.checkFinalTotal > 0 {
		passed = m.checkFinalPassed
		failed = m.checkFinalFailed
		total = m.checkFinalTotal
	}
	if total < m.totalChecks {
		total = m.totalChecks
	}

	anyInFlight := false
	for _, cat := range m.checkCategories {
		for _, row := range cat.checks {
			if row.started && !row.finished {
				anyInFlight = true
				break
			}
		}
		if anyInFlight {
			break
		}
	}

	allFinished := !anyInFlight && m.finished
	var statusLine string
	switch {
	case anyInFlight || (!m.finished && len(m.checkCategories) > 0 && !allFinished):
		statusLine = fmt.Sprintf("Status: in progress  (%d/%d passed, 0 failed)", passed, total)
	case m.finished && failed > 0:
		statusLine = fmt.Sprintf("Status: ✘ failed  (%d/%d passed, %d failed)", passed, total, failed)
	case m.finished && failed == 0:
		statusLine = fmt.Sprintf("Status: ✓ ok  (%d/%d passed, 0 failed)", passed, total)
	default:
		statusLine = fmt.Sprintf("Status: in progress  (%d/%d passed, 0 failed)", passed, total)
	}

	b.WriteString("\n")
	b.WriteString(statusLine)
	b.WriteString("\n")

	return b.String()
}

// checkTally returns (passed, failed, total) counts from all accumulated check rows.
func (m Model) checkTally() (passed, failed, total int) {
	for _, cat := range m.checkCategories {
		for _, row := range cat.checks {
			total++
			if row.finished {
				if row.passed {
					passed++
				} else {
					failed++
				}
			}
		}
	}
	return passed, failed, total
}

// ─── style helpers ────────────────────────────────────────────────────────────

func (m Model) styleHeaderLabel() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvLightGrayHex))
}
func (m Model) styleHeaderValue() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvWhiteHex)).Bold(true)
}
func (m Model) styleClusterName() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvGreenHex)).Bold(true)
}
func (m Model) styleSection() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvGreenHex)).Bold(true)
}
func (m Model) styleDoneMark() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvGreenDimHex))
}
func (m Model) styleRunMark() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvGreenHex)).Bold(true)
}
func (m Model) styleFailMark() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvRedHex)).Bold(true)
}
func (m Model) stylePendMark() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvLightGrayHex))
}
func (m Model) styleMuted() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvLightGrayHex))
}
func (m Model) styleWhite() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvWhiteHex))
}
func (m Model) styleRunDuration() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvGreenHex)).Bold(true)
}
func (m Model) styleResourceName() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvWhiteHex))
}
func (m Model) styleResourceLabel() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvGreenDimHex))
}
func (m Model) styleWaitingTag() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvAmberHex)).Bold(true)
}
func (m Model) styleProgressTag() lipgloss.Style {
	return m.renderer.NewStyle().Foreground(lipgloss.Color(nvLightGrayHex))
}

// ─── small utilities ──────────────────────────────────────────────────────────

// twoColLine formats a two-column line where left is padded to at least
// minWidth *rune* characters before right is appended. If left is longer than
// minWidth runes, 4 spaces of minimum separation are used instead.
//
// All unicode glyphs used in the left column (✓ ‼ ✘ ? —) are single display
// column, so rune count equals display width.
func twoColLine(left, right string, minWidth int) string {
	const minSpaces = 4
	runeLen := len([]rune(left))
	pad := minWidth - runeLen
	if pad < minSpaces {
		pad = minSpaces
	}
	return left + strings.Repeat(" ", pad) + right
}

// formatDuration renders a duration as either "NNs" or "NNmNNs".
// Negative durations clamp to zero.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%02ds", int(d.Seconds()))
	}
	mins := int(d.Minutes())
	secs := int(d.Seconds()) - mins*60
	return fmt.Sprintf("%02dm%02ds", mins, secs)
}

// padRight right-pads s with spaces up to width n. Operates on bytes; safe for
// ASCII resource names (the only callers here).
func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// resourceLabelFor returns the verb shown after a sub-progress count
// (e.g. "Deployments  9/14  available"). Falls back to "ready" for unknown
// resources so the layout is robust to new resource kinds added later.
func resourceLabelFor(resource string) string {
	switch resource {
	case "Namespaces":
		return "ready"
	case "CRDs":
		return "applied"
	case "Deployments":
		return "available"
	case "StatefulSets":
		return "ready"
	case "Jobs":
		return "complete"
	default:
		return "ready"
	}
}

// ─── EventSink wrapper ────────────────────────────────────────────────────────

// TTYRenderer wraps a tea.Program and forwards Emit calls into it. It satisfies
// EventSink.
//
// Lifecycle: NewTTYRenderer constructs the Program but does NOT start it. The
// orchestrator must call Start() before any Emit; Emit returns an error if
// invoked early so callers can't deadlock on tea.Program.Send (which blocks
// indefinitely against an unstarted program). Start is idempotent. Close is
// safe to call before Start (no-op) so deferred cleanup on construction-error
// paths doesn't deadlock either.
type TTYRenderer struct {
	program *tea.Program
	started atomic.Bool
	runErr  error
}

// NewTTYRenderer constructs a TTYRenderer drawing to stderr. The same stderr
// writer is threaded into ModelOpts.Output so the lipgloss.Renderer can
// detect color capability from the actual TTY (not io.Discard).
func NewTTYRenderer(stderr io.Writer, opts ModelOpts) *TTYRenderer {
	if opts.Output == nil {
		opts.Output = stderr
	}
	m := NewModel(opts)
	prog := tea.NewProgram(m,
		tea.WithOutput(stderr),
		tea.WithoutSignalHandler(), // orchestrator owns SIGINT
		// Alt-screen so each redraw replaces the prior frame instead of
		// appending. Without this, ssh/long-running renders stack the
		// dashboard vertically (iter #10 from dev-VM E2E).
		tea.WithAltScreen(),
	)
	return &TTYRenderer{
		program: prog,
	}
}

// Start launches the bubbletea Run loop in a goroutine. Idempotent — calling
// Start twice is a no-op. The runtime error from Run() is captured into
// runErr and surfaced by Close().
func (r *TTYRenderer) Start() {
	if r.started.Swap(true) {
		return
	}
	go func() {
		_, err := r.program.Run()
		r.runErr = err
	}()
}

// Emit forwards the event into the running Program's message queue.
// Returns an error if called before Start — tea.Program.Send blocks
// indefinitely on an unstarted program, so failing fast is more honest
// than letting the caller deadlock.
func (r *TTYRenderer) Emit(_ context.Context, e Event) error {
	if !r.started.Load() {
		return errors.New("progress.TTYRenderer: Emit called before Start")
	}
	r.program.Send(eventMsg{e})
	return nil
}

// Close requests Program shutdown and waits for the Run loop to return.
// Safe to call before Start (no-op) so deferred Close() on error paths
// doesn't deadlock. After Wait returns, runErr is safe to read — bubbletea
// guarantees happens-before from Run completion to Wait return.
func (r *TTYRenderer) Close() error {
	if !r.started.Load() {
		return nil
	}
	r.program.Quit()
	r.program.Wait()
	return r.runErr
}
