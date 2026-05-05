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

use headers::Header;
use http::{HeaderName, HeaderValue};
use uuid::Uuid;

static INPUT_ASSET_HEADER_NAME: HeaderName = HeaderName::from_static("nvcf-input-asset-references");

pub struct InputAssetHeader(pub Vec<Uuid>);

impl Header for InputAssetHeader {
    fn name() -> &'static HeaderName {
        &INPUT_ASSET_HEADER_NAME
    }

    fn decode<'i, I>(values: &mut I) -> Result<Self, headers::Error>
    where
        Self: Sized,
        I: Iterator<Item = &'i HeaderValue>,
    {
        Ok(Self(
            values
                .into_iter()
                .flat_map(HeaderValue::to_str)
                .flat_map(|value| value.split(','))
                .map(str::trim)
                .filter(|value| !value.is_empty())
                .map(Uuid::try_parse)
                .collect::<Result<Vec<_>, _>>()
                .map_err(|_| headers::Error::invalid())?,
        ))
    }

    fn encode<E: Extend<HeaderValue>>(&self, values: &mut E) {
        values.extend(
            self.0
                .iter()
                .flat_map(|asset_id| HeaderValue::from_str(&asset_id.to_string()).ok()),
        );
    }
}
