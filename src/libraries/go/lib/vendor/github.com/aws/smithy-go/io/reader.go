// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package io

import (
	"io"
)

// ReadSeekNopCloser wraps an io.ReadSeeker with an additional Close method
// that does nothing.
type ReadSeekNopCloser struct {
	io.ReadSeeker
}

// Close does nothing.
func (ReadSeekNopCloser) Close() error {
	return nil
}
