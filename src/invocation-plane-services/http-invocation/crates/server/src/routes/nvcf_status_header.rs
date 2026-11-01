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

use http::{HeaderName, HeaderValue, StatusCode};
use std::fmt::{Display, Formatter};

pub enum NVCFStatusHeader {
    Errored,
    InProgress,
    Fulfilled,
}

impl Display for NVCFStatusHeader {
    fn fmt(&self, f: &mut Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            NVCFStatusHeader::Errored => "errored",
            NVCFStatusHeader::InProgress => "in-progress",
            NVCFStatusHeader::Fulfilled => "fulfilled",
        })
    }
}

impl From<StatusCode> for NVCFStatusHeader {
    fn from(code: StatusCode) -> Self {
        match code.as_u16() {
            202 => Self::InProgress,
            200..=399 => Self::Fulfilled,
            _ => Self::Errored,
        }
    }
}

impl NVCFStatusHeader {
    pub const NVCF_STATUS_HEADER_NAME: HeaderName = HeaderName::from_static("nvcf-status");
    pub fn as_response_header(&self) -> (HeaderName, HeaderValue) {
        (
            Self::NVCF_STATUS_HEADER_NAME,
            HeaderValue::try_from(&self.to_string())
                .expect("a NVCFStatusHeader is always a valid header value"),
        )
    }
}
