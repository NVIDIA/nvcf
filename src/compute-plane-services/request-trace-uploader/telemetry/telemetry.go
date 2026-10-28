// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package telemetry defines the uploader's metric instruments and the small
// interface the pipeline records them through.
//
// Names are backend-neutral. Every series carries one fixed backend label, set
// once when the recorder is built, so the same dashboard serves every backend.
// Label keys come from a bounded set and label values must be short tokens, so
// no identifier, path or error text can become a label. How the instruments
// are exported, and under which prefix, is the exporter's choice.
package telemetry

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
)

// Kind is the instrument type.
type Kind int

const (
	Counter Kind = iota + 1
	Gauge
	Histogram
)

func (k Kind) String() string {
	switch k {
	case Counter:
		return "counter"
	case Gauge:
		return "gauge"
	case Histogram:
		return "histogram"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

const (
	// BackendLabel is added by the recorder to every series. Callers never
	// set it.
	BackendLabel = "backend"
	// MaxSeriesPerInstrument bounds label cardinality. Observations that would
	// add a series past it are dropped.
	MaxSeriesPerInstrument = 64
)

// Instrument names. The first group is the required set; the rest cover
// faults, the spool, batching and startup cleanup.
const (
	Uploads              = "uploads"
	UploadDuration       = "upload.duration"
	SourceBytes          = "source_bytes"
	TransportBytes       = "transport_bytes"
	ConfirmLatency       = "confirm_latency"
	RecordsDropped       = "records_dropped"
	Deletions            = "deletions"
	PendingSegments      = "pending_segments"
	QuarantinedSegments  = "quarantined_segments"
	QuarantineEvicted    = "quarantine_evicted"
	LastSuccessTimestamp = "last_success_timestamp"
	Uptime               = "uptime"

	RecordsReceived   = "records_received"
	RecordsUnreadable = "records_unreadable"
	SubmitsAmbiguous  = "submits_ambiguous"
	AuthFailures      = "auth_failures"
	JobsStranded      = "jobs_stranded"
	SegmentsEvicted   = "segments_evicted"
	QuarantineTripped = "quarantine_tripped"
	SpoolUsedBytes    = "spool_used_bytes"
	SpoolFreeBytes    = "spool_free_bytes"
	PendingBytes      = "pending_bytes"
	OldestPendingAge  = "oldest_pending_age"
	BatchSegments     = "batch_segments"
	BatchBytes        = "batch_bytes"
	BatchSplits       = "batch_splits"
	// LeftoversRemoved and LeftoversKept count files another uploader left on
	// the spool, by registered kind.
	LeftoversRemoved = "leftovers_removed"
	LeftoversKept    = "leftovers_kept"
	// SegmentsMissed counts segments that left the spool before this uploader
	// read them, which only happens in shadow mode.
	SegmentsMissed = "segments_missed"
	// RecordsEvaluated counts records checked against suppression rules,
	// RecordsSuppressed the records a rule acted on, and
	// SuppressionHeaderPresent the evaluated records that carried a rule's
	// header, matched or not. A rule is labeled by its operator-assigned
	// name, never by a header name or value.
	RecordsEvaluated         = "records_evaluated"
	RecordsSuppressed        = "records_suppressed"
	SuppressionHeaderPresent = "suppression_header_present"
	// RecordsIncomplete counts payload records whose source says
	// payload_complete is false.
	RecordsIncomplete = "records_incomplete"
	// UnknownStatus counts job status reads that returned a status the core
	// does not know. The job is treated as still running.
	UnknownStatus = "unknown_status"
)

// Labels are the attributes of one observation, without BackendLabel.
type Labels = map[string]string

// Instrument describes one metric.
type Instrument struct {
	Name string
	Kind Kind
	// Unit uses the UCUM codes OTLP expects: "s", "By", or "1".
	Unit string
	// Labels lists the label keys every observation must carry.
	Labels []string
	// Values enumerates the allowed values of a label key. A counter whose
	// keys are all enumerated is preset to zero for every combination.
	Values map[string][]string
}

var allowedLabelKeys = map[string]bool{
	BackendLabel:  true,
	"table":       true,
	"status":      true,
	"reason":      true,
	"record_type": true,
	"scope":       true,
	"stage":       true,
	"kind":        true,
	"rule":        true,
	"action":      true,
}

var labelValuePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

var outcome = map[string][]string{"status": {"success", "failure"}}

// payloadRecords is the one record type that carries headers in Dynamo
// v1.4.0, so the only one suppression evaluates.
var payloadRecords = map[string][]string{"record_type": {"request_payload"}}

// SuppressionActions are the action label values of RecordsSuppressed.
var SuppressionActions = []string{"strip_payload", "drop_record"}

var catalog = []Instrument{
	{Name: Uploads, Kind: Counter, Unit: "1", Labels: []string{"status"}, Values: outcome},
	{Name: UploadDuration, Kind: Histogram, Unit: "s"},
	{Name: SourceBytes, Kind: Counter, Unit: "By"},
	{Name: TransportBytes, Kind: Counter, Unit: "By"},
	{Name: ConfirmLatency, Kind: Histogram, Unit: "s"},
	{Name: RecordsDropped, Kind: Counter, Unit: "1", Labels: []string{"record_type", "reason"}},
	{Name: Deletions, Kind: Counter, Unit: "1", Labels: []string{"status"}, Values: outcome},
	{Name: PendingSegments, Kind: Gauge, Unit: "1"},
	{Name: QuarantinedSegments, Kind: Gauge, Unit: "1"},
	{Name: QuarantineEvicted, Kind: Counter, Unit: "1"},
	{Name: LastSuccessTimestamp, Kind: Gauge, Unit: "s"},
	{Name: Uptime, Kind: Gauge, Unit: "s"},

	{Name: RecordsReceived, Kind: Counter, Unit: "1", Labels: []string{"record_type"}},
	{Name: RecordsUnreadable, Kind: Counter, Unit: "1", Labels: []string{"reason"}},
	{Name: SubmitsAmbiguous, Kind: Counter, Unit: "1"},
	{Name: AuthFailures, Kind: Counter, Unit: "1", Labels: []string{"stage"}, Values: map[string][]string{"stage": {"token", "api"}}},
	{Name: JobsStranded, Kind: Counter, Unit: "1"},
	{Name: SegmentsEvicted, Kind: Counter, Unit: "1"},
	{Name: QuarantineTripped, Kind: Counter, Unit: "1"},
	{Name: SpoolUsedBytes, Kind: Gauge, Unit: "By"},
	{Name: SpoolFreeBytes, Kind: Gauge, Unit: "By"},
	{Name: PendingBytes, Kind: Gauge, Unit: "By"},
	{Name: OldestPendingAge, Kind: Gauge, Unit: "s"},
	{Name: BatchSegments, Kind: Histogram, Unit: "1"},
	{Name: BatchBytes, Kind: Histogram, Unit: "By"},
	{Name: BatchSplits, Kind: Counter, Unit: "1"},
	{Name: LeftoversRemoved, Kind: Counter, Unit: "1", Labels: []string{"kind"}},
	{Name: LeftoversKept, Kind: Counter, Unit: "1", Labels: []string{"kind"}},
	{Name: SegmentsMissed, Kind: Counter, Unit: "1"},
	{Name: RecordsEvaluated, Kind: Counter, Unit: "1", Labels: []string{"record_type"}, Values: payloadRecords},
	{Name: RecordsSuppressed, Kind: Counter, Unit: "1", Labels: []string{"rule", "action"},
		Values: map[string][]string{"action": SuppressionActions}},
	{Name: SuppressionHeaderPresent, Kind: Counter, Unit: "1", Labels: []string{"rule"}},
	{Name: RecordsIncomplete, Kind: Counter, Unit: "1", Labels: []string{"record_type"}, Values: payloadRecords},
	{Name: UnknownStatus, Kind: Counter, Unit: "1"},
}

var byName = func() map[string]Instrument {
	index := make(map[string]Instrument, len(catalog))
	for _, instrument := range catalog {
		index[instrument.Name] = instrument
	}
	return index
}()

// Instruments returns the catalog in a stable order.
func Instruments() []Instrument {
	return append([]Instrument(nil), catalog...)
}

// Lookup returns the instrument named name.
func Lookup(name string) (Instrument, bool) {
	instrument, ok := byName[name]
	return instrument, ok
}

// Metrics records observations. Implementations must not block or fail the
// caller: an invalid observation, or a broken exporter, is dropped.
type Metrics interface {
	// Add increases a counter. A zero delta presets a series.
	Add(name string, delta float64, labels Labels)
	// Set replaces a gauge value.
	Set(name string, value float64, labels Labels)
	// Observe records one histogram sample.
	Observe(name string, value float64, labels Labels)
}

// ValidLabelValue reports whether value is a short lowercase token, the only
// shape a label value may take. Packages that define label values at startup,
// such as the backend name or a leftover kind, check them with it.
func ValidLabelValue(value string) error {
	if !labelValuePattern.MatchString(value) {
		return fmt.Errorf("telemetry label value %q must match %s", value, labelValuePattern)
	}
	return nil
}

// Validate checks one observation against the catalog. Every recorder calls
// it, so the bounds hold for any exporter.
func Validate(kind Kind, name string, value float64, labels Labels) error {
	instrument, ok := byName[name]
	if !ok {
		return fmt.Errorf("unknown instrument %q", name)
	}
	if instrument.Kind != kind {
		return fmt.Errorf("instrument %q is a %v, not a %v", name, instrument.Kind, kind)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("instrument %q value is not finite", name)
	}
	if kind == Counter && value < 0 {
		return fmt.Errorf("counter %q cannot decrease", name)
	}
	if len(labels) != len(instrument.Labels) {
		return fmt.Errorf("instrument %q takes labels %v", name, instrument.Labels)
	}
	for _, key := range instrument.Labels {
		value, ok := labels[key]
		if !ok {
			return fmt.Errorf("instrument %q takes labels %v", name, instrument.Labels)
		}
		if !labelValuePattern.MatchString(value) {
			return fmt.Errorf("instrument %q label %q value is not a short lowercase token", name, key)
		}
		if allowed, enumerated := instrument.Values[key]; enumerated && !containsString(allowed, value) {
			return fmt.Errorf("instrument %q label %q must be one of %v", name, key, allowed)
		}
	}
	return nil
}

var errSeriesLimit = errors.New("series limit reached")

// presetLabels lists every label combination of a fully enumerated
// instrument, or the empty set for one without labels.
func presetLabels(instrument Instrument) []Labels {
	combinations := []Labels{{}}
	for _, key := range instrument.Labels {
		values, enumerated := instrument.Values[key]
		if !enumerated {
			return nil
		}
		next := make([]Labels, 0, len(combinations)*len(values))
		for _, combination := range combinations {
			for _, value := range values {
				labels := Labels{key: value}
				for k, v := range combination {
					labels[k] = v
				}
				next = append(next, labels)
			}
		}
		combinations = next
	}
	return combinations
}

// Preset records zero for every counter series known before any traffic, so
// the first export already shows them.
func Preset(m Metrics) {
	for _, instrument := range catalog {
		if instrument.Kind != Counter {
			continue
		}
		for _, labels := range presetLabels(instrument) {
			m.Add(instrument.Name, 0, labels)
		}
	}
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func seriesKey(labels Labels) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	key := ""
	for _, k := range keys {
		key += k + "=" + labels[k] + ","
	}
	return key
}

// Receiver is implemented by a component, such as a backend, that records
// its own observations. The service hands it the shared recorder, so its
// series carry the same backend label and limits as the core's.
type Receiver interface {
	UseMetrics(Metrics)
}
