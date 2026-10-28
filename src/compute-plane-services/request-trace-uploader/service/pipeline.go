// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/batch"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/format"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/cleanup"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/retry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform"
)

const (
	// defaultLane names the journal destination of a backend that declares
	// none. The journal needs a non-empty name; the backend sees "".
	defaultLane = "default"
	// strandedPollInterval paces polls of a job past its max age.
	strandedPollInterval = 30 * time.Minute
	// Bounded reason codes recorded in the journal and on counters.
	reasonTooLarge = "too_large"
	reasonRejected = "rejected"
	reasonRetained = "retained"
)

// retainedUntil parks a retained batch: it is never due, so only an
// operator moves it.
var retainedUntil = time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)

// fileIdentity tells one spool file from a later one that reuses its name.
type fileIdentity struct {
	name    string
	size    int64
	modTime time.Time
}

func laneOf(destination backend.Destination) string {
	if destination == "" {
		return defaultLane
	}
	return string(destination)
}

func (u *Uploader) destinationOf(lane string) backend.Destination {
	if lane == defaultLane && len(u.caps.Destinations) == 0 {
		return ""
	}
	return backend.Destination(lane)
}

// Pass runs one full round: it reads and prepares every new closed segment,
// batches and submits what is prepared, sends again what is due, polls every
// open job, and cleans up what is confirmed. The first call also runs the
// startup steps Run runs, so a test can drive the pipeline one pass at a
// time.
func (u *Uploader) Pass(ctx context.Context) error {
	return u.pass(ctx, true)
}

// scan is the pass Run repeats on the scan interval. It does not poll: Run
// polls on the status interval, so a short scan interval never shortens it.
func (u *Uploader) scan(ctx context.Context) error {
	return u.pass(ctx, false)
}

func (u *Uploader) pass(ctx context.Context, poll bool) error {
	if err := u.start(ctx); err != nil {
		return err
	}
	var errs []error
	if !u.draining.Load() {
		errs = append(errs, u.locked(&u.intakeMu, func() error { return u.intake(ctx, false) }))
	}
	errs = append(errs, u.locked(&u.submitMu, func() error { return errors.Join(u.submitPrepared(ctx), u.resend(ctx)) }))
	if poll {
		errs = append(errs, u.locked(&u.pollMu, func() error { return u.pollRound(ctx) }))
	}
	errs = append(errs, u.locked(&u.cleanupMu, func() error { return u.cleanupRound(ctx) }))
	u.housekeeping()
	return errors.Join(errs...)
}

func (u *Uploader) locked(mu *sync.Mutex, run func() error) error {
	mu.Lock()
	defer mu.Unlock()
	return run()
}

// Poll polls every open job once and cleans up what is confirmed. Run calls
// it on the status interval, apart from passes; it takes no lock a submit
// holds, so a slow submit or a hold never delays a confirmation.
func (u *Uploader) Poll(ctx context.Context) error {
	if err := u.start(ctx); err != nil {
		return err
	}
	return errors.Join(
		u.locked(&u.pollMu, func() error { return u.pollRound(ctx) }),
		u.locked(&u.cleanupMu, func() error { return u.cleanupRound(ctx) }),
	)
}

// drain scans the spool one last time, so a segment that closed after the
// last scan is not left behind, submits what is prepared, polls once more
// and cleans up. ctx carries the drain timeout; what it cuts short stays in
// the journal and on the spool for the next start.
func (u *Uploader) drain(ctx context.Context) error {
	return errors.Join(
		u.locked(&u.intakeMu, func() error { return u.intake(ctx, true) }),
		u.locked(&u.submitMu, func() error { return u.submitPrepared(ctx) }),
		u.locked(&u.pollMu, func() error { return u.pollRound(ctx) }),
		u.locked(&u.cleanupMu, func() error { return u.cleanupRound(ctx) }),
	)
}

