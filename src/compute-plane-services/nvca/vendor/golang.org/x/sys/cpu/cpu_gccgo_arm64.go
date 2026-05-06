// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build gccgo

package cpu

func getisar0() uint64 { return 0 }
func getisar1() uint64 { return 0 }
func getpfr0() uint64  { return 0 }
func getzfr0() uint64  { return 0 }
