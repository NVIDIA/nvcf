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

//! GDS URLs, TLS options, backoff and the connect policy.

use std::fmt;
use std::time::Duration;

use tonic::transport::{Certificate, ClientTlsConfig, Identity};

use crate::error::Error;

/// One parsed GDS gRPC URL: `https://host[:port]` or, when allowed,
/// `http://host[:port]`.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub struct GdsUrl {
    url: String,
    host: String,
    tls: bool,
}

impl GdsUrl {
    /// Parses one URL. A value without a scheme is taken as `https://`.
    /// Plain `http` is rejected unless `allow_insecure_http` is set.
    pub fn parse(raw: &str, allow_insecure_http: bool) -> Result<Self, Error> {
        let raw = raw.trim();
        let invalid = |reason: &str| Error::InvalidUrl {
            url: raw.to_string(),
            reason: reason.to_string(),
        };
        if raw.is_empty() {
            return Err(invalid("empty URL"));
        }
        let with_scheme = if raw.contains("://") {
            raw.to_string()
        } else {
            format!("https://{raw}")
        };
        let uri: http::Uri = with_scheme
            .parse()
            .map_err(|error: http::uri::InvalidUri| invalid(&error.to_string()))?;
        let scheme = uri
            .scheme_str()
            .map(str::to_ascii_lowercase)
            .ok_or_else(|| invalid("missing scheme"))?;
        let tls = match scheme.as_str() {
            "https" => true,
            "http" if allow_insecure_http => false,
            "http" => {
                return Err(invalid(
                    "plain http is rejected unless allow_insecure_http is set",
                ));
            }
            _ => return Err(invalid("scheme must be https or http")),
        };
        let authority = uri.authority().ok_or_else(|| invalid("missing host"))?;
        if authority.as_str().contains('@') {
            return Err(invalid("credentials in the URL are not supported"));
        }
        if authority.host().is_empty() {
            return Err(invalid("missing host"));
        }
        if !matches!(uri.path(), "" | "/") || uri.query().is_some() {
            return Err(invalid("a GDS URL has no path or query"));
        }
        Ok(Self {
            url: format!("{scheme}://{}", authority.as_str().to_ascii_lowercase()),
            host: authority.host().to_ascii_lowercase(),
            tls,
        })
    }

    /// The normalized URL, `scheme://authority`.
    pub fn as_str(&self) -> &str {
        &self.url
    }

    /// The host name or address, without the port.
    pub fn host(&self) -> &str {
        &self.host
    }

    /// Whether the URL uses TLS.
    pub fn is_tls(&self) -> bool {
        self.tls
    }
}

impl fmt::Display for GdsUrl {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.url)
    }
}

/// The ordered, de-duplicated list of configured GDS URLs. The first entry is
/// the primary, normally the global name `gds.<env>.internal`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct EndpointSet {
    urls: Vec<GdsUrl>,
}

impl EndpointSet {
    /// Parses URLs in order. Each value may itself be a comma separated list,
    /// as in a `GDS_URLS` environment variable. Empty values are skipped and
    /// repeated URLs keep their first position.
    pub fn parse<I, S>(urls: I, allow_insecure_http: bool) -> Result<Self, Error>
    where
        I: IntoIterator<Item = S>,
        S: AsRef<str>,
    {
        let mut parsed: Vec<GdsUrl> = Vec::new();
        for value in urls {
            for raw in value.as_ref().split(',') {
                if raw.trim().is_empty() {
                    continue;
                }
                let url = GdsUrl::parse(raw, allow_insecure_http)?;
                if !parsed.contains(&url) {
                    parsed.push(url);
                }
            }
        }
        if parsed.is_empty() {
            return Err(Error::NoEndpoints);
        }
        Ok(Self { urls: parsed })
    }

    /// The URLs in configured order.
    pub fn urls(&self) -> &[GdsUrl] {
        &self.urls
    }

    /// The first configured URL.
    pub fn primary(&self) -> &GdsUrl {
        &self.urls[0]
    }

    pub fn len(&self) -> usize {
        self.urls.len()
    }

