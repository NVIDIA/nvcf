// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && !arm && !arm64 && !loong64 && !mips64 && !mips64le && !ppc64 && !ppc64le && !s390x && !riscv64

package cpu

func doinit() {}
