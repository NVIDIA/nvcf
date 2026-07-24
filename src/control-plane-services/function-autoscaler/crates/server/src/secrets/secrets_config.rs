/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

use serde::Deserialize;

#[derive(Debug, Deserialize, Clone)]
pub struct Secrets {
    #[serde(rename = "nvcfApiToken")]
    pub nvcf_api_token: Option<String>,
    pub kv: CredentialsData,
}

#[derive(Debug, Deserialize, Clone)]
#[serde(deny_unknown_fields)]
pub struct CredentialsData {
    pub cassandra: Option<CassandraCredentials>,
    pub cassandra_ssl: Option<CassandraSslCertificates>,
    pub timeseries_db: Option<TimeseriesDbCredentials>,
    pub tracing: Option<TracingCredentials>,
    pub nvcf_api: Option<OAuth2ClientCredentials>,
}

#[derive(Debug, Deserialize, Clone)]
pub struct CassandraCredentials {
    pub username: String,
    pub password: String,
}

#[derive(Debug, Deserialize, Clone)]
pub struct CassandraSslCertificates {
    pub app_cert: String,
    pub app_key: String,
    pub tls_cert: String,
}

#[derive(Debug, Deserialize, Clone)]
pub struct TimeseriesDbCredentials {
    pub username: String,
    pub password: String,
}

#[derive(Debug, Deserialize, Clone)]
pub struct TracingCredentials {
    pub access_key: String,
}

#[derive(Debug, Deserialize, Clone)]
pub struct OAuth2ClientCredentials {
    pub client_id: String,
    pub client_secret: String,
}
