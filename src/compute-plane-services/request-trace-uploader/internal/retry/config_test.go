// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"strings"
	"testing"
	"time"
)

// The design caps the transient backoff at 5 minutes. The literal is
// independent of DefaultTransientMax, so a change to the constant fails here.
func TestDefaultTransientBackoffCapsAtFiveMinutes(t *testing.T) {
	if got := DefaultConfig().Transient.Max; got != 5*time.Minute {
		t.Fatalf("DefaultConfig().Transient.Max = %v, want 5m", got)
	}
}

func TestConfigValidate(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("DefaultConfig().Validate() = %v, want nil", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "zero transient base", mutate: func(c *Config) { c.Transient.Base = 0 }, want: "transient"},
		{name: "transient base over max", mutate: func(c *Config) { c.Transient.Base = c.Transient.Max + time.Second }, want: "transient"},
		{name: "negative auth base", mutate: func(c *Config) { c.Auth.Base = -time.Second }, want: "auth"},
		{name: "auth base over max", mutate: func(c *Config) { c.Auth.Max = c.Auth.Base - time.Second }, want: "auth"},
		{name: "transient max too large", mutate: func(c *Config) { c.Transient.Max = 1 << 62 }, want: "transient"},
		{name: "auth max too large", mutate: func(c *Config) { c.Auth.Base, c.Auth.Max = 1<<62, 1<<62 }, want: "auth"},
		{name: "retry-after cap too large", mutate: func(c *Config) { c.RetryAfterMax = 1 << 62 }, want: "retry-after"},
		{name: "resume jitter too large", mutate: func(c *Config) { c.ResumeJitter = 1 << 62 }, want: "resume jitter"},
		{name: "correlation window too large", mutate: func(c *Config) { c.CorrelationWindow = 1 << 62 }, want: "correlation window"},
		{name: "zero retry-after cap", mutate: func(c *Config) { c.RetryAfterMax = 0 }, want: "retry-after"},
		{name: "negative resume jitter", mutate: func(c *Config) { c.ResumeJitter = -time.Second }, want: "resume jitter"},
		{name: "zero create budget", mutate: func(c *Config) { c.CreatesPerMinute = 0 }, want: "creates per minute"},
		{name: "zero create burst", mutate: func(c *Config) { c.CreateBurst = 0 }, want: "create burst"},
		{name: "zero correlation window", mutate: func(c *Config) { c.CorrelationWindow = 0 }, want: "correlation window"},
		{name: "zero backend scope segments", mutate: func(c *Config) { c.BackendScopeSegments = 0 }, want: "backend scope segments"},
		{name: "zero segment scope failures", mutate: func(c *Config) { c.SegmentScopeFailures = 0 }, want: "segment scope failures"},
		{name: "zero quarantine trip", mutate: func(c *Config) { c.QuarantineTrip = 0 }, want: "quarantine trip"},
		{name: "unknown ambiguous policy", mutate: func(c *Config) { c.AmbiguousWithoutID = "guess" }, want: "ambiguous"},
		{name: "empty ambiguous policy", mutate: func(c *Config) { c.AmbiguousWithoutID = "" }, want: "ambiguous"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want an error naming %q", err, tt.want)
			}
		})
	}
}
