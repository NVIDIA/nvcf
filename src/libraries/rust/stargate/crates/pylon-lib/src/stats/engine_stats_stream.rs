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

use std::borrow::Cow;
use std::fmt;
use std::str::FromStr;
use std::sync::Arc;
use std::time::Duration;

use bytes::Bytes;
use futures::{Stream, StreamExt};
use reqwest::StatusCode;
use serde::de::{IgnoredAny, MapAccess, SeqAccess, Visitor};
use serde::{Deserialize, Deserializer};
use tokio::time::Instant as TokioInstant;
use tokio_util::sync::CancellationToken;

use super::collector::{RequestCounterUpdate, StatsAggregatorUpdate, StatsUpdateSource};
use super::metrics::PylonMetrics;
use crate::PylonRuntimeState;
use crate::generated_request_id::generated_request_generation;
use stargate_runtime::OwnedTask;

const DEFAULT_ENGINE_STATS_STREAM_PATH: &str = "/pylon/v1/stats/stream";
const DEFAULT_INITIAL_RECONNECT_BACKOFF: Duration = Duration::from_millis(100);
const DEFAULT_MAX_RECONNECT_BACKOFF: Duration = Duration::from_secs(5);
const DEFAULT_MAX_LINE_BYTES: usize = 64 * 1024;
const HEADER_ACCEPT_NDJSON: &str = "application/x-ndjson";
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EngineStatsStreamMode {
    Auto,
    Required,
    Off,
}
impl EngineStatsStreamMode {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Auto => "auto",
            Self::Required => "required",
            Self::Off => "off",
        }
    }
}
impl fmt::Display for EngineStatsStreamMode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}
impl FromStr for EngineStatsStreamMode {
    type Err = ParseEngineStatsStreamModeError;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        match value {
            "auto" => Ok(Self::Auto),
            "required" => Ok(Self::Required),
            "off" => Ok(Self::Off),
            _ => Err(ParseEngineStatsStreamModeError),
        }
    }
}
#[derive(Debug, Clone, Copy, thiserror::Error)]
#[error("expected one of auto, required, off")]
pub struct ParseEngineStatsStreamModeError;
#[derive(Debug, Clone)]
pub struct EngineStatsStreamConfig {
    pub url: String,
    pub mode: EngineStatsStreamMode,
    pub initial_reconnect_backoff: Duration,
    pub max_reconnect_backoff: Duration,
    pub max_line_bytes: usize,
    pub metrics: Option<Arc<PylonMetrics>>,
    pub runtime_state: Option<PylonRuntimeState>,
}
impl EngineStatsStreamConfig {
    pub fn new(upstream_base_url: &str, path: &str, mode: EngineStatsStreamMode) -> Self {
        Self {
            url: crate::upstream_url::upstream_endpoint(upstream_base_url, path),
            mode,
            initial_reconnect_backoff: DEFAULT_INITIAL_RECONNECT_BACKOFF,
            max_reconnect_backoff: DEFAULT_MAX_RECONNECT_BACKOFF,
            max_line_bytes: DEFAULT_MAX_LINE_BYTES,
            metrics: None,
            runtime_state: None,
        }
    }
}
impl Default for EngineStatsStreamConfig {
    fn default() -> Self {
        Self::new(
            "http://127.0.0.1:8090",
            DEFAULT_ENGINE_STATS_STREAM_PATH,
            EngineStatsStreamMode::Auto,
        )
    }
}
owned_task_handle!(EngineStatsStreamHandle);

