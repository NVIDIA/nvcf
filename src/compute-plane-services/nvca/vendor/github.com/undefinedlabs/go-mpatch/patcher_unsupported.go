// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !386 && !amd64 && !arm64
// +build !386,!amd64,!arm64

package mpatch

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

// Gets the jump function rewrite bytes
//
//go:nosplit
func getJumpFuncBytes(to unsafe.Pointer) ([]byte, error) {
	return nil, errors.New(fmt.Sprintf("unsupported architecture: %s", runtime.GOARCH))
}
