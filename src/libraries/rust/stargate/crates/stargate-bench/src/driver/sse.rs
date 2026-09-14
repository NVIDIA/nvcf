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

use anyhow::{Context, Result, bail};
use serde_json::Value;

const MAX_SSE_EVENT_BYTES: usize = 1024 * 1024;
const UTF8_BOM: &[u8] = b"\xef\xbb\xbf";

#[derive(Default)]
pub(super) struct CompletionStream {
    event: Vec<u8>,
    bom_checked: bool,
    skip_lf: bool,
    output_tokens: Option<u64>,
    complete: bool,
}

impl CompletionStream {
    pub(super) fn push(&mut self, chunk: &[u8]) -> Result<bool> {
        let mut generated_output = false;
        for &byte in chunk {
            if self.complete {
                break;
            }
            // Treat CR, LF and CRLF as one line ending, including split CRLF.
            if std::mem::replace(&mut self.skip_lf, false) && byte == b'\n' {
                continue;
            }
            let byte = if byte == b'\r' {
                self.skip_lf = true;
                b'\n'
            } else {
                byte
            };
            if self.event.len() == MAX_SSE_EVENT_BYTES {
                bail!("upstream SSE event exceeded {MAX_SSE_EVENT_BYTES} bytes");
            }
            self.event.push(byte);
            if !self.bom_checked {
                if UTF8_BOM.starts_with(&self.event) {
                    if self.event.len() == UTF8_BOM.len() {
                        self.event.clear();
                        self.bom_checked = true;
                    }
                    continue;
                }
                self.bom_checked = true;
            }
            if byte == b'\n' && (self.event.len() == 1 || self.event.ends_with(b"\n\n")) {
                generated_output |= self.consume_event()?;
            }
        }
        Ok(generated_output)
    }

    fn consume_event(&mut self) -> Result<bool> {
        let text = std::str::from_utf8(&self.event).context("upstream SSE event is not UTF-8")?;
        let data = text
            .lines()
            .filter_map(|line| {
                line.strip_prefix("data:")
                    .map(|data| data.strip_prefix(' ').unwrap_or(data))
            })
            .collect::<Vec<_>>()
            .join("\n");
        self.event.clear();
        let data = data.trim();
        if data.is_empty() {
            return Ok(false);
        }
        if data == "[DONE]" {
            self.complete = true;
            return Ok(false);
        }
        let value: Value = serde_json::from_str(data).context("invalid upstream SSE JSON")?;
        if value.get("error").is_some_and(|error| !error.is_null()) {
            bail!("upstream returned an SSE error event");
        }
        let generated_output =
            value
                .get("choices")
                .and_then(Value::as_array)
                .is_some_and(|choices| {
                    choices.iter().any(|choice| {
                        let delta = &choice["delta"];
                        ["content", "reasoning_content", "reasoning"]
                            .iter()
                            .any(|field| {
                                delta[*field].as_str().is_some_and(|text| !text.is_empty())
                            })
                            || delta["tool_calls"].as_array().is_some_and(|calls| {
                                calls.iter().any(|call| {
                                    call["function"]["arguments"]
                                        .as_str()
                                        .is_some_and(|arguments| !arguments.is_empty())
                                })
                            })
                    })
                });
        if let Some(tokens) = value
            .pointer("/usage/completion_tokens")
            .or_else(|| value.get("output_tokens_so_far"))
            .filter(|tokens| !tokens.is_null())
        {
            self.output_tokens = Some(
                tokens
                    .as_u64()
                    .context("upstream output token usage is not an unsigned integer")?,
            );
        } else if generated_output {
            // A prior cumulative counter does not cover later uncounted output.
            self.output_tokens = None;
        }
        Ok(generated_output)
    }

    pub(super) fn output_tokens(&self) -> Option<u64> {
        self.output_tokens
    }

    pub(super) fn is_complete(&self) -> bool {
        self.complete
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cumulative_usage_must_cover_the_last_generated_output() {
        let mut stream = CompletionStream::default();
        stream.push(b"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}],\"output_tokens_so_far\":1}\n\n").unwrap();
        assert_eq!(stream.output_tokens(), Some(1));
        stream
            .push(b"data: {\"choices\":[{\"delta\":{\"content\":\" two\"}}]}\n\ndata: [DONE]\n\n")
            .unwrap();
        assert!(stream.is_complete());
        assert_eq!(stream.output_tokens(), None);
    }

    #[test]
    fn fragmented_events_preserve_text_and_observed_usage() {
        for separator in [
            "\n\n", "\r\r", "\r\n\r\n", "\n\r", "\n\r\n", "\r\n\n", "\r\n\r", "\r\r\n",
        ] {
            for prefix in ["", "\u{feff}"] {
                let event = "data: {\"choices\":[{\"delta\":{\"content\":\"\u{03bb}\"}}],\"usage\":{\"completion_tokens\":2}}";
                let body = format!("{prefix}{event}{separator}data: [DONE]{separator}");
                for chunk_size in [1, 2, body.len()] {
                    let mut stream = CompletionStream::default();
                    let mut output_events = 0;
                    for chunk in body.as_bytes().chunks(chunk_size) {
                        output_events += usize::from(stream.push(chunk).unwrap());
                    }
                    assert_eq!(output_events, 1, "{body:?}");
                    assert_eq!(stream.output_tokens(), Some(2), "{body:?}");
                    assert!(stream.is_complete(), "{body:?}");
                }
            }
        }
    }

    #[test]
    fn split_crlf_is_one_line_ending() {
        let mut stream = CompletionStream::default();
        stream.push(b"data: [DONE]\r").unwrap();
        assert!(!stream.is_complete());
        stream.push(b"\n").unwrap();
        assert!(!stream.is_complete());
        stream.push(b"\r").unwrap();
        assert!(stream.is_complete());
    }

    #[test]
    fn empty_events_do_not_accumulate_or_restart_bom_detection() {
        let mut stream = CompletionStream::default();
        assert!(!stream.push(&vec![b'\n'; MAX_SSE_EVENT_BYTES + 1]).unwrap());
        stream
            .push(b"\xef\xbb\xbfdata: {\"usage\":{\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
            .unwrap();
        assert!(stream.is_complete());
        assert_eq!(stream.output_tokens(), None);
    }

    #[test]
    fn metadata_and_empty_role_chunks_do_not_count_as_output() {
        let mut stream = CompletionStream::default();
        assert!(!stream.push(b": keepalive\n\ndata: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n").unwrap());
        assert!(!stream.is_complete());
        assert_eq!(stream.output_tokens(), None);
    }

    #[test]
    fn terminal_without_usage_preserves_unknown_output_count() {
        let mut stream = CompletionStream::default();
        stream.push(b"data: [DONE]\n\n").unwrap();
        assert!(stream.is_complete());
        assert_eq!(stream.output_tokens(), None);
    }

    #[test]
    fn malformed_or_failed_events_are_rejected() {
        for event in [
            b"data: {bad}\n\n".as_slice(),
            b"data: {\"error\":{\"message\":\"failed\"}}\n\n",
            b"data: {\"usage\":{\"completion_tokens\":-1}}\n\n",
        ] {
            assert!(CompletionStream::default().push(event).is_err());
        }
        assert!(
            CompletionStream::default()
                .push(&vec![b'x'; MAX_SSE_EVENT_BYTES + 1])
                .is_err()
        );
    }
}