pub fn start_engine_stats_stream(
    config: EngineStatsStreamConfig,
    stats_update_tx: flume::Sender<StatsAggregatorUpdate>,
) -> Option<EngineStatsStreamHandle> {
    if config.mode == EngineStatsStreamMode::Off {
        return None;
    }
    let task = OwnedTask::spawn("engine stats stream", move |stop| {
        run_engine_stats_stream(config, stats_update_tx, stop)
    });
    Some(EngineStatsStreamHandle { task })
}
#[derive(Debug)]
pub(crate) enum ParsedEngineStatsEvent {
    Stats(RequestCounterUpdate),
    Ping,
}
#[derive(Debug, thiserror::Error)]
pub(crate) enum EngineStatsParseError {
    #[error("invalid JSON: {0}")]
    Json(#[from] serde_json::Error),
    #[error("event must be a JSON object")]
    NotObject,
    #[error("missing field {0}")]
    MissingField(&'static str),
    #[error("unsupported version {0}")]
    UnsupportedVersion(u64),
    #[error("invalid field {0}")]
    InvalidField(&'static str),
    #[error("unknown event type {0}")]
    UnknownType(String),
    #[error("stats event must include at least one counter unless finished=true")]
    EmptyStatsCounters,
}

pub(crate) fn parse_engine_stats_line(
    line: &[u8],
    observed_at: TokioInstant,
) -> Result<ParsedEngineStatsEvent, EngineStatsParseError> {
    if line.iter().find(|byte| !byte.is_ascii_whitespace()) != Some(&b'{') {
        serde_json::from_slice::<IgnoredAny>(line)?;
        return Err(EngineStatsParseError::NotObject);
    }
    let mut raw: RawEngineStatsEvent<'_> = serde_json::from_slice(line)?;
    let event_type = engine_stats_event_type(&mut raw)?;
    match event_type {
        "stats" => parse_stats_event(raw, observed_at),
        "ping" => Ok(ParsedEngineStatsEvent::Ping),
        other => Err(EngineStatsParseError::UnknownType(other.to_string())),
    }
}
fn engine_stats_event_type<'a>(
    raw: &'a mut RawEngineStatsEvent<'_>,
) -> Result<&'a str, EngineStatsParseError> {
    let version =
        optional_u64(raw.version.take(), "v")?.ok_or(EngineStatsParseError::MissingField("v"))?;
    if version != 1 {
        return Err(EngineStatsParseError::UnsupportedVersion(version));
    }
    match raw.event_type.as_ref() {
        Some(JsonScalar::String(value)) => Ok(value.as_ref()),
        Some(_) => Err(EngineStatsParseError::InvalidField("type")),
        None => Err(EngineStatsParseError::MissingField("type")),
    }
}
fn parse_stats_event(
    raw: RawEngineStatsEvent<'_>,
    observed_at: TokioInstant,
) -> Result<ParsedEngineStatsEvent, EngineStatsParseError> {
    let request_id = required_nonempty_string(raw.request_id, "request_id")?;
    let model_id = required_nonempty_string(raw.model, "model")?;
    let tokens_processed = optional_u64(raw.tokens_processed, "tokens_processed")?;
    let tokens_generated = optional_u64(raw.tokens_generated, "tokens_generated")?;
    let finished = match raw.finished {
        Some(JsonScalar::Bool(value)) => value,
        Some(_) => return Err(EngineStatsParseError::InvalidField("finished")),
        None => false,
    };
    if tokens_processed.is_none() && tokens_generated.is_none() && !finished {
        return Err(EngineStatsParseError::EmptyStatsCounters);
    }
    Ok(ParsedEngineStatsEvent::Stats(RequestCounterUpdate {
        source: StatsUpdateSource::EngineStatsStream,
        request_id,
        model_id,
        generation: None,
        tokens_processed,
        tokens_generated,
        finished,
        observed_at,
    }))
}
#[doc(hidden)]
pub fn parse_engine_stats_line_for_benchmark(line: &[u8], observed_at: TokioInstant) -> bool {
    parse_engine_stats_line(line, observed_at).is_ok()
}

#[derive(Default)]
struct RawEngineStatsEvent<'a> {
    version: Option<JsonScalar<'a>>,
    event_type: Option<JsonScalar<'a>>,
    request_id: Option<JsonScalar<'a>>,
    model: Option<JsonScalar<'a>>,
    tokens_processed: Option<JsonScalar<'a>>,
    tokens_generated: Option<JsonScalar<'a>>,
    finished: Option<JsonScalar<'a>>,
}

impl<'de> Deserialize<'de> for RawEngineStatsEvent<'de> {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        deserializer.deserialize_map(RawEngineStatsEventVisitor)
    }
}

struct RawEngineStatsEventVisitor;

impl<'de> Visitor<'de> for RawEngineStatsEventVisitor {
    type Value = RawEngineStatsEvent<'de>;

    fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("an engine stats JSON object")
    }

    fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
    where
        M: MapAccess<'de>,
    {
        let mut event = RawEngineStatsEvent::default();
        while let Some(key) = map.next_key::<Cow<'de, str>>()? {
            match key.as_ref() {
                "v" => event.version = Some(map.next_value()?),
                "type" => event.event_type = Some(map.next_value()?),
                "request_id" => event.request_id = Some(map.next_value()?),
                "model" => event.model = Some(map.next_value()?),
                "tokens_processed" => event.tokens_processed = Some(map.next_value()?),
                "tokens_generated" => event.tokens_generated = Some(map.next_value()?),
                "finished" => event.finished = Some(map.next_value()?),
                _ => {
                    map.next_value::<IgnoredAny>()?;
                }
            }
        }
        Ok(event)
    }
}

enum JsonScalar<'a> {
    String(Cow<'a, str>),
    Unsigned(u64),
    Bool(bool),
    Invalid,
}

impl<'de> Deserialize<'de> for JsonScalar<'de> {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        deserializer.deserialize_any(JsonScalarVisitor)
    }
}

struct JsonScalarVisitor;

impl<'de> Visitor<'de> for JsonScalarVisitor {
    type Value = JsonScalar<'de>;

    fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("a JSON scalar")
    }

    fn visit_borrowed_str<E>(self, value: &'de str) -> Result<Self::Value, E> {
        Ok(JsonScalar::String(Cow::Borrowed(value)))
    }

    fn visit_str<E>(self, value: &str) -> Result<Self::Value, E> {
        Ok(JsonScalar::String(Cow::Owned(value.to_string())))
    }

    fn visit_u64<E>(self, value: u64) -> Result<Self::Value, E> {
        Ok(JsonScalar::Unsigned(value))
    }

    fn visit_i64<E>(self, value: i64) -> Result<Self::Value, E> {
        Ok(u64::try_from(value)
            .map(JsonScalar::Unsigned)
            .unwrap_or(JsonScalar::Invalid))
    }

    fn visit_bool<E>(self, value: bool) -> Result<Self::Value, E> {
        Ok(JsonScalar::Bool(value))
    }

    fn visit_seq<A>(self, mut seq: A) -> Result<Self::Value, A::Error>
    where
        A: SeqAccess<'de>,
    {
        while seq.next_element::<IgnoredAny>()?.is_some() {}
        Ok(JsonScalar::Invalid)
    }

    fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
    where
        M: MapAccess<'de>,
    {
        while map.next_entry::<IgnoredAny, IgnoredAny>()?.is_some() {}
        Ok(JsonScalar::Invalid)
    }

    fn visit_f64<E>(self, _value: f64) -> Result<Self::Value, E> {
        Ok(JsonScalar::Invalid)
    }

    fn visit_unit<E>(self) -> Result<Self::Value, E> {
        Ok(JsonScalar::Invalid)
    }
}

