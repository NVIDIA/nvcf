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

use async_nats::{
    rustls::{CertificateError, Error as RustlsError},
    ClientError, ConnectError, ConnectErrorKind, Event, ServerError,
};
use std::error::Error as StdError;
use std::io;
use std::sync::atomic::{AtomicU8, Ordering};

/// Low-cardinality cause of a NATS connection failure, safe to use as a metric label.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u8)]
pub enum NatsFailureReason {
    None = 0,
    TlsCertExpired,
    TlsVerification,
    Tls,
    Authentication,
    Timeout,
    Connection,
    Other,
}

impl NatsFailureReason {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::None => "none",
            Self::TlsCertExpired => "tls_cert_expired",
            Self::TlsVerification => "tls_verification",
            Self::Tls => "tls",
            Self::Authentication => "authentication",
            Self::Timeout => "timeout",
            Self::Connection => "connection",
            Self::Other => "other",
        }
    }

    fn from_u8(value: u8) -> Self {
        match value {
            1 => Self::TlsCertExpired,
            2 => Self::TlsVerification,
            3 => Self::Tls,
            4 => Self::Authentication,
            5 => Self::Timeout,
            6 => Self::Connection,
            7 => Self::Other,
            _ => Self::None,
        }
    }
}

/// Most recent classified failure, shared between the event callback and the health check.
#[derive(Debug, Default)]
pub struct LastNatsFailure(AtomicU8);

impl LastNatsFailure {
    pub fn set(&self, reason: NatsFailureReason) {
        self.0.store(reason as u8, Ordering::Relaxed);
    }

    pub fn get(&self) -> NatsFailureReason {
        NatsFailureReason::from_u8(self.0.load(Ordering::Relaxed))
    }
}

pub fn classify_connect_error(err: &ConnectError) -> NatsFailureReason {
    let from_chain = classify_source_chain(err);
    match err.kind() {
        ConnectErrorKind::Authentication | ConnectErrorKind::AuthorizationViolation => {
            NatsFailureReason::Authentication
        }
        ConnectErrorKind::TimedOut => NatsFailureReason::Timeout,
        ConnectErrorKind::Tls => from_chain.unwrap_or(NatsFailureReason::Tls),
        ConnectErrorKind::Io => from_chain.unwrap_or(NatsFailureReason::Connection),
        ConnectErrorKind::Dns | ConnectErrorKind::ServerParse | ConnectErrorKind::MaxReconnects => {
            NatsFailureReason::Connection
        }
    }
}

pub fn classify_client_error(err: &ClientError) -> NatsFailureReason {
    match err {
        ClientError::MaxReconnects => NatsFailureReason::Connection,
        ClientError::Other(msg) => classify_message(msg),
    }
}

pub fn classify_server_error(err: &ServerError) -> NatsFailureReason {
    match err {
        ServerError::AuthorizationViolation => NatsFailureReason::Authentication,
        ServerError::SlowConsumer(_) => NatsFailureReason::Other,
        ServerError::Other(msg) => classify_message(msg),
    }
}

pub fn classify_event(event: &Event) -> Option<NatsFailureReason> {
    match event {
        Event::ClientError(err) => Some(classify_client_error(err)),
        Event::ServerError(err) => Some(classify_server_error(err)),
        _ => None,
    }
}

