// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"
)

func TestStatusIntervalIsABackendNeutralSetting(t *testing.T) {
	cfg, _, err := Load(mapLookup(requiredEnv(nil)))
	if err != nil || cfg.StatusInterval != DefaultPollInterval || DefaultPollInterval != 30*time.Second {
		t.Fatalf("default status interval = %v (error %v), want 30 s", cfg.StatusInterval, err)
	}
	cfg, _, err = Load(mapLookup(requiredEnv(map[string]string{EnvStatusInterval: "10"})))
	if err != nil || cfg.StatusInterval != 10*time.Second {
		t.Fatalf("status interval = %v (error %v), want 10 s", cfg.StatusInterval, err)
	}
	for _, bad := range []string{"0", "3601", "10s"} {
		if _, _, err := Load(mapLookup(requiredEnv(map[string]string{EnvStatusInterval: bad}))); err == nil {
			t.Errorf("status interval %q accepted, want a startup error", bad)
		}
	}
}
