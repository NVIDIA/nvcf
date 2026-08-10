/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package utils

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

func TestLevelFromEnv(t *testing.T) {
	t.Run("valid level parsed from env", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "debug")
		lvl := LevelFromEnv()
		require.Equal(t, zapcore.DebugLevel, lvl.Level())
	})

	t.Run("invalid level defaults to info", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "not-a-level")
		lvl := LevelFromEnv()
		require.Equal(t, zapcore.InfoLevel, lvl.Level())
	})

	t.Run("empty env defaults to info", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "")
		lvl := LevelFromEnv()
		require.Equal(t, zapcore.InfoLevel, lvl.Level())
	})
}

func TestNewProductionLogger(t *testing.T) {
	logger := NewProductionLogger()
	require.NotNil(t, logger)
	require.NotNil(t, logger.GetZapLogger())

	// Smoke test that the configured logger is usable end to end.
	require.NotPanics(t, func() {
		logger.GetZapLogger().Info("production logger smoke test")
		_ = logger.GetZapLogger().Sync()
	})
}
