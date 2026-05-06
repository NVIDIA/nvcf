// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build aix || dragonfly || freebsd || linux || netbsd || openbsd

package unix

// ReadDirent reads directory entries from fd and writes them into buf.
func ReadDirent(fd int, buf []byte) (n int, err error) {
	return Getdents(fd, buf)
}