fn classify_source_chain(err: &(dyn StdError + 'static)) -> Option<NatsFailureReason> {
    let mut cause: Option<&(dyn StdError + 'static)> = Some(err);
    while let Some(e) = cause {
        if let Some(rustls_err) = e.downcast_ref::<RustlsError>() {
            return Some(classify_rustls(rustls_err));
        }
        if let Some(io_err) = e.downcast_ref::<io::Error>() {
            // io::Error::source() skips the wrapped error, so inspect it directly.
            if let Some(rustls_err) = io_err
                .get_ref()
                .and_then(|inner| inner.downcast_ref::<RustlsError>())
            {
                return Some(classify_rustls(rustls_err));
            }
            if io_err.kind() == io::ErrorKind::TimedOut {
                return Some(NatsFailureReason::Timeout);
            }
        }
        cause = e.source();
    }
    None
}

fn classify_rustls(err: &RustlsError) -> NatsFailureReason {
    match err {
        RustlsError::InvalidCertificate(
            CertificateError::Expired | CertificateError::ExpiredContext { .. },
        ) => NatsFailureReason::TlsCertExpired,
        RustlsError::InvalidCertificate(_) => NatsFailureReason::TlsVerification,
        _ => NatsFailureReason::Tls,
    }
}

// Runtime events only carry the error as a string.
fn classify_message(msg: &str) -> NatsFailureReason {
    let msg = msg.to_ascii_lowercase();
    if msg.contains("expired") {
        NatsFailureReason::TlsCertExpired
    } else if msg.contains("certificate") || msg.contains("unknownissuer") {
        NatsFailureReason::TlsVerification
    } else if msg.contains("tls") || msg.contains("handshake") {
        NatsFailureReason::Tls
    } else if msg.contains("authorization") || msg.contains("authentication") {
        NatsFailureReason::Authentication
    } else if msg.contains("timed out") || msg.contains("timeout") {
        NatsFailureReason::Timeout
    } else if msg.contains("connection") || msg.contains("io error") || msg.contains("dns") {
        NatsFailureReason::Connection
    } else {
        NatsFailureReason::Other
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tls_io_error(err: RustlsError) -> ConnectError {
        ConnectError::from(io::Error::new(io::ErrorKind::InvalidData, err))
    }

    #[test]
    fn expired_certificate_is_classified() {
        let err = tls_io_error(RustlsError::InvalidCertificate(CertificateError::Expired));
        assert_eq!(
            classify_connect_error(&err),
            NatsFailureReason::TlsCertExpired
        );
    }

    #[test]
    fn untrusted_certificate_is_verification_failure() {
        let err = tls_io_error(RustlsError::InvalidCertificate(
            CertificateError::UnknownIssuer,
        ));
        assert_eq!(
            classify_connect_error(&err),
            NatsFailureReason::TlsVerification
        );
    }

    #[test]
    fn other_tls_error_is_generic_tls() {
        let err = tls_io_error(RustlsError::HandshakeNotComplete);
        assert_eq!(classify_connect_error(&err), NatsFailureReason::Tls);
    }

    #[test]
    fn connect_kinds_are_classified() {
        let auth = ConnectError::new(ConnectErrorKind::AuthorizationViolation);
        assert_eq!(
            classify_connect_error(&auth),
            NatsFailureReason::Authentication
        );
        let timeout = ConnectError::new(ConnectErrorKind::TimedOut);
        assert_eq!(classify_connect_error(&timeout), NatsFailureReason::Timeout);
        let io_timeout = ConnectError::from(io::Error::from(io::ErrorKind::TimedOut));
        assert_eq!(
            classify_connect_error(&io_timeout),
            NatsFailureReason::Timeout
        );
        let refused = ConnectError::from(io::Error::from(io::ErrorKind::ConnectionRefused));
        assert_eq!(
            classify_connect_error(&refused),
            NatsFailureReason::Connection
        );
    }

    #[test]
    fn runtime_errors_are_classified() {
        assert_eq!(
            classify_server_error(&ServerError::AuthorizationViolation),
            NatsFailureReason::Authentication
        );
        assert_eq!(
            classify_client_error(&ClientError::Other(
                "IO error: invalid peer certificate: Expired".into()
            )),
            NatsFailureReason::TlsCertExpired
        );
        assert_eq!(
            classify_client_error(&ClientError::Other(
                "IO error: invalid peer certificate: UnknownIssuer".into()
            )),
            NatsFailureReason::TlsVerification
        );
        assert_eq!(
            classify_client_error(&ClientError::MaxReconnects),
            NatsFailureReason::Connection
        );
        assert_eq!(
            classify_server_error(&ServerError::Other("something odd".into())),
            NatsFailureReason::Other
        );
    }

    #[test]
    fn last_failure_round_trips() {
        let last = LastNatsFailure::default();
        assert_eq!(last.get(), NatsFailureReason::None);
        last.set(NatsFailureReason::Timeout);
        assert_eq!(last.get(), NatsFailureReason::Timeout);
    }
}
