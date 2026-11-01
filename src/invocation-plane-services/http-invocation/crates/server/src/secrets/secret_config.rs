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

use serde::{Deserialize, Serialize};

#[derive(Serialize, Deserialize, Debug, Clone, PartialEq)]
pub struct Secrets {
    pub oauth2: Oauth2Secrets,
    pub nats: NatsSecrets,
    pub tracing: TracingSecrets,
    pub fixed_bearer_secrets: FixedBearerSecrets,
}

#[derive(Serialize, Deserialize, Debug, Clone, PartialEq)]
pub struct Oauth2Secrets {
    pub client_id: String,
    pub client_secret: String,
}

#[derive(Serialize, Deserialize, Debug, Clone, PartialEq)]
pub struct NatsSecrets {
    pub nkey_pub: Option<String>,
    pub nkey_seed: Option<String>,
}

#[derive(Serialize, Deserialize, Debug, Clone, PartialEq)]
pub struct TracingSecrets {
    pub access_key: String,
}

#[derive(Serialize, Deserialize, Debug, Clone, PartialEq)]
pub struct FixedBearerSecrets {
    pub nvcf_api_token: Option<String>,
    pub rate_limit_token: Option<String>,
}
