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
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/format"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform"
)

// fileIdentity tells one spool file from a later one that reuses its name.
type fileIdentity struct {
	name    string
	size    int64
	modTime time.Time
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

// recordSchemaVersion is the version of what the core hands a format writer
// for the same settings. Bump it when a release changes that, such as how
// records are redacted, so unsent transports an older release prepared are
// prepared again.
const recordSchemaVersion = 1

// formatVersioner is implemented by a format factory whose writers can
// change what they write between releases, such as a table writer whose
// schema changes. A new version makes unsent transports an older writer
// wrote be prepared again.
type formatVersioner interface {
	FormatVersion() string
}

// recordPolicy fingerprints everything that decides what a transport holds:
// the read bounds, suppression rules and headerless policy, redaction, the
// record schema, and the format with its writer version. Only the hash is
// kept or logged, never the configured values.
func recordPolicy(cfg transform.Config, factory format.Factory) (string, error) {
	writerVersion := ""
	if versioned, ok := factory.(formatVersioner); ok {
		writerVersion = versioned.FormatVersion()
	}
	raw, err := json.Marshal(struct {
		Transform     transform.Config
		RecordSchema  int
		Format        backend.Format
		WriterVersion string
	}{cfg, recordSchemaVersion, factory.Format(), writerVersion})
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

var unreadableReasons = []struct {
	reason string
	count  func(transform.Result) int
}{
	{"unparseable", func(r transform.Result) int { return r.Unparseable }},
	{"line_too_long", func(r transform.Result) int { return r.LineTooLong }},
	{"row_too_large", func(r transform.Result) int { return r.RowTooLarge }},
	{"cut", func(r transform.Result) int { return r.Cut }},
}

// unsupportedVersionLabels labels records_dropped for records from a Dynamo
// release older than v1.4.0. They carry no event type, so their record type
// is unknown, as it is for any type outside v1.4.0's set.
func unsupportedVersionLabels() telemetry.Labels {
	return telemetry.Labels{"record_type": "unknown", "reason": "unsupported_version"}
}

func (u *Uploader) recordRead(item segment.Segment, result transform.Result) {
	u.metrics.Add(telemetry.SourceBytes, float64(item.Size), nil)
	for _, r := range unreadableReasons {
		if n := r.count(result); n > 0 {
			u.metrics.Add(telemetry.RecordsUnreadable, float64(n), telemetry.Labels{"reason": r.reason})
		}
	}
	if result.UnsupportedVersion > 0 {
		u.metrics.Add(telemetry.RecordsDropped, float64(result.UnsupportedVersion), unsupportedVersionLabels())
	}
}
