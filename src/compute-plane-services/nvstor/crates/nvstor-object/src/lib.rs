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

//! nvstor S3-compatible HTTP gateway.
//!
//! Provides an S3 REST API on top of ChunkManager, including:
//! - Bucket CRUD
//! - Object CRUD with Range reads and CopyObject
//! - ListObjectsV2
//! - Multipart upload (initiate / upload part / complete / abort)
//! - Batch DeleteObjects
//! - SigV4 authentication
//! - Quota / rate limiting

pub mod auth;
pub mod lifecycle;
pub mod presign;
pub mod quota;
pub mod s3;