    pub fn is_empty(&self) -> bool {
        self.urls.is_empty()
    }

    pub fn iter(&self) -> impl Iterator<Item = &GdsUrl> {
        self.urls.iter()
    }
}

/// TLS settings for `https` URLs. The system and Mozilla roots are always
/// trusted; `ca_certificate_pem` adds a private CA.
#[derive(Clone, Default)]
pub struct TlsOptions {
    /// PEM bundle of additional trusted CAs.
    pub ca_certificate_pem: Option<Vec<u8>>,
    /// PEM certificate chain and private key for mutual TLS.
    pub client_identity_pem: Option<(Vec<u8>, Vec<u8>)>,
    /// Server name for SNI and verification instead of each URL's host.
    pub server_name: Option<String>,
}

impl fmt::Debug for TlsOptions {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("TlsOptions")
            .field("ca_certificate_pem", &self.ca_certificate_pem.is_some())
            .field("client_identity_pem", &self.client_identity_pem.is_some())
            .field("server_name", &self.server_name)
            .finish()
    }
}

impl TlsOptions {
    /// Trusts the CAs in a PEM bundle in addition to the default roots.
    pub fn with_ca_pem(ca_certificate_pem: impl Into<Vec<u8>>) -> Self {
        Self {
            ca_certificate_pem: Some(ca_certificate_pem.into()),
            ..Self::default()
        }
    }

    /// Reads a PEM CA bundle from a file.
    pub fn from_ca_file(path: impl AsRef<std::path::Path>) -> Result<Self, Error> {
        let path = path.as_ref();
        let pem = std::fs::read(path)
            .map_err(|error| Error::Tls(format!("read CA bundle {}: {error}", path.display())))?;
        if pem.is_empty() {
            return Err(Error::Tls(format!("CA bundle {} is empty", path.display())));
        }
        Ok(Self::with_ca_pem(pem))
    }

    pub(crate) fn client_config(&self) -> ClientTlsConfig {
        let mut config = ClientTlsConfig::new().with_enabled_roots();
        if let Some(pem) = &self.ca_certificate_pem {
            config = config.ca_certificate(Certificate::from_pem(pem));
        }
        if let Some((cert, key)) = &self.client_identity_pem {
            config = config.identity(Identity::from_pem(cert, key));
        }
        if let Some(server_name) = &self.server_name {
            config = config.domain_name(server_name.clone());
        }
        config
    }
}

/// Exponential backoff with full jitter: attempt `n` waits a uniformly random
/// duration in `[0, min(max, initial * multiplier^n)]`.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Backoff {
    pub initial: Duration,
    pub max: Duration,
    pub multiplier: f64,
}

impl Default for Backoff {
    fn default() -> Self {
        Self {
            initial: Duration::from_millis(200),
            max: Duration::from_secs(30),
            multiplier: 2.0,
        }
    }
}

impl Backoff {
    /// The upper bound of the delay before retry `attempt` (0-based).
    pub fn ceiling(&self, attempt: u32) -> Duration {
        let multiplier = if self.multiplier.is_finite() && self.multiplier >= 1.0 {
            self.multiplier
        } else {
            1.0
        };
        let exponent = i32::try_from(attempt).unwrap_or(i32::MAX);
        let ceiling = self.initial.as_secs_f64() * multiplier.powi(exponent);
        let max = self.max.max(self.initial).as_secs_f64();
        Duration::from_secs_f64(if ceiling.is_finite() {
            ceiling.min(max)
        } else {
            max
        })
    }

    /// A random delay before retry `attempt` (0-based).
    pub fn delay(&self, attempt: u32) -> Duration {
        self.ceiling(attempt).mul_f64(rand::random::<f64>())
    }
}

