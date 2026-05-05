// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cpu

// getAuxvFn is non-nil on Go 1.21+ (via runtime_auxv_go121.go init)
// on platforms that use auxv.
var getAuxvFn func() []uintptr

func getAuxv() []uintptr {
	if getAuxvFn == nil {
		return nil
	}
	return getAuxvFn()
}