fn optional_u64(
    value: Option<JsonScalar<'_>>,
    field: &'static str,
) -> Result<Option<u64>, EngineStatsParseError> {
    match value {
        Some(JsonScalar::Unsigned(value)) => Ok(Some(value)),
        Some(_) => Err(EngineStatsParseError::InvalidField(field)),
        None => Ok(None),
    }
}

fn required_nonempty_string(
    value: Option<JsonScalar<'_>>,
    field: &'static str,
) -> Result<String, EngineStatsParseError> {
    let value = match value {
        Some(JsonScalar::String(value)) => value,
        Some(_) => return Err(EngineStatsParseError::InvalidField(field)),
        None => return Err(EngineStatsParseError::MissingField(field)),
    };
    let value = value.trim();
    if value.is_empty() {
        return Err(EngineStatsParseError::InvalidField(field));
    }
    Ok(value.to_string())
}

async fn run_engine_stats_stream(
    config: EngineStatsStreamConfig,
    stats_update_tx: flume::Sender<StatsAggregatorUpdate>,
    stop: CancellationToken,
) {
    let client = reqwest::Client::new();
    let mut backoff = config.initial_reconnect_backoff;
    let mut valid_event_seen = false;
    loop {
        if stop.is_cancelled() {
            return;
        }
        let retry_reason = match read_stream_once(
            &config,
            &client,
            &stats_update_tx,
            &stop,
            &mut valid_event_seen,
        )
        .await
        {
            StreamReadOutcome::Stopped => return,
            StreamReadOutcome::Unsupported
                if config.mode == EngineStatsStreamMode::Auto && !valid_event_seen =>
            {
                tracing::warn!(
                    url = config.url,
                    "engine stats stream unsupported; using OpenAI fallback observation"
                );
                let _ = send_stats_update(
                    &stats_update_tx,
                    StatsAggregatorUpdate::EnableOpenAiFallback,
                    &stop,
                )
                .await;
                return;
            }
            StreamReadOutcome::Unsupported => "unsupported",
            StreamReadOutcome::Retry(reason) => reason,
        };
        if let Some(metrics) = &config.metrics {
            metrics.observe_engine_stats_reconnect(retry_reason);
        }

        if stop
            .run_until_cancelled(tokio::time::sleep(backoff))
            .await
            .is_none()
        {
            return;
        }
        backoff = (backoff * 2).min(config.max_reconnect_backoff);
    }
}

