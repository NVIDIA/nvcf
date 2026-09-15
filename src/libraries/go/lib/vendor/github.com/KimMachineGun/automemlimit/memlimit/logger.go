// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package memlimit

import (
	"context"
	"log/slog"
)

type noopLogger struct{}

func (noopLogger) Enabled(context.Context, slog.Level) bool  { return false }
func (noopLogger) Handle(context.Context, slog.Record) error { return nil }
func (d noopLogger) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d noopLogger) WithGroup(string) slog.Handler           { return d }