// intake reads every closed segment the journal does not know yet. Only one
// intake runs at a time; it shares nothing with polling but the journal. A
// scan stops early once draining starts, except the final one, which the
// drain itself runs.
func (u *Uploader) intake(ctx context.Context, final bool) error {
	scan, err := segment.ScanDir(u.cfg.SourceDir, u.cfg.Prefixes())
	if err != nil {
		return fmt.Errorf("scan the spool: %w", err)
	}
	u.forgetGone(scan)
	var errs []error
	for _, item := range scan.Closed {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !final && u.draining.Load() {
			break
		}
		// Checked per segment, so a large bad backlog stops at the trip
		// instead of being quarantined wholesale.
		if u.policy.Breaker().Tripped() {
			slog.Warn("too many segments quarantined in a row; not reading new segments until restart")
			break
		}
		if err := u.take(ctx, item); err != nil {
			slog.Error("prepare request trace segment", "segment", item.Index, "error", safeError(err))
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (u *Uploader) take(ctx context.Context, item segment.Segment) error {
	sum, err := u.fingerprint(item)
	if errors.Is(err, fs.ErrNotExist) {
		// Cleanup removed it after the scan listed it.
		return nil
	}
	if err != nil {
		return err
	}
	seg, _, err := u.store.Discover(journal.SegmentInfo{
		Name:    filepath.Base(item.Path),
		Prefix:  item.Prefix,
		Index:   item.Index,
		Size:    item.Size,
		ModTime: item.ModTime,
		SHA256:  sum,
	})
	if err != nil {
		return err
	}
	switch seg.State {
	case journal.StateDiscovered, journal.StatePreparing:
		return u.prepare(ctx, item, seg)
	case journal.StateQuarantined:
		// A crash between the journal record and the move left the source
		// on the spool. Admit skips what is already moved.
		return u.quarantine.Admit(seg.ID, item.Path)
	}
	return nil
}

// fingerprint returns the hex sha256 of a closed segment, reading it only
// the first time its name, size and modification time are seen.
func (u *Uploader) fingerprint(item segment.Segment) (string, error) {
	key := fileIdentity{name: filepath.Base(item.Path), size: item.Size, modTime: item.ModTime}
	if sum, ok := u.fingerprints[key]; ok {
		return sum, nil
	}
	file, err := os.Open(item.Path)
	if err != nil {
		return "", fmt.Errorf("open segment: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("fingerprint segment: %w", err)
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	u.fingerprints[key] = sum
	return sum, nil
}

func (u *Uploader) forgetGone(scan segment.Scan) {
	present := map[fileIdentity]bool{}
	for _, item := range append(append([]segment.Segment(nil), scan.Closed...), scan.Active...) {
		present[fileIdentity{name: filepath.Base(item.Path), size: item.Size, modTime: item.ModTime}] = true
	}
	for key := range u.fingerprints {
		if !present[key] {
			delete(u.fingerprints, key)
		}
	}
}

// prepare reads one segment into the backend's transport format and records
// the result. A segment found Preparing is prepared again from the start:
// its earlier writer was aborted.
func (u *Uploader) prepare(ctx context.Context, item segment.Segment, seg journal.Segment) error {
	if seg.State == journal.StateDiscovered {
		if err := u.store.BeginPrepare(seg.ID); err != nil {
			return err
		}
	}
	writer, err := u.factory.NewWriter(format.Target{
		Segment:      item,
		SegmentID:    seg.ID,
		Dir:          u.store.TransportDir(),
		Destinations: u.caps.AllDestinations(),
	})
	if err != nil {
		return fmt.Errorf("open format writer: %w", err)
	}
	rejects := transform.NewRejectFile(u.quarantine.RejectsDir())
	result, err := u.preparer.Prepare(ctx, item.Path, writer, rejects)
	if err != nil {
		return errors.Join(fmt.Errorf("prepare segment: %w", err), rejects.Discard())
	}
	outputs := writer.Outputs()
	if hex.EncodeToString(result.Fingerprint) != seg.SHA256 {
		// The file changed after it was fingerprinted, so nothing read here
		// belongs to this journal entry.
		for _, output := range outputs {
			_ = removeFile(output.Path)
		}
		return errors.Join(fmt.Errorf("segment %d changed while it was read", item.Index), rejects.Discard())
	}
	if _, err := rejects.Commit(seg.ID); err != nil {
		// The rejected lines exist only in the source until the artifact is
		// kept, so the segment is held and prepared again later.
		for _, output := range outputs {
			_ = removeFile(output.Path)
		}
		return fmt.Errorf("keep rejected lines: %w", err)
	}
	u.recordPrepared(result)
	u.recordRead(item, result)

	switch {
	case result.Quarantine != "":
		return u.quarantineSegment(seg.ID, item.Index, result.Quarantine, item.Path)
	case len(outputs) == 0:
		return u.store.MarkEmpty(seg.ID)
	}
	transports := make(map[string]journal.Transport, len(outputs))
	for _, output := range outputs {
		transports[laneOf(output.Destination)] = journal.Transport{Path: output.Path, SHA256: output.SHA256, Rows: output.Rows, Bytes: output.Bytes, Policy: u.prepPolicy}
	}
	if err := u.store.MarkPrepared(seg.ID, u.name, transports); err != nil {
		return err
	}
	u.point(PointPrepared)
	slog.Info("prepared request trace segment", "segment", item.Index, "records", result.Written)
	return nil
}

// recordPolicy fingerprints everything that decides what a transport holds:
// the read bounds, suppression rules and headerless policy, redaction, and
// the format. Only the hash is kept or logged, never the configured values.
func recordPolicy(cfg transform.Config, f backend.Format) (string, error) {
	raw, err := json.Marshal(struct {
		Transform transform.Config
		Format    backend.Format
	}{cfg, f})
	if err != nil {
		return "", fmt.Errorf("fingerprint the record policy: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16]), nil
}

// reprepareStale discards unsent transports written under a record policy
// other than the one now configured, so newly enabled suppression or
// redaction applies to them. Their segments are prepared again from the
// source, which is still on the spool. A batch whose submits were only ever
// refused is released first, since nothing in it was sent. Transports in a
// batch that may have landed are left alone.
func (u *Uploader) reprepareStale() error {
	var errs []error
	for _, b := range u.store.Batches() {
		if b.State != journal.StateRetryWait || b.Receipt != nil || b.MayHaveLanded || b.Backend != u.name || !u.holdsStale(b) {
			continue
		}
		if err := u.store.ReleaseBatch(b.ID); err != nil {
			errs = append(errs, err)
		}
	}
	discarded := 0
	for _, seg := range u.store.Segments() {
		if seg.State != journal.StatePrepared || seg.Backend != u.name {
			continue
		}
		stale, batched := false, false
		for _, dest := range seg.Destinations {
			stale = stale || dest.Transport.Policy != u.prepPolicy
			batched = batched || dest.BatchID != ""
		}
		if !stale || batched {
			continue
		}
		if err := u.store.Reprepare(seg.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		discarded++
	}
	if discarded > 0 {
		slog.Info("record policy changed; unsent segments are prepared again", "segments", discarded)
	}
	return errors.Join(errs...)
}

// holdsStale reports whether a member of the batch was prepared under a
// record policy other than the one now configured.
func (u *Uploader) holdsStale(b journal.Batch) bool {
	for _, m := range b.Members {
		seg, ok := u.store.Segment(m.SegmentID)
		if !ok {
			continue
		}
		if dest, ok := seg.Destinations[b.Destination]; ok && dest.Transport.Policy != u.prepPolicy {
			return true
		}
	}
	return false
}

// quarantineSegment records a quarantine and then moves the files, so a crash
// in between is finished by the next pass.
func (u *Uploader) quarantineSegment(id string, index int, reason string, paths ...string) error {
	if err := u.store.Quarantine(id, reason); err != nil {
		return err
	}
	if u.policy.Breaker().Quarantined() {
		u.metrics.Add(telemetry.QuarantineTripped, 1, nil)
	}
	slog.Warn("quarantined request trace segment", "segment", index, "reason", reason)
	return u.quarantine.Admit(id, paths...)
}

// submitPrepared batches every prepared destination that is in no batch and
// submits the batches. Lanes are flushed at the end of each call, so a
// segment waits at most one scan interval, as with the earlier exporter.
func (u *Uploader) submitPrepared(ctx context.Context) error {
	now := time.Now()
	var batches []batch.Batch
	var errs []error
	for _, seg := range u.store.Segments() {
		if seg.State != journal.StatePrepared && seg.State != journal.StateSubmitting {
			continue
		}
		for lane, dest := range seg.Destinations {
			if dest.State != journal.StatePrepared || dest.BatchID != "" {
				continue
			}
			preparedAt := seg.PreparedAt
			if preparedAt.IsZero() {
				preparedAt = now
			}
			member := batch.Member{
				SegmentID:   seg.ID,
				Destination: lane,
				File:        batch.File{Path: dest.Transport.Path, SHA256: dest.Transport.SHA256, Bytes: dest.Transport.Bytes, Rows: dest.Transport.Rows},
				PreparedAt:  preparedAt,
			}
			if u.isSolo(seg.ID) {
				solo, err := u.batcher.Solo(member, now)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				batches = append(batches, solo)
				continue
			}
			closed, err := u.batcher.Add(member, now)
			if errors.Is(err, batch.ErrMemberTooLarge) {
				errs = append(errs, u.quarantineSegment(seg.ID, seg.Index, reasonTooLarge, filepath.Join(u.cfg.SourceDir, seg.Name), dest.Transport.Path))
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			batches = append(batches, closed...)
		}
	}
	batches = append(batches, u.batcher.Due(now.Add(batch.DefaultMaxWait))...)
	for _, b := range batches {
		if err := u.submit(ctx, b, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resend submits again every batch whose retry is due, and every batch whose
// earlier submit may have committed without a job id; the second kind can
// land twice and is counted. When ambiguous submits are retained, such a
// batch is parked for an operator instead, as a failed submit would be.
func (u *Uploader) resend(ctx context.Context) error {
	now := time.Now()
	var errs []error
	for _, b := range u.store.Batches() {
		switch {
		case b.State == journal.StateRetryWait && !now.Before(b.NextRetryAt):
		case b.State == journal.StateAmbiguous && b.Receipt == nil && u.retainsAmbiguous():
			// Recovery counted it when it found the submit unfinished.
			slog.Warn("submit retained for an operator", "batch", shortID(b.ID))
			if err := u.store.RetryBatch(b.ID, retainedUntil, reasonRetained); err != nil {
				errs = append(errs, err)
			}
			continue
		case b.State == journal.StateAmbiguous && b.Receipt == nil:
			u.metrics.Add(telemetry.SubmitsAmbiguous, 1, nil)
		default:
			continue
		}
		if err := u.submit(ctx, toBatch(b), true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// retainsAmbiguous reports whether a submit that may have landed without a
// job id waits for an operator instead of being sent again.
func (u *Uploader) retainsAmbiguous() bool {
	return u.faults.AmbiguousWithoutID == retry.AmbiguousRetain && !u.caps.ResubmitSafe
}

func toBatch(b journal.Batch) batch.Batch {
	out := batch.Batch{ID: b.ID, Destination: b.Destination, ClosedAt: b.CreatedAt}
	for _, m := range b.Members {
		out.Members = append(out.Members, batch.Member{
			SegmentID:   m.SegmentID,
			Destination: b.Destination,
			File:        batch.File{Path: m.Path, SHA256: m.SHA256, Bytes: m.Bytes, Rows: m.Rows},
			PreparedAt:  b.CreatedAt,
		})
		out.Bytes += m.Bytes
	}
	return out
}

// submit waits for the create budget and any hold, records the batch (a new
// one) or moves it back to Submitting (a resend), sends it, and saves the job
// id before it looks at the error.
func (u *Uploader) submit(ctx context.Context, b batch.Batch, resend bool) error {
	if err := u.policy.WaitSubmit(ctx); err != nil {
		return err
	}
	j := batchJournal{store: u.store, point: u.point, exports: u.caps.Exports}
	if resend {
		if err := u.store.ResumeBatch(b.ID); err != nil {
			return err
		}
	} else if err := j.OpenBatch(b, u.name); err != nil {
		// A batch recorded with only some members pointing at it must
		// never be sent: a member that does not point at it would be
		// batched and sent again. Release it; nothing was sent.
		if _, recorded := u.store.Batch(b.ID); recorded {
			err = errors.Join(err, u.store.ReleaseBatch(b.ID))
		}
		return fmt.Errorf("record batch before submit: %w", err)
	}
	receipt, err := batch.Send(ctx, j, u.name, b, u.send)
	if receipt != "" {
		if errors.Is(err, batch.ErrReceiptNotSaved) {
			// Keep the id, so a later round saves it instead of
			// submitting again.
			u.stateMu.Lock()
			u.unsaved[b.ID] = receipt
			u.stateMu.Unlock()
			return err
		}
		if err != nil {
			slog.Warn("create returned a job id and an error; the job is polled", "batch", shortID(b.ID), "fault", faultText(backend.Classify(u.client, err)))
		}
		slog.Info("submitted request trace batch", "batch", shortID(b.ID), "segments", len(b.Members), "bytes", b.Bytes)
		return nil
	}
	if err == nil {
		err = errors.New("backend returned no job id")
	}
	return u.submitFailed(b, err)
}

func (u *Uploader) send(ctx context.Context, b batch.Batch) (string, error) {
	request := backend.SubmitRequest{Destination: u.destinationOf(b.Destination)}
	for _, m := range b.Members {
		request.Files = append(request.Files, backend.File{Path: m.File.Path, Bytes: m.File.Bytes, Rows: m.File.Rows})
	}
	if len(b.Members) == 1 {
		request.Path = b.Members[0].File.Path
		if seg, ok := u.store.Segment(b.Members[0].SegmentID); ok {
			request.Segment = segment.Segment{
				Path:    filepath.Join(u.cfg.SourceDir, seg.Name),
				Prefix:  seg.Prefix,
				Index:   seg.Index,
				Size:    seg.Size,
				ModTime: seg.ModTime,
			}
		}
	}
	if err := u.caps.Check(request); err != nil {
		return "", err
	}
	id, err := u.client.Submit(ctx, request)
	u.point(PointSubmitted)
	return id, err
}

// submitFailed applies the fault policy to a submit that created no job.
func (u *Uploader) submitFailed(b batch.Batch, err error) error {
	now := time.Now()
	stored, _ := u.store.Batch(b.ID)
	fault := backend.Classify(u.client, err)
	u.countAuth(fault)
	// The journal counted this attempt when the batch was recorded or
	// resumed, so it is not added again.
	attempts := max(stored.Attempts, u.countFailure(stored.Members))
	decision := u.policy.Failed(now, u.caps, retry.Failure{Key: failureKey(stored.Members), Stage: retry.StageSubmit, Fault: fault, Attempts: attempts})
	class := errorClass(fault)
	slog.Warn("submit failed", "batch", shortID(b.ID), "segments", len(b.Members), "fault", faultText(fault), "action", decision.Action)
	switch decision.Action {
	case retry.ActionResubmit:
		return u.store.MarkBatchAmbiguous(b.ID, class)
	case retry.ActionQuarantine:
		return u.split(stored, class)
	case retry.ActionRetain:
		// The create may have landed and the backend cannot take it twice,
		// so the batch waits for an operator and is never sent again
		// automatically, also after a restart.
		slog.Warn("submit retained for an operator", "batch", shortID(b.ID))
		return u.store.RetryBatch(b.ID, retainedUntil, reasonRetained)
	}
	next := now.Add(u.retryDelay(stored.Attempts))
	if decision.Hold.Until.After(next) {
		next = decision.Hold.Until
	}
	return u.store.RetryBatch(b.ID, next, class)
}

func (u *Uploader) retryDelay(attempts int) time.Duration {
	delay := time.Duration(0)
	for range attempts + 1 {
		delay = u.faults.Transient.Doubling(delay)
	}
	return delay
}

// split ends a batch that a segment fault failed. A batch of one quarantines
// its segment; a larger batch is released and its members go alone, so the
// one at fault is found.
func (u *Uploader) split(b journal.Batch, reason string) error {
	if err := u.store.ReleaseBatch(b.ID); err != nil {
		return err
	}
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	if len(b.Members) > 1 {
		for _, m := range b.Members {
			u.solo[m.SegmentID] = true
		}
		u.metrics.Add(telemetry.BatchSplits, 1, nil)
		return nil
	}
	m := b.Members[0]
	seg, ok := u.store.Segment(m.SegmentID)
	if !ok {
		return nil
	}
	delete(u.solo, m.SegmentID)
	delete(u.failures, m.SegmentID)
	return u.quarantineSegment(seg.ID, seg.Index, reason, filepath.Join(u.cfg.SourceDir, seg.Name), m.Path)
}

// pollRound reads the status of every job with a saved id.
func (u *Uploader) pollRound(ctx context.Context) error {
	now := time.Now()
	u.saveUnsavedReceipts(now)
	// A confirmation whose member records failed is finished here, so its
	// segments reach cleanup without a restart.
	if err := u.store.SettleBatches(); err != nil {
		slog.Warn("finish recorded confirmations", "error", safeError(err))
	}
	for _, b := range u.store.Batches() {
		if b.Receipt == nil || b.Receipt.Backend != u.name {
			continue
		}
		switch b.State {
		case journal.StateSubmitted, journal.StateAmbiguous:
		case journal.StateStranded:
			if now.Sub(u.polledAt(b.ID)) < strandedPollInterval {
				continue
			}
		default:
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		u.stateMu.Lock()
		u.lastPolled[b.ID] = now
		u.stateMu.Unlock()
		status, err := u.client.Status(ctx, b.Receipt.ID)
		if err := u.applyStatus(b, status, err, now); err != nil {
			slog.Error("record job status", "batch", shortID(b.ID), "error", safeError(err))
		}
	}
	return nil
}

func (u *Uploader) applyStatus(b journal.Batch, status backend.Status, err error, now time.Time) error {
	if status == "" {
		// The read failed, so the job's state is unknown: keep polling.
		fault := backend.Classify(u.client, err)
		u.countAuth(fault)
		u.policy.Failed(now, u.caps, retry.Failure{Key: b.ID, Stage: retry.StageStatus, Fault: fault, JobID: true, Attempts: b.Attempts + 1})
		slog.Warn("read job status", "batch", shortID(b.ID), "fault", faultText(fault))
		return nil
	}
	switch status {
	case backend.StatusSuccess:
		if err := u.store.ConfirmBatch(b.ID); err != nil {
			if stored, ok := u.store.Batch(b.ID); ok && stored.State == journal.StateConfirmed {
				// The batch is confirmed; only member records failed, and
				// the next round finishes them.
				u.confirmed(b, now)
			}
			return err
		}
		u.point(PointConfirmed)
		u.confirmed(b, now)
	case backend.StatusPending:
		if b.State == journal.StateSubmitted && !b.SubmittedAt.IsZero() && now.Sub(b.SubmittedAt) > u.cfg.MaxJobAge {
			u.metrics.Add(telemetry.JobsStranded, 1, nil)
			slog.Warn("job unfinished past the max job age; it is kept and polled less often", "batch", shortID(b.ID))
			return u.store.MarkBatchStranded(b.ID)
		}
	case backend.StatusAmbiguous:
		if b.State == journal.StateSubmitted {
			return u.store.MarkBatchAmbiguous(b.ID, "job_ambiguous")
		}
	case backend.StatusGone:
		if b.State == journal.StateSubmitted {
			u.metrics.Add(telemetry.JobsStranded, 1, nil)
			slog.Warn("the backend no longer knows the job; it is kept for an operator", "batch", shortID(b.ID))
			return u.store.MarkBatchStranded(b.ID)
		}
	case backend.StatusFailure:
		u.metrics.Add(telemetry.Uploads, 1, telemetry.Labels{"status": "failure"})
		if b.State == journal.StateStranded {
			return nil
		}
		fault := backend.Classify(u.client, err)
		u.countAuth(fault)
		attempts := max(b.Attempts, u.countFailure(b.Members))
		decision := u.policy.Failed(now, u.caps, retry.Failure{Key: failureKey(b.Members), Stage: retry.StageJob, Fault: fault, JobID: true, Attempts: attempts})
		slog.Warn("job failed", "batch", shortID(b.ID), "fault", faultText(fault), "action", decision.Action)
		if decision.Action == retry.ActionQuarantine {
			return u.split(b, errorClass(fault))
		}
		// The job finished without landing its data, so its members are
		// batched and submitted again.
		return u.store.ReleaseBatch(b.ID)
	default:
		u.metrics.Add(telemetry.UnknownStatus, 1, nil)
		slog.Warn("unknown job status; the job is polled again", "batch", shortID(b.ID))
	}
	return nil
}

func (u *Uploader) confirmed(b journal.Batch, now time.Time) {
	var bytes int64
	u.stateMu.Lock()
	for _, m := range b.Members {
		bytes += m.Bytes
		delete(u.solo, m.SegmentID)
		delete(u.failures, m.SegmentID)
	}
	delete(u.lastPolled, b.ID)
	u.stateMu.Unlock()
	u.policy.Breaker().Succeeded()
	u.policy.Succeeded(now, failureKey(b.Members))
	u.metrics.Add(telemetry.Uploads, 1, telemetry.Labels{"status": "success"})
	u.metrics.Add(telemetry.TransportBytes, float64(bytes), nil)
	u.metrics.Set(telemetry.LastSuccessTimestamp, float64(now.Unix()), nil)
	if !b.SubmittedAt.IsZero() {
		u.metrics.Observe(telemetry.ConfirmLatency, now.Sub(b.SubmittedAt).Seconds(), nil)
	}
	slog.Info("request trace batch confirmed", "batch", shortID(b.ID), "segments", len(b.Members))
}

// cleanupRound finishes every confirmed or empty segment.
func (u *Uploader) cleanupRound(ctx context.Context) error {
	var errs []error
	for _, seg := range u.store.Segments() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		switch seg.State {
		case journal.StateConfirmed, journal.StateCleaning:
			err = u.clean(ctx, seg)
		case journal.StateEmpty:
			err = u.cleanEmpty(ctx, seg)
		}
		if err != nil {
			slog.Warn("clean up request trace segment", "segment", seg.Index, "error", safeError(err))
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func cleanupItem(sourceDir string, seg journal.Segment) cleanup.Item {
	item := cleanup.Item{
		SegmentID: seg.ID,
		Source:    filepath.Join(sourceDir, seg.Name),
		Prefix:    seg.Prefix,
		Index:     seg.Index,
		Size:      seg.Size,
		ModTime:   seg.ModTime,
		SHA256:    seg.SHA256,
	}
	for _, dest := range seg.Destinations {
		item.Transports = append(item.Transports, dest.Transport.Path)
	}
	return item
}

// clean deletes a confirmed segment's files. A segment whose confirmed
// submits did not all export (a diagnostic read, a dry run) never loses its
// source: only the transports go, and the entry is finished. The journal
// records this per submit, so a restart under a backend that exports does
// not change it.
func (u *Uploader) clean(ctx context.Context, seg journal.Segment) error {
	item := cleanupItem(u.cfg.SourceDir, seg)
	if exportedEverywhere(seg) {
		_, err := u.cleaner.Clean(ctx, item)
		return err
	}
	if err := u.store.BeginCleaning(seg.ID); err != nil {
		return err
	}
	for _, path := range item.Transports {
		if err := removeFile(path); err != nil {
			return err
		}
	}
	return u.store.FinishCleaning(seg.ID)
}

func exportedEverywhere(seg journal.Segment) bool {
	if len(seg.Destinations) == 0 {
		return false
	}
	for _, dest := range seg.Destinations {
		if !dest.Exported {
			return false
		}
	}
	return true
}

func (u *Uploader) cleanEmpty(ctx context.Context, seg journal.Segment) error {
	if u.caps.Exports {
		_, err := u.cleaner.CleanEmpty(ctx, cleanupItem(u.cfg.SourceDir, seg))
		return err
	}
	return u.store.FinishEmpty(seg.ID)
}

// housekeeping drops old tombstones and refreshes the spool gauges.
func (u *Uploader) housekeeping() {
	if _, err := u.store.Sweep(); err != nil {
		slog.Warn("sweep the journal", "error", safeError(err))
	}
	pending, quarantined := 0, 0
	for _, seg := range u.store.Segments() {
		switch seg.State {
		case journal.StateDone:
		case journal.StateQuarantined:
			quarantined++
		default:
			pending++
		}
	}
	u.metrics.Set(telemetry.PendingSegments, float64(pending), nil)
	u.metrics.Set(telemetry.QuarantinedSegments, float64(quarantined), nil)
}

var unreadableReasons = []struct {
	reason string
	count  func(transform.Result) int
}{
	{"unparseable", func(r transform.Result) int { return r.Unparseable }},
	{"line_too_long", func(r transform.Result) int { return r.LineTooLong }},
	{"row_too_large", func(r transform.Result) int { return r.RowTooLarge }},
	{"cut", func(r transform.Result) int { return r.Cut }},
}

func (u *Uploader) recordRead(item segment.Segment, result transform.Result) {
	u.metrics.Add(telemetry.SourceBytes, float64(item.Size), nil)
	for _, r := range unreadableReasons {
		if n := r.count(result); n > 0 {
			u.metrics.Add(telemetry.RecordsUnreadable, float64(n), telemetry.Labels{"reason": r.reason})
		}
	}
}

func (u *Uploader) countAuth(fault backend.Fault) {
	if fault.Reason == backend.ReasonAuth {
		u.metrics.Add(telemetry.AuthFailures, 1, telemetry.Labels{"stage": "api"})
	}
}

// errorClass is the bounded code the journal records for a fault.
func errorClass(fault backend.Fault) string {
	if fault.Reason != "" {
		return string(fault.Reason)
	}
	return reasonRejected
}

// faultText describes a fault without its cause, which can carry remote
// text, ids or paths.
func faultText(fault backend.Fault) string {
	return fmt.Sprintf("class=%s scope=%s reason=%s", fault.Class, fault.Scope, fault.Reason)
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (u *Uploader) isSolo(segmentID string) bool {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	return u.solo[segmentID]
}

func (u *Uploader) polledAt(batchID string) time.Time {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	return u.lastPolled[batchID]
}

// failureKey names a batch's work for fault correlation. A batch sent again
// gets a new id, so the key is the member set instead: one segment failing
// under a run of replacement batches counts as one item, never as many
// items failing, which would hold the whole backend.
func failureKey(members []journal.Member) string {
	if len(members) == 1 {
		return members[0].SegmentID
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.SegmentID)
	}
	slices.Sort(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return "batch-" + hex.EncodeToString(sum[:])
}

// countFailure records one more failure for every member and returns the
// highest count, so a segment's history survives the new batch id each
// retry gets.
func (u *Uploader) countFailure(members []journal.Member) int {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	highest := 0
	for _, m := range members {
		u.failures[m.SegmentID]++
		highest = max(highest, u.failures[m.SegmentID])
	}
	return highest
}

// saveUnsavedReceipts records job ids the journal could not take when they
// came back. Until then their batches are neither polled nor sent again.
func (u *Uploader) saveUnsavedReceipts(now time.Time) {
	u.stateMu.Lock()
	pending := make(map[string]string, len(u.unsaved))
	for id, receipt := range u.unsaved {
		pending[id] = receipt
	}
	u.stateMu.Unlock()
	for id, receipt := range pending {
		if err := u.store.SetReceipt(id, journal.Receipt{Backend: u.name, ID: receipt, At: now, Exported: u.caps.Exports}); err != nil {
			slog.Warn("save a job id", "batch", shortID(id), "error", safeError(err))
			continue
		}
		u.stateMu.Lock()
		delete(u.unsaved, id)
		u.stateMu.Unlock()
	}
}

// safeError describes err for a log line without the paths that file
// system errors carry.
func safeError(err error) string {
	if err == nil {
		return ""
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var parts []string
		for _, e := range joined.Unwrap() {
			if e != nil {
				parts = append(parts, safeError(e))
			}
		}
		return strings.Join(parts, "; ")
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Op + ": " + pathErr.Err.Error()
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Op + ": " + linkErr.Err.Error()
	}
	return err.Error()
}