#[derive(Debug)]
enum StreamReadOutcome {
    Stopped,
    Unsupported,
    Retry(&'static str),
}

async fn read_stream_once(
    config: &EngineStatsStreamConfig,
    client: &reqwest::Client,
    stats_update_tx: &flume::Sender<StatsAggregatorUpdate>,
    stop: &CancellationToken,
    valid_event_seen: &mut bool,
) -> StreamReadOutcome {
    let response = match open_engine_stats_response(config, client, stop).await {
        Ok(response) => response,
        Err(outcome) => return outcome,
    };
    observe_connected(config, true);
    let outcome =
        drain_engine_stats_response(config, response, stats_update_tx, stop, valid_event_seen)
            .await;
    observe_connected(config, false);
    outcome
}
async fn drain_engine_stats_response(
    config: &EngineStatsStreamConfig,
    response: reqwest::Response,
    stats_update_tx: &flume::Sender<StatsAggregatorUpdate>,
    stop: &CancellationToken,
    valid_event_seen: &mut bool,
) -> StreamReadOutcome {
    let mut stream = response.bytes_stream();
    let mut line_buffer = Vec::with_capacity(1024);
    let mut discarding_oversized_line = false;
    loop {
        let chunk = match next_engine_stats_chunk(config, stop, &mut stream).await {
            Ok(chunk) => chunk,
            Err(outcome) => break outcome,
        };
        line_buffer.extend_from_slice(&chunk);
        if !process_buffered_engine_stats_lines(
            config,
            stats_update_tx,
            valid_event_seen,
            &mut line_buffer,
            &mut discarding_oversized_line,
            stop,
        )
        .await
        {
            break StreamReadOutcome::Stopped;
        }
    }
}
async fn open_engine_stats_response(
    config: &EngineStatsStreamConfig,
    client: &reqwest::Client,
    stop: &CancellationToken,
) -> Result<reqwest::Response, StreamReadOutcome> {
    let response = tokio::select! {
        _ = stop.cancelled() => return Err(StreamReadOutcome::Stopped),
        response = client
            .get(&config.url)
            .header(reqwest::header::ACCEPT, HEADER_ACCEPT_NDJSON)
            .send() => response,
    };
    let response = match response {
        Ok(response) => response,
        Err(error) => {
            tracing::warn!(url = config.url, error = %error, "engine stats stream connect failed");
            return Err(StreamReadOutcome::Retry("connect_error"));
        }
    };
    if matches!(
        response.status(),
        StatusCode::NOT_FOUND | StatusCode::METHOD_NOT_ALLOWED | StatusCode::NOT_IMPLEMENTED
    ) {
        tracing::warn!(
            url = config.url,
            status = %response.status(),
            "engine stats stream endpoint is unsupported"
        );
        return Err(StreamReadOutcome::Unsupported);
    }
    if !response.status().is_success() {
        tracing::warn!(
            url = config.url,
            status = %response.status(),
            "engine stats stream returned non-success status"
        );
        return Err(StreamReadOutcome::Retry("http_status"));
    }
    Ok(response)
}

async fn next_engine_stats_chunk<S>(
    config: &EngineStatsStreamConfig,
    stop: &CancellationToken,
    stream: &mut S,
) -> Result<Bytes, StreamReadOutcome>
where
    S: Stream<Item = Result<Bytes, reqwest::Error>> + Unpin,
{
    let chunk = tokio::select! {
        _ = stop.cancelled() => return Err(StreamReadOutcome::Stopped),
        chunk = stream.next() => chunk,
    };
    let chunk = chunk.ok_or(StreamReadOutcome::Retry("eof"))?;
    let chunk = chunk.map_err(|error| {
        tracing::warn!(url = config.url, error = %error, "engine stats stream read failed");
        StreamReadOutcome::Retry("read_error")
    })?;
    (!chunk.is_empty())
        .then_some(chunk)
        .ok_or(StreamReadOutcome::Retry("empty_chunk"))
}

async fn process_buffered_engine_stats_lines(
    config: &EngineStatsStreamConfig,
    stats_update_tx: &flume::Sender<StatsAggregatorUpdate>,
    valid_event_seen: &mut bool,
    line_buffer: &mut Vec<u8>,
    discarding_oversized_line: &mut bool,
    stop: &CancellationToken,
) -> bool {
    let mut consumed = if *discarding_oversized_line {
        let Some(newline_index) = memchr::memchr(b'\n', line_buffer) else {
            line_buffer.clear();
            return true;
        };
        *discarding_oversized_line = false;
        newline_index + 1
    } else {
        0
    };
    while let Some(relative_newline_index) = memchr::memchr(b'\n', &line_buffer[consumed..]) {
        let newline_index = consumed + relative_newline_index;
        if newline_index - consumed > config.max_line_bytes {
            observe_invalid(config, "line_too_large");
            consumed = newline_index + 1;
            continue;
        }
        let line_end = if line_buffer[consumed..newline_index].ends_with(b"\r") {
            newline_index - 1
        } else {
            newline_index
        };
        if line_end != consumed {
            let event =
                parse_engine_stats_line(&line_buffer[consumed..line_end], TokioInstant::now());
            if !emit_engine_stats_event(config, stats_update_tx, valid_event_seen, event, stop)
                .await
            {
                return false;
            }
        }
        consumed = newline_index + 1;
    }
    *discarding_oversized_line = compact_line_buffer(config, line_buffer, consumed);
    true
}

async fn emit_engine_stats_event(
    config: &EngineStatsStreamConfig,
    stats_update_tx: &flume::Sender<StatsAggregatorUpdate>,
    valid_event_seen: &mut bool,
    event: Result<ParsedEngineStatsEvent, EngineStatsParseError>,
    stop: &CancellationToken,
) -> bool {
    let event = match event {
        Ok(event) => event,
        Err(error) => {
            tracing::warn!(url = config.url, error = %error, "invalid engine stats event");
            observe_invalid(config, error.metric_reason());
            return true;
        }
    };
    *valid_event_seen = true;
    let (event_type, update) = match event {
        ParsedEngineStatsEvent::Stats(update) => ("stats", Some(update)),
        ParsedEngineStatsEvent::Ping => ("ping", None),
    };
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_stream_event(event_type);
    }
    match update {
        Some(mut update) => {
            update.generation = generated_request_generation(&update.request_id, &update.model_id)
                .or_else(|| {
                    config.runtime_state.as_ref().and_then(|runtime_state| {
                        runtime_state.request_generation(&update.request_id)
                    })
                });
            send_stats_update(
                stats_update_tx,
                StatsAggregatorUpdate::RequestCounters(update),
                stop,
            )
            .await
        }
        None => true,
    }
}

fn compact_line_buffer(
    config: &EngineStatsStreamConfig,
    line_buffer: &mut Vec<u8>,
    consumed: usize,
) -> bool {
    line_buffer.drain(..consumed).count();
    if line_buffer.len() > config.max_line_bytes {
        observe_invalid(config, "line_too_large");
        line_buffer.clear();
        true
    } else {
        false
    }
}

async fn send_stats_update(
    stats_update_tx: &flume::Sender<StatsAggregatorUpdate>,
    update: StatsAggregatorUpdate,
    stop: &CancellationToken,
) -> bool {
    match stats_update_tx.try_send(update) {
        Ok(()) => true,
        Err(flume::TrySendError::Full(update)) => stop
            .run_until_cancelled(stats_update_tx.send_async(update))
            .await
            .is_some_and(|result| result.is_ok()),
        Err(flume::TrySendError::Disconnected(_)) => false,
    }
}

impl EngineStatsParseError {
    fn metric_reason(&self) -> &'static str {
        match self {
            Self::Json(_) => "json",
            Self::NotObject => "not_object",
            Self::MissingField(_) => "missing_field",
            Self::UnsupportedVersion(_) => "version",
            Self::InvalidField(_) => "field",
            Self::UnknownType(_) => "type",
            Self::EmptyStatsCounters => "empty_stats",
        }
    }
}

