// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//! nvstor FUSE frontend — high-performance filesystem serving over /dev/fuse.
//!
//! Serves a real directory tree as a read-only FUSE mount. Designed for:
//! - Container image overlay lowerdirs (zero-transfer container start)
//! - Container runtime CDI hook workloads (concurrent stat/readdir/open
//!   across thousands of device/driver files)
//!
//! Key properties:
//! - Multi-threaded (fuser handles thread pool)
//! - No global locks on the request path
//! - Complete POSIX attributes (uid, gid, mode, rdev, nlink, blocks)
//! - Extended attribute (xattr) pass-through
//! - Kernel page cache integration (long attr/entry timeouts for read-only)

pub mod cache;
pub mod fs_server;
pub mod passthrough;
pub mod remote;