/// Timeouts, retries and failover behaviour of a client.
#[derive(Debug, Clone)]
pub struct ConnectPolicy {
    /// Timeout of one connection attempt to one endpoint.
    pub connect_timeout: Duration,
    /// Deadline of one unary call attempt.
    pub rpc_timeout: Duration,
    /// How long the client stays on a non-primary endpoint before it tries
    /// the primary again.
    pub primary_retry_interval: Duration,
    /// Accept `http://` URLs. Off by default; for tests and development.
    pub allow_insecure_http: bool,
    /// Delay between retries and reconnects.
    pub backoff: Backoff,
    /// Attempts per unary call, including the first.
    pub max_attempts: u32,
    /// HTTP/2 keepalive ping interval; detects dead connections under
    /// long-lived watches.
    pub keepalive_interval: Duration,
    /// How long a keepalive ping may go unanswered.
    pub keepalive_timeout: Duration,
    /// User agent sent with every call.
    pub user_agent: Option<String>,
}

impl Default for ConnectPolicy {
    fn default() -> Self {
        Self {
            connect_timeout: Duration::from_secs(5),
            rpc_timeout: Duration::from_secs(10),
            primary_retry_interval: Duration::from_secs(300),
            allow_insecure_http: false,
            backoff: Backoff::default(),
            max_attempts: 4,
            keepalive_interval: Duration::from_secs(30),
            keepalive_timeout: Duration::from_secs(10),
            user_agent: None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_keeps_order_and_dedupes() {
        let set = EndpointSet::parse(
            [
                "https://gds.env.internal",
                "gds.us-east-1.env.internal:8443, https://GDS.env.internal/",
                "",
            ],
            false,
        )
        .expect("parse");
        let urls: Vec<&str> = set.iter().map(GdsUrl::as_str).collect();
        assert_eq!(
            urls,
            [
                "https://gds.env.internal",
                "https://gds.us-east-1.env.internal:8443"
            ]
        );
        assert_eq!(set.primary().host(), "gds.env.internal");
        assert!(set.primary().is_tls());
    }

    #[test]
    fn parse_rejects_http_unless_allowed() {
        let error = EndpointSet::parse(["http://127.0.0.1:9090"], false).unwrap_err();
        assert!(matches!(error, Error::InvalidUrl { .. }), "{error}");
        let set = EndpointSet::parse(["http://127.0.0.1:9090"], true).expect("parse");
        assert!(!set.primary().is_tls());
        assert_eq!(set.primary().as_str(), "http://127.0.0.1:9090");
    }

    #[test]
    fn parse_rejects_bad_urls() {
        for raw in [
            "ftp://gds",
            "https://gds/v1",
            "https://gds?x=1",
            "https://user:pw@gds",
            "https://",
        ] {
            assert!(
                matches!(GdsUrl::parse(raw, true), Err(Error::InvalidUrl { .. })),
                "{raw} should be rejected"
            );
        }
    }

    #[test]
    fn parse_empty_is_no_endpoints() {
        assert!(matches!(
            EndpointSet::parse(Vec::<String>::new(), false),
            Err(Error::NoEndpoints)
        ));
        assert!(matches!(
            EndpointSet::parse([" , "], false),
            Err(Error::NoEndpoints)
        ));
    }

    #[test]
    fn backoff_ceiling_grows_and_caps() {
        let backoff = Backoff {
            initial: Duration::from_millis(100),
            max: Duration::from_secs(1),
            multiplier: 2.0,
        };
        assert_eq!(backoff.ceiling(0), Duration::from_millis(100));
        assert_eq!(backoff.ceiling(1), Duration::from_millis(200));
        assert_eq!(backoff.ceiling(3), Duration::from_millis(800));
        assert_eq!(backoff.ceiling(4), Duration::from_secs(1));
        assert_eq!(backoff.ceiling(u32::MAX), Duration::from_secs(1));
        for attempt in 0..10 {
            assert!(backoff.delay(attempt) <= backoff.ceiling(attempt));
        }
    }

    #[test]
    fn tls_options_debug_hides_material() {
        let options = TlsOptions {
            ca_certificate_pem: Some(b"SENTINEL-CA".to_vec()),
            client_identity_pem: Some((b"SENTINEL-CERT".to_vec(), b"SENTINEL-KEY".to_vec())),
            server_name: None,
        };
        let debug = format!("{options:?}");
        assert!(!debug.contains("SENTINEL"), "{debug}");
    }
}
