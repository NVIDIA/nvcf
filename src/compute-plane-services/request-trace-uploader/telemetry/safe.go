// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"log/slog"
	"sync"
)

// Safe wraps m so that a panic inside it is recovered and the observation
// dropped. A broken exporter then costs metrics, never an upload. The first
// failure is logged; later ones are not, so a dead exporter cannot flood logs.
func Safe(m Metrics) Metrics {
	return &safe{next: m}
}

type safe struct {
	next Metrics
	warn sync.Once
}

func (s *safe) Add(name string, delta float64, labels Labels) {
	defer s.dropPanic(name)
	s.next.Add(name, delta, labels)
}

func (s *safe) Set(name string, value float64, labels Labels) {
	defer s.dropPanic(name)
	s.next.Set(name, value, labels)
}

func (s *safe) Observe(name string, value float64, labels Labels) {
	defer s.dropPanic(name)
	s.next.Observe(name, value, labels)
}

func (s *safe) dropPanic(name string) {
	if r := recover(); r != nil {
		s.warn.Do(func() {
			slog.Warn("telemetry recorder failed, dropping observations", "instrument", name, "panic", r)
		})
	}
}
