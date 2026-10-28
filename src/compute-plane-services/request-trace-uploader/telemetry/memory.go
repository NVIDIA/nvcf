// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"fmt"
	"sync"
)

// Memory is an in-process recorder. Tests read it directly; it keeps only
// totals per series, so its size is bounded by the catalog and the series cap.
type Memory struct {
	backend string

	mu      sync.Mutex
	series  map[string]map[string]*point
	dropped int
}

type point struct {
	labels Labels
	value  float64
	count  int
	sum    float64
}

// NewMemory returns a recorder whose series all carry backend, with every
// known counter series preset to zero.
func NewMemory(backend string) (*Memory, error) {
	if err := ValidLabelValue(backend); err != nil {
		return nil, err
	}
	m := &Memory{backend: backend, series: map[string]map[string]*point{}}
	Preset(m)
	return m, nil
}

func (m *Memory) Add(name string, delta float64, labels Labels) {
	m.record(Counter, name, delta, labels)
}

func (m *Memory) Set(name string, value float64, labels Labels) {
	m.record(Gauge, name, value, labels)
}

func (m *Memory) Observe(name string, value float64, labels Labels) {
	m.record(Histogram, name, value, labels)
}

func (m *Memory) record(kind Kind, name string, value float64, labels Labels) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := Validate(kind, name, value, labels); err != nil {
		m.dropped++
		return
	}
	p, err := m.point(name, labels, true)
	if err != nil {
		m.dropped++
		return
	}
	switch kind {
	case Counter:
		p.value += value
	case Gauge:
		p.value = value
	case Histogram:
		p.count++
		p.sum += value
	}
}

// point finds the series for labels, creating it when create is set and the
// instrument is under the series cap.
func (m *Memory) point(name string, labels Labels, create bool) (*point, error) {
	full := Labels{BackendLabel: m.backend}
	for k, v := range labels {
		full[k] = v
	}
	key := seriesKey(full)
	byKey := m.series[name]
	if p, ok := byKey[key]; ok {
		return p, nil
	}
	if !create {
		return nil, fmt.Errorf("no series %s%s", name, key)
	}
	if len(byKey) >= MaxSeriesPerInstrument {
		return nil, errSeriesLimit
	}
	if byKey == nil {
		byKey = map[string]*point{}
		m.series[name] = byKey
	}
	p := &point{labels: full}
	byKey[key] = p
	return p, nil
}

// Value returns a counter or gauge value. labels exclude BackendLabel.
func (m *Memory) Value(name string, labels Labels) (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.point(name, labels, false)
	if err != nil {
		return 0, false
	}
	return p.value, true
}

// Histogram returns the sample count and sum of a histogram series.
func (m *Memory) Histogram(name string, labels Labels) (int, float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.point(name, labels, false)
	if err != nil {
		return 0, 0, false
	}
	return p.count, p.sum, true
}

// Series returns the full label set, BackendLabel included, of every series
// recorded for name.
func (m *Memory) Series(name string) []Labels {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Labels, 0, len(m.series[name]))
	for _, p := range m.series[name] {
		labels := Labels{}
		for k, v := range p.labels {
			labels[k] = v
		}
		out = append(out, labels)
	}
	return out
}

// Dropped counts observations rejected by Validate or the series cap.
func (m *Memory) Dropped() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropped
}
