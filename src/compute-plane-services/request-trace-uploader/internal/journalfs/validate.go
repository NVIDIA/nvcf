// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

var (
	segmentStates = states(journal.StateDiscovered, journal.StatePreparing, journal.StatePrepared,
		journal.StateSubmitting, journal.StateConfirmed, journal.StateCleaning, journal.StateDone,
		journal.StateEmpty, journal.StateQuarantined)
	destinationStates = states(journal.StatePrepared, journal.StateSubmitting, journal.StateConfirmed)
	batchStates       = states(journal.StateSubmitting, journal.StateSubmitted, journal.StateRetryWait,
		journal.StateAmbiguous, journal.StateConfirmed, journal.StateStranded, journal.StateReleased)
	// receiptStates are the batch states that only SetReceipt reaches.
	receiptStates = states(journal.StateSubmitted, journal.StateStranded, journal.StateConfirmed)
	// transportStates are the segment states that own transports.
	transportStates = states(journal.StatePrepared, journal.StateSubmitting, journal.StateConfirmed, journal.StateCleaning)
)

func states(list ...journal.State) map[journal.State]bool {
	out := make(map[journal.State]bool, len(list))
	for _, s := range list {
		out[s] = true
	}
	return out
}

// ownedTransport reports whether path is a file in the transport directory
// that belongs to the segment. Only such files are ever deleted as
// transports, so a bad record can never point a delete at a source segment
// or outside the journal.
func ownedTransport(transportDir, segmentID, path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("transport path must be absolute")
	}
	clean := filepath.Clean(path)
	if filepath.Dir(clean) != filepath.Clean(transportDir) || !strings.HasPrefix(filepath.Base(clean), segmentID+".") {
		return errors.New("transport must be in the transport directory and named for its segment")
	}
	return nil
}

// validSegment checks the invariants a loaded or updated segment entry must
// hold before the store acts on it.
func validSegment(seg *journal.Segment, transportDir string) error {
	var errs []error
	if !validSegmentName(seg.Name) || seg.Prefix == "" || seg.Index < 0 || seg.Size < 0 {
		errs = append(errs, errors.New("segment identity is incomplete"))
	}
	if !segmentIDPattern.MatchString(seg.SHA256) {
		errs = append(errs, errors.New("segment content hash is not sha256 hex"))
	}
	if !segmentStates[seg.State] {
		errs = append(errs, fmt.Errorf("unknown segment state %q", seg.State))
	}
	if transportStates[seg.State] && (seg.Backend == "" || len(seg.Destinations) == 0) {
		errs = append(errs, fmt.Errorf("a %s segment needs a backend and destinations", seg.State))
	}
	for name, dest := range seg.Destinations {
		if name == "" || dest == nil {
			errs = append(errs, errors.New("destination is empty"))
			continue
		}
		if !destinationStates[dest.State] {
			errs = append(errs, fmt.Errorf("unknown destination state %q", dest.State))
		}
		if dest.BatchID != "" && !batchIDPattern.MatchString(dest.BatchID) {
			errs = append(errs, errors.New("destination batch id is not a plain name"))
		}
		if err := ownedTransport(transportDir, seg.ID, dest.Transport.Path); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// validBatch checks the invariants a loaded batch entry must hold.
func validBatch(b *journal.Batch, transportDir string) error {
	var errs []error
	if b.Backend == "" || b.Destination == "" {
		errs = append(errs, errors.New("batch needs a backend and a destination"))
	}
	if !batchStates[b.State] {
		errs = append(errs, fmt.Errorf("unknown batch state %q", b.State))
	}
	if len(b.Members) == 0 {
		errs = append(errs, errors.New("batch has no members"))
	}
	for _, m := range b.Members {
		if !segmentIDPattern.MatchString(m.SegmentID) {
			errs = append(errs, errors.New("batch member segment id is not sha256 hex"))
			continue
		}
		if err := ownedTransport(transportDir, m.SegmentID, m.Path); err != nil {
			errs = append(errs, err)
		}
	}
	switch {
	case b.Receipt != nil && (b.Receipt.ID == "" || b.Receipt.Backend != b.Backend):
		errs = append(errs, errors.New("batch receipt does not belong to the batch backend"))
	case b.Receipt == nil && receiptStates[b.State]:
		errs = append(errs, fmt.Errorf("a %s batch needs a receipt", b.State))
	}
	if b.Attempts < 0 {
		errs = append(errs, errors.New("batch attempts are negative"))
	}
	return errors.Join(errs...)
}
