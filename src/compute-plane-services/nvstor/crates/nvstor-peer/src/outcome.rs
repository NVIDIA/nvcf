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

//! What a caller of `nvstor-peer fetch` can depend on: the exit code and
//! the `--json` line (`docs/peer-copy.md`, "Stable interface").
//!
//! Every failure maps to one [`ErrorKind`], and each kind to one exit
//! code. The mappings below are exhaustive matches without a wildcard, so
//! a new error variant does not compile until someone decides which kind
//! it is.

use std::time::Duration;

use nvstor_rdma::hydrate::HydrateError;
use nvstor_rdma::object::GetError;

use crate::fetch::{FetchError, FetchReport, OpenError};

/// This build's version: the release version it was built for, else the
/// crate's (see `build.rs`).
pub const VERSION: &str = env!("NVSTOR_PEER_VERSION");
/// The git commit this build came from, or "unknown" (see `build.rs`).
pub const COMMIT: &str = env!("NVSTOR_PEER_COMMIT");

/// The copy is complete.
pub const EXIT_OK: u8 = 0;

/// What kind of failure ended a run. The exit code and the JSON
/// `error_kind` follow from it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ErrorKind {
    /// Anything not covered below. Exit 1.
    Other,
    /// Bad command line or options. Exit 2.
    Usage,
    /// Every source refused this fetch's credentials. Exit 3.
    Auth,
    /// No source could serve the copy: all busy, unreachable, gone, or
    /// serving a manifest this build cannot use. Try the next tier.
    /// Exit 4.
    Unavailable,
    /// The data could not be copied intact: CRC mismatch after retries,
    /// a file changed on the source mid-copy, sources disagree on the
    /// content. Exit 5.
    Integrity,
    /// The destination failed: I/O error, disk full, permission, or
    /// locked by another fetch. Exit 6.
    Destination,
}

impl ErrorKind {
    pub fn exit_code(self) -> u8 {
        match self {
            ErrorKind::Other => 1,
            ErrorKind::Usage => 2,
            ErrorKind::Auth => 3,
            ErrorKind::Unavailable => 4,
            ErrorKind::Integrity => 5,
            ErrorKind::Destination => 6,
        }
    }

    /// The JSON `error_kind` value.
    pub fn as_str(self) -> &'static str {
        match self {
            ErrorKind::Other => "other",
            ErrorKind::Usage => "usage",
            ErrorKind::Auth => "auth",
            ErrorKind::Unavailable => "unavailable",
            ErrorKind::Integrity => "integrity",
            ErrorKind::Destination => "destination",
        }
    }
}

/// The kind of a failed fetch.
pub fn fetch_error_kind(e: &FetchError) -> ErrorKind {
    match e {
        // Every source failed to open, or failed mid-copy with no
        // replacement.
        FetchError::Connect(_) => ErrorKind::Unavailable,
        // Every source's manifest was unusable (malformed, too large, a
        // version this build does not read, or kept changing).
        FetchError::Manifest(_) => ErrorKind::Unavailable,
        FetchError::Get { error, .. } => get_error_kind(error),
        FetchError::Changed { .. } => ErrorKind::Integrity,
        FetchError::Io { .. } => ErrorKind::Destination,
        FetchError::Locked { .. } => ErrorKind::Destination,
        FetchError::Unavailable(_) => ErrorKind::Unavailable,
        FetchError::Mismatch(_) => ErrorKind::Integrity,
        FetchError::Refused(_) => ErrorKind::Auth,
        FetchError::Corrupt(_) => ErrorKind::Integrity,
        FetchError::Truncated { .. } => ErrorKind::Integrity,
        // Some files were not copied (paths too long for the transport,
        // or refused per file); everything else was.
        FetchError::Incomplete { .. } => ErrorKind::Other,
        // Options the fetch cannot run with (a bad --prefix, ...).
        FetchError::Device(_) => ErrorKind::Destination,
        FetchError::NotAll(_) => ErrorKind::Other,
        FetchError::Invalid(_) => ErrorKind::Usage,
    }
}

/// The kind of a range that failed for good.
pub fn get_error_kind(e: &GetError) -> ErrorKind {
    match e {
        // The file went away on the source mid-copy.
        GetError::NotFound => ErrorKind::Integrity,
        GetError::GenerationChanged => ErrorKind::Integrity,
        GetError::Corrupt { .. } => ErrorKind::Integrity,
        // Still busy, or the connection kept failing, after every retry.
        GetError::Busy => ErrorKind::Unavailable,
        GetError::Transport(_) => ErrorKind::Unavailable,
        GetError::Server(_) => ErrorKind::Other,
    }
}

/// The kind of a failure to open one source.
pub fn open_error_kind(e: &OpenError) -> ErrorKind {
    match e {
        OpenError::Busy(_) => ErrorKind::Unavailable,
        OpenError::Refused(_) => ErrorKind::Auth,
        OpenError::Failed(h) => hydrate_error_kind(h),
    }
}

/// The kind of a transport-level failure.
pub fn hydrate_error_kind(e: &HydrateError) -> ErrorKind {
    match e {
        HydrateError::Connect(_) => ErrorKind::Unavailable,
        HydrateError::Get { error, .. } => get_error_kind(error),
        HydrateError::Io(_) => ErrorKind::Destination,
        HydrateError::Invalid(_) => ErrorKind::Usage,
    }
}

