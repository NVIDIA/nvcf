// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package log

import "github.com/go-kit/log"

// NewNopLogger returns a logger that doesn't do anything.
func NewNopLogger() Logger {
	return log.NewNopLogger()
}
