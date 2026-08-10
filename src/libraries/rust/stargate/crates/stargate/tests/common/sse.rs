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

use serde_json::Value;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SseEvent {
    pub event_name: Option<String>,
    pub data: String,
}

pub fn parse_sse_events(body: &str) -> Result<Vec<SseEvent>, String> {
    let body = body.replace("\r\n", "\n");
    if !body.is_empty() && !body.ends_with("\n\n") {
        return Err("SSE body ended with an incomplete event".to_string());
    }

    body.split("\n\n")
        .filter(|event| !event.is_empty())
        .filter_map(parse_sse_event)
        .collect()
}

fn parse_sse_event(raw_event: &str) -> Option<Result<SseEvent, String>> {
    let mut event_name = None;
    let mut data_lines = Vec::new();
    let mut saw_field = false;

    for line in raw_event.lines() {
        if line.starts_with(':') {
            continue;
        }
        let (field, value) = line.split_once(':').unwrap_or((line, ""));
        let value = value.strip_prefix(' ').unwrap_or(value);
        match field {
            "event" => {
                event_name = Some(value.to_string());
                saw_field = true;
            }
            "data" => {
                data_lines.push(value);
                saw_field = true;
            }
            _ => {}
        }
    }

    saw_field.then(|| {
        Ok(SseEvent {
            event_name,
            data: data_lines.join("\n"),
        })
    })
}

pub fn assert_sse_done(events: &[SseEvent]) {
    assert!(
        events
            .last()
            .is_some_and(|event| event.data.trim() == "[DONE]"),
        "SSE stream must terminate with [DONE], events: {events:#?}"
    );
}

pub fn json_events(events: &[SseEvent]) -> Vec<Value> {
    events
        .iter()
        .filter(|event| event.data.trim() != "[DONE]")
        .map(|event| {
            serde_json::from_str(&event.data).unwrap_or_else(|error| {
                panic!("SSE event data must be JSON; event={event:#?}; error={error}")
            })
        })
        .collect()
}

pub fn chat_completion_contents(events: &[SseEvent]) -> Vec<String> {
    json_events(events)
        .into_iter()
        .filter_map(|event| {
            event
                .pointer("/choices/0/delta/content")
                .and_then(Value::as_str)
                .map(ToOwned::to_owned)
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_crlf_multiline_json_events_and_done() {
        let events = parse_sse_events(
            ": keepalive\r\nevent: chunk\r\ndata: {\r\ndata: \"type\":\"chunk\"\r\ndata: }\r\n\r\ndata: [DONE]\r\n\r\n",
        )
        .expect("SSE fixture should parse");

        assert_eq!(
            events,
            vec![
                SseEvent {
                    event_name: Some("chunk".to_string()),
                    data: "{\n\"type\":\"chunk\"\n}".to_string(),
                },
                SseEvent {
                    event_name: None,
                    data: "[DONE]".to_string(),
                },
            ]
        );
        assert_eq!(
            json_events(&events),
            vec![serde_json::json!({"type": "chunk"})]
        );
        assert_sse_done(&events);
    }

    #[test]
    fn rejects_incomplete_final_event() {
        assert_eq!(
            parse_sse_events("data: {\"type\":\"chunk\"}"),
            Err("SSE body ended with an incomplete event".to_string())
        );
    }
}