/// `s` as a JSON string literal: `"` and `\` escaped, control characters
/// as `\u00XX`; everything else (all of Unicode) as is.
pub fn json_str(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// The end of a fetch, as the `--json` line reports it.
#[derive(Debug, Clone, PartialEq)]
pub struct Summary {
    /// `None` when the copy is complete.
    pub failure: Option<(ErrorKind, String)>,
    pub files: usize,
    pub bytes: u64,
    pub seconds: f64,
    pub retries: u64,
    pub reconnects: u64,
    pub failovers: u64,
    /// The source the copy started from; empty when none was reached.
    pub source: String,
    /// The content digest of the copy; empty when no manifest was had.
    pub content_digest: String,
    /// Which attributes the copy was given (`Attributes::describe`, as
    /// in the summary line); `None` when no copy was made.
    pub attributes: Option<String>,
    /// Set-id bits were kept, and on how many files and directories the
    /// kernel cleared them anyway.
    pub setid_kept: bool,
    pub setid_cleared: u64,
    /// A fetch into GPU memory (`fetch --gpu`): where the copy is.
    pub gpu: Option<GpuSummary>,
}

/// The GPU copy of a `fetch --gpu`.
#[derive(Clone, Debug, PartialEq)]
pub struct GpuSummary {
    /// The GPU's index (CUDA's order).
    pub device: u32,
    /// The buffer holding the files (landing slots included).
    pub bytes: u64,
    /// From the start of the fetch to every file in GPU memory.
    pub ready_seconds: f64,
}

impl Summary {
    /// A complete copy.
    pub fn complete(r: &FetchReport) -> Self {
        Self {
            failure: None,
            files: r.files,
            bytes: r.bytes,
            seconds: r.elapsed.as_secs_f64(),
            retries: r.retries,
            reconnects: r.reconnects,
            failovers: r.failovers,
            source: r.source.clone(),
            content_digest: r.content_digest.clone(),
            attributes: Some(r.attributes.describe()),
            setid_kept: r.setid_kept,
            setid_cleared: r.setid_cleared,
            gpu: None,
        }
    }

    /// A fetch that failed after `elapsed`, with what is known of it:
    /// an incomplete copy reports what it did copy.
    pub fn failed(e: &FetchError, elapsed: Duration) -> Self {
        let mut s = match e {
            FetchError::Incomplete { report, .. } => Self::complete(report),
            _ => Self::failure(ErrorKind::Other, String::new(), elapsed),
        };
        s.failure = Some((fetch_error_kind(e), e.to_string()));
        s.seconds = elapsed.as_secs_f64();
        s
    }

    /// The outcome of `fetch_from`, which took `elapsed`.
    pub fn of(result: &Result<FetchReport, FetchError>, elapsed: Duration) -> Self {
        match result {
            Ok(r) => Self::complete(r),
            Err(e) => Self::failed(e, elapsed),
        }
    }

    /// A run that failed before or after the copy (a usage error, the
    /// ready file), with nothing copied.
    pub fn failure(kind: ErrorKind, message: String, elapsed: Duration) -> Self {
        Self {
            failure: Some((kind, message)),
            files: 0,
            bytes: 0,
            seconds: elapsed.as_secs_f64(),
            retries: 0,
            reconnects: 0,
            failovers: 0,
            source: String::new(),
            content_digest: String::new(),
            attributes: None,
            setid_kept: false,
            setid_cleared: 0,
            gpu: None,
        }
    }

    pub fn ok(&self) -> bool {
        self.failure.is_none()
    }

    pub fn exit_code(&self) -> u8 {
        self.failure
            .as_ref()
            .map_or(EXIT_OK, |(k, _)| k.exit_code())
    }

    /// One JSON object, one line:
    /// `{"ok","exit_code","error","error_kind","files","bytes","seconds",
    /// "retries","reconnects","failovers","source","content_digest",
    /// "attributes","setid_kept","setid_cleared","gpu","gpu_bytes",
    /// "gpu_ready_seconds","version","commit"}`;
    /// `error` and `error_kind` are null on success, `attributes` when no
    /// copy was made, the `gpu*` fields unless the copy is in GPU memory
    /// (`fetch --gpu`: the GPU's index, the buffer's bytes, the seconds
    /// until every file was in it).
    pub fn to_json(&self) -> String {
        let (error, kind) = match &self.failure {
            None => ("null".to_string(), "null".to_string()),
            Some((k, m)) => (json_str(m), json_str(k.as_str())),
        };
        let seconds = if self.seconds.is_finite() {
            self.seconds
        } else {
            0.0
        };
        let gpu =
            |f: fn(&GpuSummary) -> String| self.gpu.as_ref().map_or_else(|| "null".to_string(), f);
        format!("{{\"ok\":{},\"exit_code\":{},\"error\":{error},\"error_kind\":{kind},\"files\":{},\"bytes\":{},\
                 \"seconds\":{seconds:.3},\"retries\":{},\"reconnects\":{},\"failovers\":{},\"source\":{},\
                 \"content_digest\":{},\"attributes\":{},\"setid_kept\":{},\"setid_cleared\":{},\"gpu\":{},\
                 \"gpu_bytes\":{},\"gpu_ready_seconds\":{},\"version\":{},\"commit\":{}}}",
                self.ok(), self.exit_code(), self.files, self.bytes, self.retries, self.reconnects, self.failovers,
                json_str(&self.source), json_str(&self.content_digest),
                self.attributes.as_deref().map_or_else(|| "null".to_string(), json_str),
                self.setid_kept, self.setid_cleared,
                gpu(|g| g.device.to_string()), gpu(|g| g.bytes.to_string()),
                gpu(|g| format!("{:.3}", if g.ready_seconds.is_finite() { g.ready_seconds } else { 0.0 })),
                json_str(VERSION), json_str(COMMIT))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;
    use std::path::{Path, PathBuf};
    use std::sync::Arc;
    use std::time::Instant;

    use nvstor_rdma::hydrate::RangeSource;
    use parking_lot::Mutex;

    use crate::export::DirExport;
    use crate::fetch::{fetch_from, Applied, Attributes, FetchOptions, SourceSet};

    fn report() -> FetchReport {
        FetchReport {
            changed_on: vec![],
            files: 3,
            bytes: 12288,
            ranges: 3,
            retries: 1,
            reconnects: 2,
            source: "s:1".into(),
            failovers: 1,
            content_digest: "abc".into(),
            attributes: Attributes {
                modes: Applied::Yes,
                owners: Applied::NoCapability("CAP_CHOWN"),
            },
            setid_kept: false,
            setid_cleared: 0,
            elapsed: Duration::from_millis(1500),
        }
    }

    /// Every variant, by name, with the kind and exit code it must have.
    #[test]
    fn every_fetch_error_has_its_exit_code() {
        let io = || std::io::Error::from_raw_os_error(libc::ENOSPC);
        let cases: Vec<(FetchError, ErrorKind, u8)> = vec![
            (FetchError::Connect("x".into()), ErrorKind::Unavailable, 4),
            (FetchError::Manifest("x".into()), ErrorKind::Unavailable, 4),
            (
                FetchError::Get {
                    path: "f".into(),
                    offset: 0,
                    error: GetError::Corrupt {
                        expected: 1,
                        actual: 2,
                    },
                },
                ErrorKind::Integrity,
                5,
            ),
            (
                FetchError::Get {
                    path: "f".into(),
                    offset: 0,
                    error: GetError::NotFound,
                },
                ErrorKind::Integrity,
                5,
            ),
            (
                FetchError::Get {
                    path: "f".into(),
                    offset: 0,
                    error: GetError::GenerationChanged,
                },
                ErrorKind::Integrity,
                5,
            ),
            (
                FetchError::Get {
                    path: "f".into(),
                    offset: 0,
                    error: GetError::Busy,
                },
                ErrorKind::Unavailable,
                4,
            ),
            (
                FetchError::Get {
                    path: "f".into(),
                    offset: 0,
                    error: GetError::Transport("x".into()),
                },
                ErrorKind::Unavailable,
                4,
            ),
            (
                FetchError::Get {
                    path: "f".into(),
                    offset: 0,
                    error: GetError::Server(9),
                },
                ErrorKind::Other,
                1,
            ),
            (
                FetchError::Changed { path: "f".into() },
                ErrorKind::Integrity,
                5,
            ),
            (
                FetchError::Io {
                    path: PathBuf::from("/d/f"),
                    error: io(),
                },
                ErrorKind::Destination,
                6,
            ),
            (
                FetchError::Locked {
                    dest: PathBuf::from("/d"),
                },
                ErrorKind::Destination,
                6,
            ),
            (
                FetchError::Unavailable("busy".into()),
                ErrorKind::Unavailable,
                4,
            ),
            (FetchError::Mismatch("x".into()), ErrorKind::Integrity, 5),
            (FetchError::Refused("x".into()), ErrorKind::Auth, 3),
            (FetchError::Corrupt("x".into()), ErrorKind::Integrity, 5),
            (
                FetchError::Truncated {
                    path: "f".into(),
                    offset: 4096,
                    size: 8192,
                },
                ErrorKind::Integrity,
                5,
            ),
            (FetchError::Device("x".into()), ErrorKind::Destination, 6),
            (FetchError::NotAll("x".into()), ErrorKind::Other, 1),
            (
                FetchError::Incomplete {
                    report: Box::new(report()),
                    failed: vec![("f".into(), "too long".into())],
                },
                ErrorKind::Other,
                1,
            ),
            (
                FetchError::Invalid("bad prefix".into()),
                ErrorKind::Usage,
                2,
            ),
        ];
        for (e, kind, code) in cases {
            assert_eq!(fetch_error_kind(&e), kind, "{e:?}");
            assert_eq!(kind.exit_code(), code, "{e:?}");
            assert_eq!(
                Summary::failed(&e, Duration::ZERO).exit_code(),
                code,
                "{e:?}"
            );
        }
    }

    #[test]
    fn every_open_error_has_its_kind() {
        assert_eq!(
            open_error_kind(&OpenError::Busy("x".into())),
            ErrorKind::Unavailable
        );
        assert_eq!(
            open_error_kind(&OpenError::Refused("x".into())),
            ErrorKind::Auth
        );
        assert_eq!(
            open_error_kind(&OpenError::Failed(HydrateError::Connect("x".into()))),
            ErrorKind::Unavailable
        );
        assert_eq!(
            open_error_kind(&OpenError::Failed(HydrateError::Get {
                offset: 0,
                error: GetError::Busy
            })),
            ErrorKind::Unavailable
        );
        assert_eq!(
            open_error_kind(&OpenError::Failed(HydrateError::Io(std::io::Error::other(
                "x"
            )))),
            ErrorKind::Destination
        );
        assert_eq!(
            open_error_kind(&OpenError::Failed(HydrateError::Invalid("x".into()))),
            ErrorKind::Usage
        );
    }

    #[test]
    fn exit_codes_and_names_are_distinct() {
        let all = [
            ErrorKind::Other,
            ErrorKind::Usage,
            ErrorKind::Auth,
            ErrorKind::Unavailable,
            ErrorKind::Integrity,
            ErrorKind::Destination,
        ];
        let codes: Vec<u8> = all.iter().map(|k| k.exit_code()).collect();
        assert_eq!(codes, [1, 2, 3, 4, 5, 6]);
        let names: Vec<&str> = all.iter().map(|k| k.as_str()).collect();
        assert_eq!(
            names,
            [
                "other",
                "usage",
                "auth",
                "unavailable",
                "integrity",
                "destination"
            ]
        );
        assert!(!codes.contains(&EXIT_OK));
    }

    #[test]
    fn json_strings_are_escaped() {
        assert_eq!(json_str("mlx5_0"), "\"mlx5_0\"");
        assert_eq!(json_str("a\"b\\c\nd"), "\"a\\\"b\\\\c\\u000ad\"");
        assert_eq!(
            json_str("\u{1}\t\r\u{1f}"),
            "\"\\u0001\\u0009\\u000d\\u001f\""
        );
        assert_eq!(
            json_str("é/ü ✓ \u{7f}"),
            "\"é/ü ✓ \u{7f}\"",
            "non-control characters pass through"
        );
        assert_eq!(json_str(""), "\"\"");
    }

    /// A minimal JSON reader for the tests: the object's top-level
    /// members, values as their raw text, strings unescaped. Fails on
    /// anything that is not one well-formed flat object.
    fn parse_flat_object(s: &str) -> Result<HashMap<String, String>, String> {
        let b: Vec<char> = s.chars().collect();
        let mut i = 0;
        let err = |m: &str, i: usize| format!("{m} at {i} in {s}");
        let string = |i: &mut usize| -> Result<String, String> {
            if b.get(*i) != Some(&'"') {
                return Err(err("expected string", *i));
            }
            *i += 1;
            let mut out = String::new();
            loop {
                match b.get(*i) {
                    None => return Err(err("unterminated string", *i)),
                    Some('"') => {
                        *i += 1;
                        return Ok(out);
                    }
                    Some('\\') => {
                        match b.get(*i + 1) {
                            Some('"') => out.push('"'),
                            Some('\\') => out.push('\\'),
                            Some('u') => {
                                let hex: String = b
                                    .get(*i + 2..*i + 6)
                                    .ok_or_else(|| err("short \\u", *i))?
                                    .iter()
                                    .collect();
                                let v = u32::from_str_radix(&hex, 16)
                                    .map_err(|_| err("bad \\u", *i))?;
                                out.push(char::from_u32(v).ok_or_else(|| err("bad \\u", *i))?);
                                *i += 4;
                            }
                            _ => return Err(err("bad escape", *i)),
                        }
                        *i += 2;
                    }
                    Some(c) if (*c as u32) < 0x20 => return Err(err("raw control character", *i)),
                    Some(c) => {
                        out.push(*c);
                        *i += 1;
                    }
                }
            }
        };
        if b.first() != Some(&'{') || b.last() != Some(&'}') {
            return Err(err("not an object", 0));
        }
        i += 1;
        let mut m = HashMap::new();
        loop {
            let key = string(&mut i)?;
            if b.get(i) != Some(&':') {
                return Err(err("expected :", i));
            }
            i += 1;
            let value = if b.get(i) == Some(&'"') {
                string(&mut i)?
            } else {
                let start = i;
                while i < b.len() && b[i] != ',' && b[i] != '}' {
                    i += 1;
                }
                let raw: String = b[start..i].iter().collect();
                let ok =
                    raw == "null" || raw == "true" || raw == "false" || raw.parse::<f64>().is_ok();
                if !ok {
                    return Err(err("bad literal", start));
                }
                raw
            };
            if m.insert(key.clone(), value).is_some() {
                return Err(err("duplicate key", i));
            }
            match b.get(i) {
                Some(',') => i += 1,
                Some('}') if i + 1 == b.len() => return Ok(m),
                _ => return Err(err("expected , or final }", i)),
            }
        }
    }

    const FIELDS: [&str; 20] = [
        "ok",
        "exit_code",
        "error",
        "error_kind",
        "files",
        "bytes",
        "seconds",
        "retries",
        "reconnects",
        "failovers",
        "source",
        "content_digest",
        "attributes",
        "setid_kept",
        "setid_cleared",
        "gpu",
        "gpu_bytes",
        "gpu_ready_seconds",
        "version",
        "commit",
    ];

    #[test]
    fn json_line_has_every_field_on_success_and_failure() {
        let ok = Summary::complete(&report()).to_json();
        assert!(!ok.contains('\n'), "{ok}");
        let m = parse_flat_object(&ok).expect("valid JSON");
        let mut keys: Vec<&str> = m.keys().map(String::as_str).collect();
        keys.sort();
        let mut want = FIELDS.to_vec();
        want.sort();
        assert_eq!(keys, want);
        assert_eq!(
            (
                m["ok"].as_str(),
                m["exit_code"].as_str(),
                m["error"].as_str(),
                m["error_kind"].as_str()
            ),
            ("true", "0", "null", "null")
        );
        assert_eq!(
            (
                m["files"].as_str(),
                m["bytes"].as_str(),
                m["seconds"].as_str()
            ),
            ("3", "12288", "1.500")
        );
        assert_eq!(
            (
                m["retries"].as_str(),
                m["reconnects"].as_str(),
                m["failovers"].as_str()
            ),
            ("1", "2", "1")
        );
        assert_eq!(
            (m["source"].as_str(), m["content_digest"].as_str()),
            ("s:1", "abc")
        );
        assert_eq!(m["attributes"], "mode+mtime (owner kept: no CAP_CHOWN)");
        assert_eq!(
            (m["setid_kept"].as_str(), m["setid_cleared"].as_str()),
            ("false", "0")
        );
        assert_eq!(
            (m["version"].as_str(), m["commit"].as_str()),
            (VERSION, COMMIT)
        );
        assert_eq!(
            (
                m["gpu"].as_str(),
                m["gpu_bytes"].as_str(),
                m["gpu_ready_seconds"].as_str()
            ),
            ("null", "null", "null")
        );

        // A copy in GPU memory.
        let mut s = Summary::complete(&report());
        s.gpu = Some(GpuSummary {
            device: 3,
            bytes: 1 << 30,
            ready_seconds: 0.25,
        });
        let m = parse_flat_object(&s.to_json()).expect("valid JSON");
        assert_eq!(m.len(), FIELDS.len());
        assert_eq!(
            (
                m["gpu"].as_str(),
                m["gpu_bytes"].as_str(),
                m["gpu_ready_seconds"].as_str()
            ),
            ("3", "1073741824", "0.250")
        );

        // A failure whose message needs escaping; counts of an
        // incomplete copy are kept.
        let e = FetchError::Incomplete {
            report: Box::new(report()),
            failed: vec![("a\"b\n".into(), "why\\".into())],
        };
        let line = Summary::failed(&e, Duration::from_millis(2250)).to_json();
        assert!(!line.contains('\n'), "{line}");
        let m = parse_flat_object(&line).expect("valid JSON");
        assert_eq!(m.len(), FIELDS.len());
        assert_eq!(
            (
                m["ok"].as_str(),
                m["exit_code"].as_str(),
                m["error_kind"].as_str()
            ),
            ("false", "1", "other")
        );
        assert_eq!(m["error"], e.to_string(), "the message round-trips");
        assert_eq!((m["files"].as_str(), m["seconds"].as_str()), ("3", "2.250"));

        // Nothing known: zeros and empty strings, still every field.
        let m = parse_flat_object(
            &Summary::failure(
                ErrorKind::Usage,
                "--dest is required".into(),
                Duration::ZERO,
            )
            .to_json(),
        )
        .expect("valid JSON");
        assert_eq!(m.len(), FIELDS.len());
        assert_eq!(
            (
                m["exit_code"].as_str(),
                m["error_kind"].as_str(),
                m["source"].as_str(),
                m["files"].as_str()
            ),
            ("2", "usage", "", "0")
        );
        assert_eq!(m["attributes"], "null", "no copy, no attributes");
    }

    #[test]
    fn version_and_commit_are_embedded() {
        assert!(!VERSION.is_empty());
        let hex = COMMIT.strip_suffix("-dirty").unwrap_or(COMMIT);
        assert!(
            COMMIT == "unknown"
                || ((7..=40).contains(&hex.len()) && hex.bytes().all(|b| b.is_ascii_hexdigit())),
            "{COMMIT}"
        );
    }

    /// In-process source over a `DirExport`, aligned like the RDMA pool;
    /// without one it serves 64 bytes of garbage for every key.
    struct Local {
        export: Option<Arc<DirExport>>,
        buf: Vec<u8>,
        off: usize,
        /// What file data (not the manifest) looks like from here.
        data: Data,
    }

    #[derive(Clone, Copy)]
    enum Data {
        Good,
        Corrupt,
        Truncated,
        /// Serves 0xEE bytes (CRC-clean, wrong) below this offset and
        /// ends every file there: an inconsistent source.
        LyingThenShort(u64),
        /// The same, taking this long to serve each wrong range.
        SlowLyingThenShort(u64, Duration),
    }

    const MAX: usize = 65536;

    impl Local {
        fn new(export: Option<&Arc<DirExport>>, data: Data) -> Self {
            let buf = vec![0xa5u8; MAX + 4096];
            let off = (4096 - buf.as_ptr() as usize % 4096) % 4096;
            Self {
                export: export.cloned(),
                buf,
                off,
                data,
            }
        }
    }

    impl RangeSource for Local {
        fn max_range(&self) -> usize {
            MAX
        }
        fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError> {
            let Some(export) = &self.export else {
                return Ok((64, 1));
            };
            let r = export
                .read(key, 0, &mut [])
                .map_err(|_| GetError::NotFound)?;
            Ok((r.size, r.generation))
        }
        fn fetch(
            &mut self,
            key: &str,
            offset: u64,
            generation: u128,
            len: usize,
        ) -> Result<&[u8], GetError> {
            let Some(export) = &self.export else {
                return Ok(&self.buf
                    [self.off..self.off + 64usize.saturating_sub(offset as usize).min(len)]);
            };
            if crate::manifest::prefix_of(key).is_none() {
                match self.data {
                    Data::Good => {}
                    Data::Corrupt => {
                        return Err(GetError::Corrupt {
                            expected: 1,
                            actual: 2,
                        })
                    }
                    Data::Truncated => return Ok(&[]),
                    Data::LyingThenShort(cut) | Data::SlowLyingThenShort(cut, _)
                        if offset >= cut =>
                    {
                        return Ok(&[])
                    }
                    Data::LyingThenShort(_) => {}
                    Data::SlowLyingThenShort(_, delay) => std::thread::sleep(delay),
                }
            }
            let buf = &mut self.buf[self.off..self.off + len.min(MAX)];
            let r = export
                .read(key, offset, buf)
                .map_err(|_| GetError::NotFound)?;
            if crate::manifest::prefix_of(key).is_none()
                && matches!(
                    self.data,
                    Data::LyingThenShort(_) | Data::SlowLyingThenShort(..)
                )
            {
                self.buf[self.off..self.off + r.len].fill(0xee);
            }
            if r.generation != generation {
                return Err(GetError::GenerationChanged);
            }
            Ok(&self.buf[self.off..self.off + r.len])
        }
        fn reconnect(&mut self) -> Result<(), HydrateError> {
            Ok(())
        }
    }

    /// Named sources: each serves the export, refuses the credentials,
    /// or is unreachable.
    enum Behaviour {
        Serve(Arc<DirExport>),
        Corrupt(Arc<DirExport>),
        Truncated(Arc<DirExport>),
        LyingThenShort(Arc<DirExport>, u64),
        SlowLyingThenShort(Arc<DirExport>, u64, Duration),
        Garbage,
        Refuse,
        Down,
    }

    struct Set {
        sources: Vec<(String, Behaviour)>,
        opens: Mutex<u32>,
    }

    impl SourceSet for Set {
        type Source = Local;
        fn candidates(&self) -> Vec<String> {
            self.sources.iter().map(|(n, _)| n.clone()).collect()
        }
        fn open(&self, name: &str) -> Result<Local, OpenError> {
            *self.opens.lock() += 1;
            match self.sources.iter().find(|(n, _)| n == name).map(|(_, b)| b) {
                Some(Behaviour::Serve(e)) => Ok(Local::new(Some(e), Data::Good)),
                Some(Behaviour::Corrupt(e)) => Ok(Local::new(Some(e), Data::Corrupt)),
                Some(Behaviour::Truncated(e)) => Ok(Local::new(Some(e), Data::Truncated)),
                Some(Behaviour::LyingThenShort(e, cut)) => {
                    Ok(Local::new(Some(e), Data::LyingThenShort(*cut)))
                }
                Some(Behaviour::SlowLyingThenShort(e, cut, d)) => {
                    Ok(Local::new(Some(e), Data::SlowLyingThenShort(*cut, *d)))
                }
                Some(Behaviour::Garbage) => Ok(Local::new(None, Data::Good)),
                Some(Behaviour::Refuse) => Err(OpenError::Refused(format!(
                    "{name}: {}",
                    nvstor_rdma::bootstrap::BootstrapError::Rejected(
                        nvstor_rdma::bootstrap::RejectReason::Credentials
                    )
                ))),
                Some(Behaviour::Down) | None => Err(OpenError::Failed(HydrateError::Connect(
                    format!("{name}: connection refused"),
                ))),
            }
        }
    }

    /// A seed under which the names are tried in this order.
    fn seed_ordering(order: &[&str]) -> u64 {
        let names: Vec<String> = order.iter().map(|n| n.to_string()).collect();
        (0..)
            .find(|&s| crate::fetch::source_order(s, names.clone()) == names)
            .expect("some seed")
    }

    /// A seed under which `first` is tried before every other name.
    fn seed_putting_first(first: &str, names: &[&str]) -> u64 {
        let names: Vec<String> = names.iter().map(|n| n.to_string()).collect();
        (0..)
            .find(|&s| crate::fetch::source_order(s, names.clone())[0] == first)
            .expect("some seed")
    }

    fn opts() -> FetchOptions {
        FetchOptions {
            connections: 2,
            range_size: MAX,
            max_retries: 2,
            retry_backoff: Duration::from_millis(1),
            direct_io: false,
            wait: Duration::from_secs(5),
            busy_backoff: Duration::from_millis(1),
            max_busy_backoff: Duration::from_millis(2),
            ..FetchOptions::default()
        }
    }

    fn tree(dir: &Path) -> Arc<DirExport> {
        std::fs::create_dir_all(dir.join("ckpt")).expect("mkdir");
        std::fs::write(dir.join("ckpt/a.bin"), vec![1u8; 100_000]).expect("write");
        std::fs::write(dir.join("ckpt/b \"q\".bin"), vec![2u8; 5]).expect("write");
        DirExport::open(dir).expect("export")
    }

    /// What the CLI does with a fetch: run it, time it, report it.
    fn run(set: &Set, dest: &Path) -> (Summary, String) {
        run_with(set, dest, &opts())
    }

    fn run_with(set: &Set, dest: &Path, opts: &FetchOptions) -> (Summary, String) {
        let started = Instant::now();
        let result = fetch_from(set, "ckpt", dest, opts);
        let s = Summary::of(&result, started.elapsed());
        let line = s.to_json();
        (s, line)
    }

    #[test]
    fn end_to_end_fetch_reports_success_as_json() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        let export = tree(src.path());
        let set = Set {
            sources: vec![("good:1".into(), Behaviour::Serve(export))],
            opens: Mutex::new(0),
        };
        let (s, line) = run(&set, &dst.path().join("out"));
        assert_eq!(s.exit_code(), 0, "{line}");
        let m = parse_flat_object(&line).expect("valid JSON");
        assert_eq!(
            (
                m["ok"].as_str(),
                m["exit_code"].as_str(),
                m["error"].as_str()
            ),
            ("true", "0", "null")
        );
        assert_eq!(
            (
                m["files"].as_str(),
                m["bytes"].as_str(),
                m["source"].as_str()
            ),
            ("2", "100005", "good:1")
        );
        assert_eq!(m["content_digest"].len(), 64, "{line}");
        assert_eq!(
            std::fs::read(dst.path().join("out/a.bin")).expect("copied"),
            vec![1u8; 100_000]
        );
    }

    // Credentials refused by every source: exit 3, without retrying a
    // refusal (one open per source).
    #[test]
    fn end_to_end_fetch_refused_everywhere_exits_3() {
        let dst = tempfile::tempdir().expect("tempdir");
        let set = Set {
            sources: vec![
                ("a:1".into(), Behaviour::Refuse),
                ("b:1".into(), Behaviour::Refuse),
            ],
            opens: Mutex::new(0),
        };
        let (s, line) = run(&set, &dst.path().join("out"));
        let m = parse_flat_object(&line).expect("valid JSON");
        assert_eq!(
            (
                m["ok"].as_str(),
                m["exit_code"].as_str(),
                m["error_kind"].as_str()
            ),
            ("false", "3", "auth"),
            "{line}"
        );
        assert!(
            m["error"].contains("a:1") && m["error"].contains("b:1"),
            "{line}"
        );
        assert_eq!((m["files"].as_str(), m["source"].as_str()), ("0", ""));
        assert_eq!(s.exit_code(), 3);
        assert_eq!(*set.opens.lock(), 2, "a refusal is final for that source");
    }

    // One source refuses, the other is down: not an auth problem alone,
    // so "unavailable" (the next tier may work).
    #[test]
    fn end_to_end_fetch_with_no_usable_source_exits_4() {
        let dst = tempfile::tempdir().expect("tempdir");
        let set = Set {
            sources: vec![
                ("a:1".into(), Behaviour::Refuse),
                ("b:1".into(), Behaviour::Down),
            ],
            opens: Mutex::new(0),
        };
        let (_, line) = run(&set, &dst.path().join("out"));
        let m = parse_flat_object(&line).expect("valid JSON");
        assert_eq!(
            (m["exit_code"].as_str(), m["error_kind"].as_str()),
            ("4", "unavailable"),
            "{line}"
        );
    }

    // One source refuses, the other serves an unreadable manifest: exit
    // 4, and the message names both.
    #[test]
    fn refused_and_bad_manifest_sources_are_both_reported() {
        let dst = tempfile::tempdir().expect("tempdir");
        let set = Set {
            sources: vec![
                ("refuse:1".into(), Behaviour::Refuse),
                ("garbage:1".into(), Behaviour::Garbage),
            ],
            opens: Mutex::new(0),
        };
        let (_, line) = run(&set, &dst.path().join("out"));
        let m = parse_flat_object(&line).expect("valid JSON");
        assert_eq!(
            (m["exit_code"].as_str(), m["error_kind"].as_str()),
            ("4", "unavailable"),
            "{line}"
        );
        assert!(
            m["error"].contains("refuse:1") && m["error"].contains("garbage:1"),
            "{line}"
        );
    }

    // A refusing source next to a good one: the fetch uses the good one.
    #[test]
    fn a_refusing_source_does_not_stop_a_good_one() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        let export = tree(src.path());
        let set = Set {
            sources: vec![
                ("bad:1".into(), Behaviour::Refuse),
                ("good:1".into(), Behaviour::Serve(export)),
            ],
            opens: Mutex::new(0),
        };
        let (s, line) = run(&set, &dst.path().join("out"));
        assert_eq!(s.exit_code(), 0, "{line}");
    }

    // Data that fails its CRC on every source, after every retry: exit 5,
    // not 4 (corrupt sources must not be counted as merely failed, which
    // would end the fetch with "no usable source").
    #[test]
    fn end_to_end_fetch_with_corrupt_data_everywhere_exits_5() {
        let src = tempfile::tempdir().expect("tempdir");
        let export = tree(src.path());
        for sources in [
            vec![("bad:1".to_string(), Behaviour::Corrupt(Arc::clone(&export)))],
            vec![
                ("bad:1".to_string(), Behaviour::Corrupt(Arc::clone(&export))),
                ("bad:2".to_string(), Behaviour::Corrupt(Arc::clone(&export))),
            ],
            vec![
                ("bad:1".to_string(), Behaviour::Corrupt(Arc::clone(&export))),
                ("down:1".to_string(), Behaviour::Down),
            ],
        ] {
            let dst = tempfile::tempdir().expect("tempdir");
            let set = Set {
                sources,
                opens: Mutex::new(0),
            };
            let (_, line) = run(&set, &dst.path().join("out"));
            let m = parse_flat_object(&line).expect("valid JSON");
            assert_eq!(
                (
                    m["ok"].as_str(),
                    m["exit_code"].as_str(),
                    m["error_kind"].as_str()
                ),
                ("false", "5", "integrity"),
                "{line}"
            );
            assert!(m["error"].contains("bad:1"), "{line}");
        }
    }

    // --wait-s 0: the wait runs out while the other source is merely
    // unreachable. Bad data from the first still decides the outcome.
    #[test]
    fn corrupt_source_wins_when_the_wait_runs_out() {
        let src = tempfile::tempdir().expect("tempdir");
        let export = tree(src.path());
        let o = FetchOptions {
            wait: Duration::ZERO,
            ..opts()
        };
        for down_first in [false, true] {
            let mut sources = vec![
                ("bad:1".to_string(), Behaviour::Corrupt(Arc::clone(&export))),
                ("down:1".to_string(), Behaviour::Down),
            ];
            if down_first {
                sources.reverse();
            }
            let dst = tempfile::tempdir().expect("tempdir");
            let set = Set {
                sources,
                opens: Mutex::new(0),
            };
            let (_, line) = run_with(&set, &dst.path().join("out"), &o);
            let m = parse_flat_object(&line).expect("valid JSON");
            assert_eq!(
                (m["exit_code"].as_str(), m["error_kind"].as_str()),
                ("5", "integrity"),
                "{line}"
            );
        }
    }

    // A truncated source next to a good one: the copy moves to the good
    // one and finishes intact.
    #[test]
    fn a_truncated_source_fails_over_to_a_good_one() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        let export = tree(src.path());
        for first in ["short:1", "good:1"] {
            let sources = vec![
                (
                    "short:1".to_string(),
                    Behaviour::Truncated(Arc::clone(&export)),
                ),
                ("good:1".to_string(), Behaviour::Serve(Arc::clone(&export))),
            ];
            let out = dst.path().join(first);
            let set = Set {
                sources,
                opens: Mutex::new(0),
            };
            let o = FetchOptions {
                seed: seed_putting_first(first, &["short:1", "good:1"]),
                ..opts()
            };
            let (s, line) = run_with(&set, &out, &o);
            assert_eq!(s.exit_code(), 0, "{line}");
            assert_eq!(
                std::fs::read(out.join("a.bin")).expect("copied"),
                vec![1u8; 100_000]
            );
        }
    }

    // A source that serves a file's first range wrong (but CRC-clean)
    // and then ends it short: after failing over, the whole file is
    // fetched again from the good source, not just the missing range.
    #[test]
    fn a_short_file_is_refetched_whole_from_the_next_source() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        let export = tree(src.path());
        let set = Set {
            sources: vec![
                (
                    "liar:1".into(),
                    Behaviour::LyingThenShort(Arc::clone(&export), MAX as u64),
                ),
                ("good:1".into(), Behaviour::Serve(Arc::clone(&export))),
            ],
            opens: Mutex::new(0),
        };
        // One worker, so the liar serves a.bin's first range before the
        // short second one.
        let o = FetchOptions {
            connections: 1,
            seed: seed_putting_first("liar:1", &["liar:1", "good:1"]),
            ..opts()
        };
        let (s, line) = run_with(&set, &dst.path().join("out"), &o);
        assert_eq!(s.exit_code(), 0, "{line}");
        assert_eq!(s.failovers, 1, "{line}");
        let a = std::fs::read(dst.path().join("out/a.bin")).expect("copied");
        assert!(
            a == vec![1u8; 100_000],
            "a.bin keeps {} bytes from the inconsistent source",
            a.iter().filter(|&&b| b == 0xee).count()
        );
    }

    // The inconsistent source already finished another file (CRC-clean,
    // wrong bytes) before it showed itself: that file is fetched again
    // whole from the good source too, and its record follows.
    #[test]
    fn files_finished_by_an_inconsistent_source_are_fetched_again() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        tree(src.path());
        // Listed (and copied, one worker) before a.bin, all below the cut.
        std::fs::write(src.path().join("ckpt/0first.bin"), vec![3u8; 9000]).expect("write");
        // Attributes the refetched copy must carry too (applied before the
        // rename, as for a first copy).
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(
            src.path().join("ckpt/0first.bin"),
            std::fs::Permissions::from_mode(0o640),
        )
        .expect("chmod");
        let mtime = std::time::SystemTime::UNIX_EPOCH + Duration::from_secs(1_600_000_000);
        std::fs::File::options()
            .write(true)
            .open(src.path().join("ckpt/0first.bin"))
            .expect("open")
            .set_modified(mtime)
            .expect("mtime");
        let export = DirExport::open(src.path()).expect("export again");
        let set = Set {
            sources: vec![
                (
                    "liar:1".into(),
                    Behaviour::LyingThenShort(Arc::clone(&export), MAX as u64),
                ),
                ("good:1".into(), Behaviour::Serve(Arc::clone(&export))),
            ],
            opens: Mutex::new(0),
        };
        let o = FetchOptions {
            connections: 1,
            seed: seed_putting_first("liar:1", &["liar:1", "good:1"]),
            ..opts()
        };
        let out = dst.path().join("out");
        let (s, line) = run_with(&set, &out, &o);
        assert_eq!(s.exit_code(), 0, "{line}");
        assert_eq!(
            std::fs::read(out.join("0first.bin")).expect("copied"),
            vec![3u8; 9000],
            "a file the inconsistent source finished is fetched again"
        );
        assert_eq!(
            std::fs::read(out.join("a.bin")).expect("copied"),
            vec![1u8; 100_000]
        );
        assert_eq!(
            std::fs::read(out.join("b \"q\".bin")).expect("copied"),
            vec![2u8; 5]
        );
        let meta = std::fs::metadata(out.join("0first.bin")).expect("stat");
        assert_eq!(
            meta.permissions().mode() & 0o7777,
            0o640,
            "mode applied to the refetched copy"
        );
        assert_eq!(
            meta.modified().expect("mtime"),
            mtime,
            "mtime applied to the refetched copy"
        );
        // The content record names the file once, with the generation of
        // the copy that replaced the first one.
        let record = crate::manifest::decode(
            &std::fs::read(out.join(crate::content::CONTENT_FILE)).expect("record"),
        )
        .expect("decode");
        let entries: Vec<_> = record.files().filter(|e| e.path == "0first.bin").collect();
        assert_eq!(entries.len(), 1, "{record:?}");
        let file = std::fs::File::open(out.join("0first.bin")).expect("open");
        let st = crate::dirfd::fstat(&std::os::fd::OwnedFd::from(file)).expect("fstat");
        assert_eq!(entries[0].generation, crate::export::generation_of(&st));
    }

    // The replacement source has the finished file with the same size but
    // other bytes (another content identity): it cannot refetch it, and
    // nothing else can, so the fetch ends as a mismatch (exit 5) instead
    // of recording its bytes under the original identity. With the file
    // identical there, the copy completes from it.
    #[test]
    fn a_finished_file_is_refetched_only_from_a_source_with_the_same_content() {
        for differs in [true, false] {
            let a_dir = tempfile::tempdir().expect("tempdir");
            tree(a_dir.path());
            std::fs::write(a_dir.path().join("ckpt/0first.bin"), vec![3u8; 9000]).expect("write");
            let a = DirExport::open(a_dir.path()).expect("export a");
            // B: a fetched replica of A (same identities), then 0first.bin
            // rewritten with other bytes of the same size.
            let b_dir = tempfile::tempdir().expect("tempdir");
            let seed = Set {
                sources: vec![("a:1".into(), Behaviour::Serve(Arc::clone(&a)))],
                opens: Mutex::new(0),
            };
            let (s, line) = run_with(
                &seed,
                &b_dir.path().join("ckpt"),
                &FetchOptions {
                    connections: 1,
                    ..opts()
                },
            );
            assert_eq!(s.exit_code(), 0, "{line}");
            if differs {
                std::fs::write(b_dir.path().join("ckpt/0first.bin"), vec![4u8; 9000])
                    .expect("rewrite");
            }
            // Served as `ckpt`, as `fetch --then-serve --prefix ckpt` does.
            let b = DirExport::open_as(&b_dir.path().join("ckpt"), "ckpt").expect("export b");

            let dst = tempfile::tempdir().expect("tempdir");
            let set = Set {
                sources: vec![
                    (
                        "liar:1".into(),
                        Behaviour::LyingThenShort(Arc::clone(&a), MAX as u64),
                    ),
                    ("b:1".into(), Behaviour::Serve(b)),
                ],
                opens: Mutex::new(0),
            };
            let o = FetchOptions {
                connections: 1,
                seed: seed_putting_first("liar:1", &["liar:1", "b:1"]),
                ..opts()
            };
            let out = dst.path().join("out");
            let (s, line) = run_with(&set, &out, &o);
            if differs {
                let m = parse_flat_object(&line).expect("valid JSON");
                assert_eq!(
                    (m["exit_code"].as_str(), m["error_kind"].as_str()),
                    ("5", "integrity"),
                    "{line}"
                );
                assert!(m["error"].contains("0first.bin"), "{line}");
                let got = std::fs::read(out.join("0first.bin")).unwrap_or_default();
                assert_ne!(
                    got,
                    vec![4u8; 9000],
                    "B's other bytes are never put in place"
                );
            } else {
                assert_eq!(s.exit_code(), 0, "{line}");
                assert_eq!(
                    std::fs::read(out.join("0first.bin")).expect("copied"),
                    vec![3u8; 9000]
                );
            }
        }
    }

    // Many workers meet the inconsistent source at once: each file is
    // refetched by one of them (no collision on its staging name), and
    // the copy is intact.
    #[test]
    fn many_workers_refetch_each_file_once() {
        let src = tempfile::tempdir().expect("tempdir");
        let export = tree(src.path());
        for i in 0..12u8 {
            std::fs::write(
                src.path().join(format!("ckpt/f{i:02}.bin")),
                vec![i; 3 * MAX + 100],
            )
            .expect("write");
        }
        let export = {
            drop(export);
            DirExport::open(src.path()).expect("export")
        };
        for round in 0..10 {
            let dst = tempfile::tempdir().expect("tempdir");
            let set = Set {
                sources: vec![
                    (
                        "liar:1".into(),
                        Behaviour::LyingThenShort(Arc::clone(&export), 2 * MAX as u64),
                    ),
                    ("good:1".into(), Behaviour::Serve(Arc::clone(&export))),
                ],
                opens: Mutex::new(0),
            };
            let o = FetchOptions {
                connections: 8,
                seed: seed_putting_first("liar:1", &["liar:1", "good:1"]),
                ..opts()
            };
            let out = dst.path().join("out");
            let (s, line) = run_with(&set, &out, &o);
            assert_eq!(s.exit_code(), 0, "round {round}: {line}");
            for i in 0..12u8 {
                assert_eq!(
                    std::fs::read(out.join(format!("f{i:02}.bin"))).expect("copied"),
                    vec![i; 3 * MAX + 100],
                    "round {round}: f{i:02}.bin"
                );
            }
            assert_eq!(
                std::fs::read(out.join("a.bin")).expect("copied"),
                vec![1u8; 100_000]
            );
        }
    }

    // Two inconsistent sources in a row, many workers: files the first
    // one wrote are being refetched from the second when it shows itself
    // too; each is refetched again from the good source by the worker
    // already on it (no staging collision, one finalize at a time).
    #[test]
    fn two_inconsistent_sources_in_a_row() {
        let src = tempfile::tempdir().expect("tempdir");
        tree(src.path());
        for i in 0..8u8 {
            std::fs::write(
                src.path().join(format!("ckpt/f{i}.bin")),
                vec![10 + i; 3 * MAX + 100],
            )
            .expect("write");
        }
        std::fs::write(src.path().join("ckpt/0first.bin"), vec![3u8; 9000]).expect("write");
        let export = DirExport::open(src.path()).expect("export");
        let names = ["liar1:1", "liar2:1", "good:1"];
        let o = FetchOptions {
            connections: 6,
            seed: seed_ordering(&names),
            ..opts()
        };
        for round in 0..10 {
            let dst = tempfile::tempdir().expect("tempdir");
            let set = Set {
                sources: vec![
                    (
                        "liar1:1".into(),
                        Behaviour::LyingThenShort(Arc::clone(&export), MAX as u64),
                    ),
                    (
                        "liar2:1".into(),
                        Behaviour::LyingThenShort(Arc::clone(&export), 2 * MAX as u64),
                    ),
                    ("good:1".into(), Behaviour::Serve(Arc::clone(&export))),
                ],
                opens: Mutex::new(0),
            };
            let out = dst.path().join("out");
            let (s, line) = run_with(&set, &out, &o);
            assert_eq!(s.exit_code(), 0, "round {round}: {line}");
            for i in 0..8u8 {
                assert_eq!(
                    std::fs::read(out.join(format!("f{i}.bin"))).expect("copied"),
                    vec![10 + i; 3 * MAX + 100],
                    "round {round}: f{i}.bin"
                );
            }
            assert_eq!(
                std::fs::read(out.join("0first.bin")).expect("copied"),
                vec![3u8; 9000],
                "round {round}"
            );
        }
    }

    // Three inconsistent sources in a row, many workers: every file ends
    // up entirely from the good source and the fetch never hangs (a
    // refetch that goes again holds a still-open file instead of waiting
    // for it to finish, which could be waiting for its own range; a raise
    // of the bar after a scanner's last check is taken up after it lets
    // go). Run under a watchdog: a hang fails the test instead of the run.
    #[test]
    fn three_inconsistent_sources_in_a_row_never_hang() {
        let (done_tx, done_rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let src = tempfile::tempdir().expect("tempdir");
            tree(src.path());
            for i in 0..10u8 {
                std::fs::write(
                    src.path().join(format!("ckpt/g{i}.bin")),
                    vec![60 + i; 3 * MAX + 77],
                )
                .expect("write");
            }
            let export = DirExport::open(src.path()).expect("export");
            let names = ["liar1:1", "liar2:1", "liar3:1", "good:1"];
            let o = FetchOptions {
                connections: 8,
                seed: seed_ordering(&names),
                ..opts()
            };
            for round in 0..12 {
                let dst = tempfile::tempdir().expect("tempdir");
                let set = Set {
                    sources: vec![
                        (
                            "liar1:1".into(),
                            Behaviour::LyingThenShort(Arc::clone(&export), MAX as u64),
                        ),
                        (
                            "liar2:1".into(),
                            Behaviour::LyingThenShort(Arc::clone(&export), 2 * MAX as u64),
                        ),
                        (
                            "liar3:1".into(),
                            Behaviour::LyingThenShort(Arc::clone(&export), 3 * MAX as u64),
                        ),
                        ("good:1".into(), Behaviour::Serve(Arc::clone(&export))),
                    ],
                    opens: Mutex::new(0),
                };
                let out = dst.path().join("out");
                let (s, line) = run_with(&set, &out, &o);
                assert_eq!(s.exit_code(), 0, "round {round}: {line}");
                for i in 0..10u8 {
                    assert!(
                        std::fs::read(out.join(format!("g{i}.bin"))).expect("copied")
                            == vec![60 + i; 3 * MAX + 77],
                        "round {round}: g{i}.bin"
                    );
                }
            }
            done_tx.send(()).expect("send");
        });
        done_rx
            .recv_timeout(Duration::from_secs(120))
            .expect("three bad sources in a row hung the fetch");
    }

    // A second inconsistent source caught while the scanner that is
    // refetching a finished file from it holds the file's refetch lock,
    // after the scanner's last look at the bar: the catcher's try_lock
    // fails and it leaves the file; the holder must take it up after it
    // lets go, or the file keeps the second source's bytes.
    #[test]
    fn a_raise_left_to_the_lock_holder_is_taken_up() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        std::fs::create_dir_all(src.path().join("ckpt")).expect("mkdir");
        std::fs::write(src.path().join("ckpt/0hold.bin"), vec![51u8; 1000]).expect("write");
        std::fs::write(src.path().join("ckpt/1t.bin"), vec![52u8; 2 * MAX]).expect("write");
        std::fs::write(src.path().join("ckpt/2g.bin"), vec![53u8; 3 * MAX]).expect("write");
        let export = DirExport::open(src.path()).expect("export");
        let names = ["liar1:1", "liar2:1", "good:1"];
        let set = Set {
            sources: vec![
                (
                    "liar1:1".into(),
                    Behaviour::LyingThenShort(Arc::clone(&export), MAX as u64),
                ),
                // Slow: caught only after the first scanner has refetched
                // 0hold.bin from it and sits in its window before unlocking.
                (
                    "liar2:1".into(),
                    Behaviour::SlowLyingThenShort(
                        Arc::clone(&export),
                        2 * MAX as u64,
                        Duration::from_millis(150),
                    ),
                ),
                ("good:1".into(), Behaviour::Serve(Arc::clone(&export))),
            ],
            opens: Mutex::new(0),
        };
        let o = FetchOptions {
            connections: 2,
            seed: seed_ordering(&names),
            ..opts()
        };
        crate::fetch::PAUSE_BEFORE_UNLOCK
            .lock()
            .push(("ckpt/0hold.bin".into(), Duration::from_millis(1200)));
        let out = dst.path().join("out");
        let (s, line) = run_with(&set, &out, &o);
        crate::fetch::PAUSE_BEFORE_UNLOCK
            .lock()
            .retain(|(k, _)| k != "ckpt/0hold.bin");
        assert_eq!(s.exit_code(), 0, "{line}");
        assert!(
            std::fs::read(out.join("0hold.bin")).expect("copied") == vec![51u8; 1000],
            "0hold.bin kept a bad source's bytes"
        );
        assert!(
            std::fs::read(out.join("1t.bin")).expect("copied") == vec![52u8; 2 * MAX],
            "1t.bin"
        );
        assert!(
            std::fs::read(out.join("2g.bin")).expect("copied") == vec![53u8; 3 * MAX],
            "2g.bin"
        );
    }

    // A worker still connected to a source when it is found inconsistent
    // goes on to other files with it (its slow wrong bytes arrive after
    // the scan for what the source wrote): those writes are refused (the
    // source is banned) and the files come entirely from the good source.
    #[test]
    fn writes_from_a_banned_source_never_land() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        // Two workers. One copies 0s.bin (slow, wrong) and then meets the
        // short second range of 1big.bin: the source is caught. The other
        // copies the first range of 1big.bin (slow) and then, still on
        // that source, the small files listed after it.
        std::fs::create_dir_all(src.path().join("ckpt")).expect("mkdir");
        std::fs::write(src.path().join("ckpt/0s.bin"), vec![30u8; 1000]).expect("write");
        std::fs::write(src.path().join("ckpt/1big.bin"), vec![5u8; 2 * MAX]).expect("write");
        for i in 2..7u8 {
            std::fs::write(
                src.path().join(format!("ckpt/{i}s.bin")),
                vec![30 + i; 1000],
            )
            .expect("write");
        }
        let export = DirExport::open(src.path()).expect("export");
        let set = Set {
            sources: vec![
                (
                    "liar:1".into(),
                    Behaviour::SlowLyingThenShort(
                        Arc::clone(&export),
                        MAX as u64,
                        Duration::from_millis(150),
                    ),
                ),
                ("good:1".into(), Behaviour::Serve(Arc::clone(&export))),
            ],
            opens: Mutex::new(0),
        };
        let o = FetchOptions {
            connections: 2,
            seed: seed_ordering(&["liar:1", "good:1"]),
            ..opts()
        };
        let out = dst.path().join("out");
        let (s, line) = run_with(&set, &out, &o);
        assert_eq!(s.exit_code(), 0, "{line}");
        assert_eq!(
            std::fs::read(out.join("1big.bin")).expect("copied"),
            vec![5u8; 2 * MAX]
        );
        assert_eq!(
            std::fs::read(out.join("0s.bin")).expect("copied"),
            vec![30u8; 1000]
        );
        for i in 2..7u8 {
            assert!(
                std::fs::read(out.join(format!("{i}s.bin"))).expect("copied") == vec![30 + i; 1000],
                "{i}s.bin"
            );
        }
    }

    // A file being refetched (from the second source) when that source is
    // caught too, by another worker: the refetching worker sees the bar
    // rise and fetches the file again, from the good source.
    #[test]
    fn a_refetch_in_progress_goes_again_when_its_source_is_caught() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        std::fs::create_dir_all(src.path().join("ckpt")).expect("mkdir");
        std::fs::write(src.path().join("ckpt/0loopf.bin"), vec![41u8; 1000]).expect("write");
        std::fs::write(src.path().join("ckpt/1t.bin"), vec![42u8; 2 * MAX]).expect("write");
        std::fs::write(src.path().join("ckpt/2g.bin"), vec![43u8; 3 * MAX]).expect("write");
        let export = DirExport::open(src.path()).expect("export");
        let names = ["liar1:1", "liar2:1", "good:1"];
        let set = Set {
            sources: vec![
                (
                    "liar1:1".into(),
                    Behaviour::LyingThenShort(Arc::clone(&export), MAX as u64),
                ),
                (
                    "liar2:1".into(),
                    Behaviour::LyingThenShort(Arc::clone(&export), 2 * MAX as u64),
                ),
                ("good:1".into(), Behaviour::Serve(Arc::clone(&export))),
            ],
            opens: Mutex::new(0),
        };
        let o = FetchOptions {
            connections: 2,
            seed: seed_ordering(&names),
            ..opts()
        };
        // Every write of 0loopf.bin takes 300 ms: the first source's is in
        // flight when that source is caught, the second source's when the
        // other worker catches it.
        crate::fetch::PAUSE_WRITE
            .lock()
            .push(("ckpt/0loopf.bin".into(), Duration::from_millis(300)));
        let out = dst.path().join("out");
        let (s, line) = run_with(&set, &out, &o);
        crate::fetch::PAUSE_WRITE
            .lock()
            .retain(|(k, _)| k != "ckpt/0loopf.bin");
        assert_eq!(s.exit_code(), 0, "{line}");
        assert!(
            std::fs::read(out.join("0loopf.bin")).expect("copied") == vec![41u8; 1000],
            "0loopf.bin"
        );
        assert!(
            std::fs::read(out.join("1t.bin")).expect("copied") == vec![42u8; 2 * MAX],
            "1t.bin"
        );
        assert!(
            std::fs::read(out.join("2g.bin")).expect("copied") == vec![43u8; 3 * MAX],
            "2g.bin"
        );
    }

    // A write from the inconsistent source admitted just before it is
    // banned, and still in progress during the scan: the file is marked
    // as written by that source before the bytes go down, so the scan
    // finds it, waits for the write and refetches the file.
    #[test]
    fn a_write_in_flight_during_the_scan_is_refetched() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        tree(src.path());
        std::fs::write(src.path().join("ckpt/0slowwrite.bin"), vec![7u8; 1000]).expect("write");
        std::fs::write(src.path().join("ckpt/1big.bin"), vec![8u8; 2 * MAX]).expect("write");
        let export = DirExport::open(src.path()).expect("export");
        let set = Set {
            sources: vec![
                (
                    "liar:1".into(),
                    Behaviour::LyingThenShort(Arc::clone(&export), MAX as u64),
                ),
                ("good:1".into(), Behaviour::Serve(Arc::clone(&export))),
            ],
            opens: Mutex::new(0),
        };
        let o = FetchOptions {
            connections: 2,
            seed: seed_ordering(&["liar:1", "good:1"]),
            ..opts()
        };
        crate::fetch::PAUSE_WRITE
            .lock()
            .push(("ckpt/0slowwrite.bin".into(), Duration::from_millis(400)));
        let out = dst.path().join("out");
        let (s, line) = run_with(&set, &out, &o);
        crate::fetch::PAUSE_WRITE
            .lock()
            .retain(|(k, _)| k != "ckpt/0slowwrite.bin");
        assert_eq!(s.exit_code(), 0, "{line}");
        assert_eq!(
            std::fs::read(out.join("0slowwrite.bin")).expect("copied"),
            vec![7u8; 1000],
            "the paused wrong write was found by the scan"
        );
        assert_eq!(
            std::fs::read(out.join("1big.bin")).expect("copied"),
            vec![8u8; 2 * MAX]
        );
    }

    // A corrupt source next to a good one: the copy finishes from the
    // good one.
    #[test]
    fn a_corrupt_source_fails_over_to_a_good_one() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        let export = tree(src.path());
        let set = Set {
            sources: vec![
                ("bad:1".into(), Behaviour::Corrupt(Arc::clone(&export))),
                ("good:1".into(), Behaviour::Serve(export)),
            ],
            opens: Mutex::new(0),
        };
        // The corrupt source is tried first.
        let o = FetchOptions {
            seed: seed_putting_first("bad:1", &["bad:1", "good:1"]),
            ..opts()
        };
        let (s, line) = run_with(&set, &dst.path().join("out"), &o);
        assert_eq!(s.exit_code(), 0, "{line}");
    }

    // A source whose files are shorter than its manifest says: exit 5.
    #[test]
    fn end_to_end_fetch_from_a_truncated_source_exits_5() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        let export = tree(src.path());
        let set = Set {
            sources: vec![("short:1".into(), Behaviour::Truncated(export))],
            opens: Mutex::new(0),
        };
        let (_, line) = run(&set, &dst.path().join("out"));
        let m = parse_flat_object(&line).expect("valid JSON");
        assert_eq!(
            (m["exit_code"].as_str(), m["error_kind"].as_str()),
            ("5", "integrity"),
            "{line}"
        );
        assert!(m["error"].contains("manifest says"), "{line}");
    }

    // The destination is a file: exit 6.
    #[test]
    fn end_to_end_fetch_into_a_bad_destination_exits_6() {
        let src = tempfile::tempdir().expect("tempdir");
        let dst = tempfile::tempdir().expect("tempdir");
        let export = tree(src.path());
        let file = dst.path().join("not-a-dir");
        std::fs::write(&file, b"x").expect("write");
        let set = Set {
            sources: vec![("good:1".into(), Behaviour::Serve(export))],
            opens: Mutex::new(0),
        };
        let (_, line) = run(&set, &file);
        let m = parse_flat_object(&line).expect("valid JSON");
        assert_eq!(
            (
                m["ok"].as_str(),
                m["exit_code"].as_str(),
                m["error_kind"].as_str()
            ),
            ("false", "6", "destination"),
            "{line}"
        );
    }

    #[test]
    fn the_test_json_reader_rejects_bad_json() {
        for bad in [
            "",
            "{",
            "{}x",
            "{\"a\":1,}",
            "{\"a\":nul}",
            "{\"a\":\"x\ny\"}",
            "{\"a\":1,\"a\":2}",
            "{a:1}",
        ] {
            assert!(parse_flat_object(bad).is_err(), "{bad:?}");
        }
    }
}
