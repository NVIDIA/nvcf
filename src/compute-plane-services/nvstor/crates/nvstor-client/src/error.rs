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

//! Client error types.

use std::io;

/// Errors returned by [`NvstorClient`](crate::NvstorClient) operations.
#[derive(Debug, thiserror::Error)]
pub enum ClientError {
    /// Network or I/O error.
    #[error("io: {0}")]
    Io(#[from] io::Error),

    /// Serialization/deserialization failure.
    #[error("codec: {0}")]
    Codec(#[from] bincode::Error),

    /// Server returned an error response.
    #[error("server error {code}: {message}")]
    Server { code: u16, message: String },

    /// Received an unexpected response variant.
    #[error("unexpected response from server")]
    UnexpectedResponse,

    /// Invalid address string.
    #[error("invalid address: {0}")]
    InvalidAddress(String),
}