fn observe_invalid(config: &EngineStatsStreamConfig, reason: &'static str) {
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_invalid_event(reason);
    }
}

fn observe_connected(config: &EngineStatsStreamConfig, connected: bool) {
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_stream_connected(config.mode.as_str(), connected);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{Router, routing::get};
    use std::sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    };
    use tokio::net::TcpListener;

    use crate::generated_request_id::{GeneratedRequestKind, next_generated_request_id};

    fn parse(line: &[u8]) -> Result<ParsedEngineStatsEvent, EngineStatsParseError> {
        parse_engine_stats_line(line, TokioInstant::now())
    }

    struct ProcessedLines {
        valid_event_seen: bool,
        updates: flume::Receiver<StatsAggregatorUpdate>,
    }

    impl ProcessedLines {
        fn assert_state(&self, valid_event_seen: bool) {
            assert_eq!(self.valid_event_seen, valid_event_seen);
        }

        fn assert_counter(self, context: &str) {
            self.assert_state(true);
            assert!(matches!(
                self.updates
                    .try_recv()
                    .unwrap_or_else(|_| panic!("{context}")),
                StatsAggregatorUpdate::RequestCounters(_)
            ));
        }

        fn assert_no_update(self, valid_event_seen: bool) {
            self.assert_state(valid_event_seen);
            assert!(self.updates.try_recv().is_err());
        }
    }

    async fn process_lines(
        config: &EngineStatsStreamConfig,
        chunks: impl IntoIterator<Item = impl AsRef<[u8]>>,
    ) -> ProcessedLines {
        let (tx, updates) = flume::bounded(4);
        let mut valid_event_seen = false;
        let mut remaining = Vec::new();
        let mut discarding_oversized_line = false;
        for chunk in chunks {
            remaining.extend_from_slice(chunk.as_ref());
            assert!(
                process_buffered_engine_stats_lines(
                    config,
                    &tx,
                    &mut valid_event_seen,
                    &mut remaining,
                    &mut discarding_oversized_line,
                    &CancellationToken::new(),
                )
                .await
            );
        }
        assert!(remaining.is_empty());
        assert!(!discarding_oversized_line);
        ProcessedLines {
            valid_event_seen,
            updates,
        }
    }

    async fn serve_stats(app: Router) -> (String, tokio::task::JoinHandle<()>) {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("listener should bind");
        let base_url = format!(
            "http://{}",
            listener.local_addr().expect("listener should have addr")
        );
        let server = tokio::spawn(async move {
            axum::serve(listener, app).await.expect("server should run");
        });
        (base_url, server)
    }

    fn reconnecting_config(base_url: &str, mode: EngineStatsStreamMode) -> EngineStatsStreamConfig {
        EngineStatsStreamConfig {
            initial_reconnect_backoff: Duration::from_millis(1),
            max_reconnect_backoff: Duration::from_millis(1),
            ..EngineStatsStreamConfig::new(base_url, "/pylon/v1/stats/stream", mode)
        }
    }

    async fn receive_update(
        updates: &flume::Receiver<StatsAggregatorUpdate>,
        context: &str,
    ) -> StatsAggregatorUpdate {
        tokio::time::timeout(Duration::from_secs(2), updates.recv_async())
            .await
            .unwrap_or_else(|_| panic!("{context}"))
            .expect("stats update should be sent")
    }

    #[test]
    fn parses_valid_engine_stats_events() {
        let event = parse(
            br#"{"v":1,"type":"stats","request_id":"req-1","model":"llama","tokens_processed":4096,"tokens_generated":128,"finished":true}"#,
        )
        .expect("stats event should parse");
        let ParsedEngineStatsEvent::Stats(update) = event else {
            panic!("expected request counters update");
        };
        assert_eq!(update.request_id, "req-1");
        assert_eq!(update.model_id, "llama");
        assert_eq!(update.tokens_processed, Some(4096));
        assert_eq!(update.tokens_generated, Some(128));
        assert!(update.finished);

        assert!(matches!(
            parse(br#"{"v":1,"type":"ping"}"#).expect("ping should parse"),
            ParsedEngineStatsEvent::Ping
        ));
    }

    #[tokio::test]
    async fn calibration_request_ids_route_engine_events_to_their_exact_generation() {
        let runtime_state = PylonRuntimeState::new(
            stargate_proto::pb::InferenceServerStatus::Active,
            &["model-a".to_string()],
        );
        let generation = runtime_state
            .current_generation("model-a")
            .expect("test generation should exist");
        let request_id = next_generated_request_id(GeneratedRequestKind::Calibration, &generation);
        let config = EngineStatsStreamConfig {
            runtime_state: Some(runtime_state),
            ..EngineStatsStreamConfig::default()
        };
        let line = format!(
            "{{\"v\":1,\"type\":\"stats\",\"request_id\":\"{request_id}\",\"model\":\"model-a\",\"tokens_processed\":64}}\n"
        );

        let processed = process_lines(&config, [line]).await;
        let StatsAggregatorUpdate::RequestCounters(update) = processed
            .updates
            .try_recv()
            .expect("calibration event should enter the ordinary stats pipeline")
        else {
            panic!("expected request counters update");
        };

        assert_eq!(update.generation, Some(generation));
    }

    #[test]
    fn rejects_invalid_engine_stats_events() {
        assert!(matches!(
            parse(br#"{"v":2,"type":"ping"}"#).unwrap_err(),
            EngineStatsParseError::UnsupportedVersion(2)
        ));
        assert!(matches!(
            parse(br#"{"v":1,"type":"nope"}"#).unwrap_err(),
            EngineStatsParseError::UnknownType(_)
        ));
        assert!(matches!(
            parse(br#"{"v":1,"type":"stats","request_id":"req-1","model":"llama"}"#).unwrap_err(),
            EngineStatsParseError::EmptyStatsCounters
        ));
        assert!(matches!(
            parse(
                br#"{"v":1,"type":"stats","request_id":"req-1","model":"llama","tokens_processed":-1}"#,
            )
            .unwrap_err(),
            EngineStatsParseError::InvalidField("tokens_processed")
        ));
        assert!(matches!(
            parse(
                br#"{"v":1,"type":"stats","request_id":"req-1","model":"llama","tokens_generated":1.5}"#,
            )
            .unwrap_err(),
            EngineStatsParseError::InvalidField("tokens_generated")
        ));

        for (json, field) in [
            (br#"{"v":"1","type":"ping"}"#.as_slice(), "v"),
            (br#"{"v":"\u0031","type":"ping"}"#.as_slice(), "v"),
            (br#"{"v":true,"type":"ping"}"#.as_slice(), "v"),
            (br#"{"v":{},"type":"ping"}"#.as_slice(), "v"),
            (br#"{"v":1,"type":1}"#.as_slice(), "type"),
            (br#"{"v":1,"type":-1}"#.as_slice(), "type"),
            (
                br#"{"v":1,"type":"stats","request_id":"req-1","model":"llama","tokens_processed":true}"#.as_slice(),
                "tokens_processed",
            ),
            (
                br#"{"v":1,"type":"stats","request_id":"req-1","model":"llama","finished":"true"}"#.as_slice(),
                "finished",
            ),
        ] {
            assert!(matches!(
                parse(json).unwrap_err(),
                EngineStatsParseError::InvalidField(actual) if actual == field
            ));
        }
    }

    #[test]
    fn engine_stats_parser_enforces_json_boundary_policy() {
        assert!(matches!(
            parse(br#"[1,2,3]"#).unwrap_err(),
            EngineStatsParseError::NotObject
        ));
        assert!(matches!(
            parse(br#"{"v":1,"type":"ping"} trailing"#).unwrap_err(),
            EngineStatsParseError::Json(_)
        ));
        assert!(matches!(
            parse(br#"{"v":1,"type":"ping","ignored":{"nested":true}}"#)
                .expect("unknown fields should remain forward-compatible"),
            ParsedEngineStatsEvent::Ping
        ));
        assert!(matches!(
            parse(br#"{"v":2,"v":1,"type":"unknown","type":"ping"}"#)
                .expect("recognized duplicate fields should retain last-value-wins behavior"),
            ParsedEngineStatsEvent::Ping
        ));

        let event = parse(
            br#"{"v":1,"type":"stats","request_id":"req-\u0031","model":"ll\u0061ma","tokens_generated":1,"tokens_generated":2}"#,
        )
        .expect("escaped strings and duplicate counters should parse");
        assert!(matches!(
            event,
            ParsedEngineStatsEvent::Stats(update)
                if update.request_id == "req-1"
                    && update.model_id == "llama"
                    && update.tokens_generated == Some(2)
        ));

        assert!(matches!(
            parse(br#"{"v":1,"type":"stats","request_id":null,"model":"m","finished":true}"#)
                .unwrap_err(),
            EngineStatsParseError::InvalidField("request_id")
        ));

        let nested_values = "0,".repeat(16_000);
        let nested_invalid = format!(
            r#"{{"v":1,"type":"stats","request_id":"req-1","model":"m","tokens_generated":[{nested_values}0]}}"#
        );
        assert!(matches!(
            parse(nested_invalid.as_bytes()).unwrap_err(),
            EngineStatsParseError::InvalidField("tokens_generated")
        ));
    }

    #[tokio::test]
    async fn next_engine_stats_chunk_reads_before_cancellation() {
        let config = EngineStatsStreamConfig::default();
        let stop = CancellationToken::new();
        let (chunk_tx, chunk_rx) = tokio::sync::mpsc::channel(1);
        let reader = tokio::spawn(async move {
            let mut stream = tokio_stream::wrappers::ReceiverStream::new(chunk_rx);
            next_engine_stats_chunk(&config, &stop, &mut stream).await
        });
        tokio::task::yield_now().await;
        chunk_tx
            .send(Ok::<_, reqwest::Error>(Bytes::from_static(b"chunk")))
            .await
            .expect("chunk receiver should stay alive");
        let chunk = match reader.await.expect("reader task should join") {
            Ok(chunk) => chunk,
            Err(_) => panic!("uncancelled token should not stop chunk reads"),
        };

        assert_eq!(chunk, Bytes::from_static(b"chunk"));
    }

    #[tokio::test]
    async fn stats_update_send_wakes_on_cancellation_when_channel_is_full() {
        let (tx, _rx) = flume::bounded(1);
        tx.send(StatsAggregatorUpdate::EnableOpenAiFallback)
            .expect("seed update should fill channel");
        let stop = CancellationToken::new();
        let task_stop = stop.clone();

        let task = tokio::spawn(async move {
            send_stats_update(&tx, StatsAggregatorUpdate::EnableOpenAiFallback, &task_stop).await
        });
        tokio::task::yield_now().await;
        stop.cancel();

        let sent = tokio::time::timeout(Duration::from_secs(1), task)
            .await
            .expect("stats update send should wake on cancellation")
            .expect("stats update send should not panic");
        assert!(!sent);
    }

    #[tokio::test]
    async fn empty_engine_stats_chunks_force_reconnect_without_spinning() {
        let config = EngineStatsStreamConfig::default();
        let stop = CancellationToken::new();
        let mut empty_stream = futures::stream::iter([Ok::<_, reqwest::Error>(Bytes::new())]);

        assert!(matches!(
            next_engine_stats_chunk(&config, &stop, &mut empty_stream).await,
            Err(StreamReadOutcome::Retry("empty_chunk"))
        ));
    }

    #[tokio::test]
    async fn engine_stats_line_length_allows_exact_limit_only() {
        let line =
            br#"{"v":1,"type":"stats","request_id":"req-1","model":"model-a","tokens_generated":1}"#;
        let mut config = EngineStatsStreamConfig {
            max_line_bytes: line.len(),
            ..Default::default()
        };
        process_lines(&config, [line.as_slice(), b"\n"])
            .await
            .assert_counter("exact-limit line should publish stats");

        process_lines(
            &config,
            [br#"{"v":1,"type":"ping"}"#.as_slice(), b"\n", line, b"\n"],
        )
        .await
        .assert_counter("exact-limit line should publish after a prior line");

        config.max_line_bytes = line.len() - 1;
        process_lines(&config, [line.as_slice(), b"\n"])
            .await
            .assert_no_update(false);

        let metrics = PylonMetrics::new().expect("metrics should initialize");
        config.metrics = Some(metrics.clone());
        process_lines(
            &config,
            [line.as_slice(), b"\n", br#"{"v":1,"type":"ping"}"#, b"\n"],
        )
        .await
        .assert_no_update(true);
        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(
            r#"pylon_engine_stats_stream_invalid_events_total{reason="line_too_large"} 1"#
        ));
        assert!(body.contains(r#"pylon_engine_stats_stream_events_total{type="ping"} 1"#));
        assert!(!body.contains(r#"pylon_engine_stats_stream_invalid_events_total{reason="json"}"#));

        let suffix = br#"{"v":1,"type":"stats","request_id":"suffix","model":"model-a","tokens_generated":1}"#;
        let later = br#"{"v":1,"type":"stats","request_id":"later","model":"model-a","tokens_generated":1}"#;
        let max_line_bytes = suffix.len();
        let oversized_prefix = vec![b'x'; max_line_bytes + 1];
        let discard_then_resume = [suffix.as_slice(), b"\n", later.as_slice(), b"\n"].concat();
        for chunks in [
            vec![oversized_prefix.as_slice(), discard_then_resume.as_slice()],
            vec![
                &oversized_prefix[..max_line_bytes],
                &oversized_prefix[max_line_bytes..],
                &suffix[..7],
                &suffix[7..],
                b"\n",
                later.as_slice(),
                b"\n",
            ],
        ] {
            let metrics = PylonMetrics::new().expect("metrics should initialize");
            let config = EngineStatsStreamConfig {
                max_line_bytes,
                metrics: Some(metrics.clone()),
                ..Default::default()
            };
            let processed = process_lines(&config, chunks).await;
            processed.assert_state(true);
            let updates = processed.updates.try_iter().collect::<Vec<_>>();
            assert!(matches!(
                updates.as_slice(),
                [StatsAggregatorUpdate::RequestCounters(update)] if update.request_id == "later"
            ));
            let body = metrics.gather_text().expect("metrics should encode");
            assert!(body.contains(
                r#"pylon_engine_stats_stream_invalid_events_total{reason="line_too_large"} 1"#
            ));
            assert!(body.contains(r#"pylon_engine_stats_stream_events_total{type="stats"} 1"#));
        }
    }

    #[tokio::test]
    async fn blank_lf_and_crlf_engine_stats_lines_are_ignored() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let config = EngineStatsStreamConfig {
            metrics: Some(metrics.clone()),
            ..Default::default()
        };
        process_lines(
            &config,
            [concat!(
                "\n\r\n",
                r#"{"v":1,"type":"stats","request_id":"req-1","model":"model-a","tokens_generated":1}"#,
                "\r\n"
            )
            .as_bytes()],
        )
        .await
        .assert_counter("stats line after blank CRLF should publish");
        let body = metrics.gather_text().expect("metrics should encode");
        assert!(!body.contains(r#"pylon_engine_stats_stream_invalid_events_total{reason="json"}"#));
    }

    #[test]
    fn compact_engine_stats_line_buffer_keeps_partial_tail() {
        let config = EngineStatsStreamConfig::default();
        let mut line_buffer = b"{\"v\":1,\"type\":\"ping\"}\n{\"v\":1".to_vec();
        assert!(!compact_line_buffer(&config, &mut line_buffer, 22));

        assert_eq!(line_buffer, br#"{"v":1"#);
        assert!(!compact_line_buffer(&config, &mut line_buffer, 6));
        assert!(line_buffer.is_empty());

        let config = EngineStatsStreamConfig {
            max_line_bytes: 4,
            ..Default::default()
        };
        let mut line_buffer = b"1234".to_vec();
        assert!(!compact_line_buffer(&config, &mut line_buffer, 0));
        assert_eq!(line_buffer, b"1234");

        let mut line_buffer = b"12345".to_vec();
        assert!(compact_line_buffer(&config, &mut line_buffer, 0));
        assert!(line_buffer.is_empty());
    }

    #[tokio::test]
    async fn auto_mode_enables_openai_fallback_when_endpoint_is_unsupported_before_events() {
        let (base_url, server) = serve_stats(Router::new().route(
            "/pylon/v1/stats/stream",
            get(|| async { StatusCode::NOT_FOUND }),
        ))
        .await;

        let (tx, rx) = flume::bounded(1);
        let handle = start_engine_stats_stream(
            reconnecting_config(&base_url, EngineStatsStreamMode::Auto),
            tx,
        )
        .expect("auto stats stream should start");
        let update = receive_update(&rx, "auto mode should enable fallback").await;

        assert!(matches!(
            update,
            StatsAggregatorUpdate::EnableOpenAiFallback
        ));

        handle.shutdown().await;
        server.abort();
    }

    #[tokio::test]
    async fn required_mode_does_not_enable_openai_fallback_for_unsupported_endpoint() {
        let (base_url, server) = serve_stats(Router::new().route(
            "/pylon/v1/stats/stream",
            get(|| async { StatusCode::NOT_FOUND }),
        ))
        .await;

        let (tx, rx) = flume::bounded(1);
        let handle = start_engine_stats_stream(
            reconnecting_config(&base_url, EngineStatsStreamMode::Required),
            tx,
        )
        .expect("required stats stream should start");
        assert!(
            tokio::time::timeout(Duration::from_millis(50), rx.recv_async())
                .await
                .is_err(),
            "required mode must not enable OpenAI fallback"
        );

        handle.shutdown().await;
        server.abort();
    }

    #[tokio::test]
    async fn auto_mode_retries_unsupported_endpoint_after_valid_event_without_fallback() {
        let attempts = Arc::new(AtomicUsize::new(0));
        let server_attempts = attempts.clone();
        let (base_url, server) = serve_stats(
            Router::new().route(
                "/pylon/v1/stats/stream",
                get(move || {
                    let attempts = server_attempts.clone();
                    async move {
                        if attempts.fetch_add(1, Ordering::SeqCst) == 0 {
                            (
                                StatusCode::OK,
                                "{\"v\":1,\"type\":\"stats\",\"request_id\":\"req-1\",\"model\":\"model-a\",\"tokens_generated\":1}\n",
                            )
                        } else {
                            (StatusCode::NOT_FOUND, "")
                        }
                    }
                }),
            ),
        )
        .await;

        let (tx, rx) = flume::bounded(4);
        let handle = start_engine_stats_stream(
            reconnecting_config(&base_url, EngineStatsStreamMode::Auto),
            tx,
        )
        .expect("auto stats stream should start");
        let update = receive_update(&rx, "valid stats event should be sent").await;
        assert!(matches!(update, StatsAggregatorUpdate::RequestCounters(_)));
        assert!(
            tokio::time::timeout(Duration::from_millis(50), rx.recv_async())
                .await
                .is_err(),
            "auto mode must not switch to fallback after any valid stream event"
        );

        handle.shutdown().await;
        server.abort();
    }

    #[tokio::test]
    async fn stream_invalid_events_record_metric_reasons_before_valid_update() {
        let (base_url, server) = serve_stats(
            Router::new().route(
                "/pylon/v1/stats/stream",
                get(|| async {
                    concat!(
                        "not-json\n",
                        "[1]\n",
                        "{\"type\":\"ping\"}\n",
                        "{\"v\":2,\"type\":\"ping\"}\n",
                        "{\"v\":1,\"type\":\"nope\"}\n",
                        "{\"v\":1,\"type\":\"stats\",\"request_id\":\"req-empty\",\"model\":\"model-a\"}\n",
                        "{\"v\":1,\"type\":\"stats\",\"request_id\":\"\",\"model\":\"model-a\",\"tokens_generated\":1}\n",
                        "{\"v\":1,\"type\":\"stats\",\"request_id\":\"req-valid\",\"model\":\"model-a\",\"tokens_generated\":1}\n",
                    )
                }),
            ),
        )
        .await;

        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let (tx, rx) = flume::bounded(4);
        let mut config = EngineStatsStreamConfig::new(
            &base_url,
            "/pylon/v1/stats/stream",
            EngineStatsStreamMode::Required,
        );
        config.initial_reconnect_backoff = Duration::from_secs(60);
        config.max_reconnect_backoff = Duration::from_secs(60);
        config.metrics = Some(metrics.clone());

        let handle =
            start_engine_stats_stream(config, tx).expect("required stats stream should start");
        let update = receive_update(&rx, "valid stats event should be sent").await;
        let StatsAggregatorUpdate::RequestCounters(update) = update else {
            panic!("expected valid stream line to produce request counters");
        };
        assert_eq!(update.request_id, "req-valid");
        assert_eq!(update.tokens_generated, Some(1));

        handle.shutdown().await;
        server.abort();

        let body = metrics.gather_text().expect("metrics should encode");
        for reason in [
            "json",
            "not_object",
            "missing_field",
            "version",
            "type",
            "empty_stats",
            "field",
        ] {
            assert!(
                body.contains(&format!(
                    r#"pylon_engine_stats_stream_invalid_events_total{{reason="{reason}"}} 1"#
                )),
                "missing invalid-event metric for reason {reason}; body:\n{body}"
            );
        }
        assert!(body.contains(r#"pylon_engine_stats_stream_events_total{type="stats"} 1"#));
    }
}
