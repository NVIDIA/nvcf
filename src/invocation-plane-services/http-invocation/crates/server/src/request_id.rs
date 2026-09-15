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

use axum::http::{HeaderName, HeaderValue};
use std::fmt::{Debug, Display, Formatter};
use uuid::Uuid;

#[derive(Copy, Clone, Default)]
pub struct RequestId(Uuid);

impl RequestId {
    pub fn new() -> Self {
        RequestId(Uuid::new_v4())
    }
}

impl Display for RequestId {
    fn fmt(&self, f: &mut Formatter<'_>) -> std::fmt::Result {
        std::fmt::Display::fmt(&self.0, f)
    }
}

impl Debug for RequestId {
    fn fmt(&self, f: &mut Formatter<'_>) -> std::fmt::Result {
        std::fmt::Debug::fmt(&self.0, f)
    }
}

impl From<Uuid> for RequestId {
    fn from(value: Uuid) -> Self {
        Self(value)
    }
}

impl RequestId {
    pub const HEADER_NAME: HeaderName = HeaderName::from_static("nvcf-reqid");
    pub fn as_response_header(&self) -> (HeaderName, HeaderValue) {
        (
            Self::HEADER_NAME,
            HeaderValue::from_str(&self.to_string())
                .expect("a uuid is always a valid header value"),
        )
    }
}
