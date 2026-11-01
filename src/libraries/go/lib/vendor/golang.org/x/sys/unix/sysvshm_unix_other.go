// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (darwin && !ios) || zos

package unix

// SysvShmCtl performs control operations on the shared memory segment
// specified by id.
func SysvShmCtl(id, cmd int, desc *SysvShmDesc) (result int, err error) {
	return shmctl(id, cmd, desc)
}
