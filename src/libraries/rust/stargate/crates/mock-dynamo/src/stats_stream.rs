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

use axum::body::{Body, Bytes};
use axum::extract::State;
use axum::response::Response;
use serde::Serialize;
use std::time::Duration;
use tokio::sync::broadcast;

use crate::AppState;

#[derive(Debug, Clone, Serialize)]
#[serde(tag = "type")]
pub(crate) enum StatsStreamEvent {
    #[serde(rename = "stats")]
    Stats {
        v: u8,
        request_id: String,
        model: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        tokens_processed: Option<u64>,
        #[serde(skip_serializing_if = "Option::is_none")]
        tokens_generated: Option<u64>,
        #[serde(skip_serializing_if = "is_false")]
        finished: bool,
    },
    #[serde(rename = "ping")]
    Ping { v: u8 },
}

fn is_false(value: &bool) -> bool {
    !*value
}

pub(crate) async fn stats_stream(State(state): State<AppState>) -> Response {
    let mut events = state.stats_events.subscribe();
    let stream = async_stream::stream! {
        let mut ping = tokio::time::interval(Duration::from_secs(1));
        ping.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
        loop {
            tokio::select! {
                event = events.recv() => {
                    match event {
                        Ok(event) => yield Ok::<Bytes, std::convert::Infallible>(ndjson_event(&event)),
                        Err(broadcast::error::RecvError::Lagged(_)) => continue,
                        Err(broadcast::error::RecvError::Closed) => break,
                    }
                }
                _ = ping.tick() => {
                    yield Ok::<Bytes, std::convert::Infallible>(ndjson_event(&StatsStreamEvent::Ping { v: 1 }));
                }
            }
        }
    };
    Response::builder()
        .header("content-type", "application/x-ndjson")
        .body(Body::from_stream(stream))
        .expect("stats stream response should build")
}

pub(crate) fn ndjson_event(event: &StatsStreamEvent) -> Bytes {
    let mut line = serde_json::to_vec(event).expect("stats stream event should serialize");
    line.push(b'\n');
    Bytes::from(line)
}

fn emit_stats_event(state: &AppState, event: StatsStreamEvent) {
    let _ = state.stats_events.send(event);
}

pub(crate) fn emit_request_stats_event(
    state: &AppState,
    request_id: &str,
    model: &str,
    tokens_processed: Option<usize>,
    tokens_generated: Option<usize>,
    finished: bool,
) {
    emit_stats_event(
        state,
        StatsStreamEvent::Stats {
            v: 1,
            request_id: request_id.to_string(),
            model: model.to_string(),
            tokens_processed: tokens_processed.map(|tokens| tokens as u64),
            tokens_generated: tokens_generated.map(|tokens| tokens as u64),
            finished,
        },
    );
}
