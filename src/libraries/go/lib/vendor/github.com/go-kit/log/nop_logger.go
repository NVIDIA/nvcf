// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package log

type nopLogger struct{}

// NewNopLogger returns a logger that doesn't do anything.
func NewNopLogger() Logger { return nopLogger{} }

func (nopLogger) Log(...interface{}) error { return nil }
