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
use std::marker::PhantomData;
use std::str::FromStr;
use std::sync::Arc;
use std::time::Duration;

use futures::StreamExt;
use reqwest::StatusCode;
use serde::de::{self, Deserialize, Deserializer, IgnoredAny, MapAccess, SeqAccess, Visitor};
use tokio::sync::watch;
use tokio::task::JoinHandle;
use tokio::time::Instant as TokioInstant;

use super::collector::{RequestCounterUpdate, StatsAggregatorUpdate, StatsUpdateSource};
use super::metrics::PylonMetrics;

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
}

impl EngineStatsStreamConfig {
    pub fn new(upstream_base_url: &str, path: &str, mode: EngineStatsStreamMode) -> Self {
        Self {
            url: join_base_url_path(upstream_base_url, path),
            mode,
            initial_reconnect_backoff: DEFAULT_INITIAL_RECONNECT_BACKOFF,
            max_reconnect_backoff: DEFAULT_MAX_RECONNECT_BACKOFF,
            max_line_bytes: DEFAULT_MAX_LINE_BYTES,
            metrics: None,
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

pub struct EngineStatsStreamHandle {
    task: JoinHandle<()>,
}

impl EngineStatsStreamHandle {
    pub async fn shutdown(self) {
        self.task.abort();
        let _ = self.task.await;
    }
}

pub fn start_engine_stats_stream(
    config: EngineStatsStreamConfig,
    stats_update_tx: flume::Sender<StatsAggregatorUpdate>,
    stop_rx: watch::Receiver<bool>,
) -> Option<EngineStatsStreamHandle> {
    if config.mode == EngineStatsStreamMode::Off {
        return None;
    }
    let task = tokio::spawn(run_engine_stats_stream(config, stats_update_tx, stop_rx));
    Some(EngineStatsStreamHandle { task })
}

#[derive(Debug, Clone)]
pub(crate) enum ParsedEngineStatsEvent {
    Update(StatsAggregatorUpdate),
    Ping,
}

impl ParsedEngineStatsEvent {
    fn event_type(&self) -> &'static str {
        match self {
            Self::Update(StatsAggregatorUpdate::RequestCounters(_)) => "stats",
            Self::Update(StatsAggregatorUpdate::RequestObservation(_)) => "observation",
            Self::Update(StatsAggregatorUpdate::FinalizeRequest(_)) => "finalize",
            Self::Update(StatsAggregatorUpdate::EnableOpenAiFallback) => "control",
            Self::Ping => "ping",
        }
    }

    fn into_update(self) -> Option<StatsAggregatorUpdate> {
        match self {
            Self::Update(update) => Some(update),
            Self::Ping => None,
        }
    }
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
    let raw: RawEngineStatsLine<'_> = serde_json::from_slice(line)?;
    let RawEngineStatsLine::Object(mut raw) = raw else {
        return Err(EngineStatsParseError::NotObject);
    };
    let version = required_u64(raw.version.take(), "v")?;
    if version != 1 {
        return Err(EngineStatsParseError::UnsupportedVersion(version));
    }
    let event_type = required_str(raw.event_type.as_ref(), "type")?;
    if event_type == "stats" {
        parse_stats_event(raw, observed_at)
    } else if event_type == "ping" {
        Ok(ParsedEngineStatsEvent::Ping)
    } else {
        Err(EngineStatsParseError::UnknownType(event_type.to_string()))
    }
}

#[doc(hidden)]
pub fn parse_engine_stats_line_for_benchmark(line: &[u8], observed_at: TokioInstant) -> bool {
    parse_engine_stats_line(line, observed_at).is_ok()
}

fn parse_stats_event(
    raw: RawEngineStatsEvent<'_>,
    observed_at: TokioInstant,
) -> Result<ParsedEngineStatsEvent, EngineStatsParseError> {
    let request_id = required_nonempty_string(raw.request_id, "request_id")?;
    let model_id = required_nonempty_string(raw.model, "model")?;
    let tokens_processed = optional_u64(raw.tokens_processed, "tokens_processed")?;
    let tokens_generated = optional_u64(raw.tokens_generated, "tokens_generated")?;
    let finished = optional_bool(raw.finished, "finished")?.unwrap_or(false);
    if tokens_processed.is_none() && tokens_generated.is_none() && !finished {
        return Err(EngineStatsParseError::EmptyStatsCounters);
    }
    Ok(ParsedEngineStatsEvent::Update(
        StatsAggregatorUpdate::RequestCounters(RequestCounterUpdate {
            source: StatsUpdateSource::EngineStatsStream,
            request_id,
            model_id,
            tokens_processed,
            tokens_generated,
            finished,
            observed_at,
        }),
    ))
}

enum RawEngineStatsLine<'a> {
    Object(RawEngineStatsEvent<'a>),
    NotObject,
}

#[derive(Default)]
struct RawEngineStatsEvent<'a> {
    version: Option<JsonU64Field>,
    event_type: Option<JsonStringField<'a>>,
    request_id: Option<JsonStringField<'a>>,
    model: Option<JsonStringField<'a>>,
    tokens_processed: Option<JsonU64Field>,
    tokens_generated: Option<JsonU64Field>,
    finished: Option<JsonBoolField>,
}

impl<'de> Deserialize<'de> for RawEngineStatsLine<'de> {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        deserializer.deserialize_any(RawEngineStatsLineVisitor)
    }
}

struct RawEngineStatsLineVisitor;

impl<'de> Visitor<'de> for RawEngineStatsLineVisitor {
    type Value = RawEngineStatsLine<'de>;

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
                    let _ = map.next_value::<IgnoredAny>()?;
                }
            }
        }
        Ok(RawEngineStatsLine::Object(event))
    }

    fn visit_seq<A>(self, mut seq: A) -> Result<Self::Value, A::Error>
    where
        A: SeqAccess<'de>,
    {
        while seq.next_element::<IgnoredAny>()?.is_some() {}
        Ok(RawEngineStatsLine::NotObject)
    }

    fn visit_bool<E>(self, _value: bool) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(RawEngineStatsLine::NotObject)
    }

    fn visit_i64<E>(self, _value: i64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(RawEngineStatsLine::NotObject)
    }

    fn visit_u64<E>(self, _value: u64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(RawEngineStatsLine::NotObject)
    }

    fn visit_f64<E>(self, _value: f64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(RawEngineStatsLine::NotObject)
    }

    fn visit_str<E>(self, _value: &str) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(RawEngineStatsLine::NotObject)
    }

    fn visit_borrowed_str<E>(self, _value: &'de str) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(RawEngineStatsLine::NotObject)
    }

    fn visit_string<E>(self, _value: String) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(RawEngineStatsLine::NotObject)
    }

    fn visit_none<E>(self) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(RawEngineStatsLine::NotObject)
    }

    fn visit_unit<E>(self) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(RawEngineStatsLine::NotObject)
    }
}

#[derive(Debug)]
enum JsonStringField<'a> {
    Value(Cow<'a, str>),
    Invalid,
}

impl<'de> Deserialize<'de> for JsonStringField<'de> {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        deserializer.deserialize_any(JsonStringFieldVisitor(PhantomData))
    }
}

struct JsonStringFieldVisitor<'a>(PhantomData<&'a ()>);

impl<'de> Visitor<'de> for JsonStringFieldVisitor<'de> {
    type Value = JsonStringField<'de>;

    fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("a JSON string")
    }

    fn visit_borrowed_str<E>(self, value: &'de str) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonStringField::Value(Cow::Borrowed(value)))
    }

    fn visit_str<E>(self, value: &str) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonStringField::Value(Cow::Owned(value.to_string())))
    }

    fn visit_string<E>(self, value: String) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonStringField::Value(Cow::Owned(value)))
    }

    fn visit_seq<A>(self, mut seq: A) -> Result<Self::Value, A::Error>
    where
        A: SeqAccess<'de>,
    {
        while seq.next_element::<IgnoredAny>()?.is_some() {}
        Ok(JsonStringField::Invalid)
    }

    fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
    where
        M: MapAccess<'de>,
    {
        while map.next_entry::<IgnoredAny, IgnoredAny>()?.is_some() {}
        Ok(JsonStringField::Invalid)
    }

    fn visit_bool<E>(self, _value: bool) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonStringField::Invalid)
    }

    fn visit_i64<E>(self, _value: i64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonStringField::Invalid)
    }

    fn visit_u64<E>(self, _value: u64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonStringField::Invalid)
    }

    fn visit_f64<E>(self, _value: f64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonStringField::Invalid)
    }

    fn visit_none<E>(self) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonStringField::Invalid)
    }

    fn visit_unit<E>(self) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonStringField::Invalid)
    }
}

#[derive(Debug)]
enum JsonU64Field {
    Value(u64),
    Invalid,
}

impl<'de> Deserialize<'de> for JsonU64Field {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        deserializer.deserialize_any(JsonU64FieldVisitor)
    }
}

struct JsonU64FieldVisitor;

impl<'de> Visitor<'de> for JsonU64FieldVisitor {
    type Value = JsonU64Field;

    fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("a non-negative JSON integer")
    }

    fn visit_u64<E>(self, value: u64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonU64Field::Value(value))
    }

    fn visit_i64<E>(self, value: i64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(u64::try_from(value)
            .map(JsonU64Field::Value)
            .unwrap_or(JsonU64Field::Invalid))
    }

    fn visit_seq<A>(self, mut seq: A) -> Result<Self::Value, A::Error>
    where
        A: SeqAccess<'de>,
    {
        while seq.next_element::<IgnoredAny>()?.is_some() {}
        Ok(JsonU64Field::Invalid)
    }

    fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
    where
        M: MapAccess<'de>,
    {
        while map.next_entry::<IgnoredAny, IgnoredAny>()?.is_some() {}
        Ok(JsonU64Field::Invalid)
    }

    fn visit_bool<E>(self, _value: bool) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonU64Field::Invalid)
    }

    fn visit_f64<E>(self, _value: f64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonU64Field::Invalid)
    }

    fn visit_str<E>(self, _value: &str) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonU64Field::Invalid)
    }

    fn visit_borrowed_str<E>(self, _value: &'de str) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonU64Field::Invalid)
    }

    fn visit_string<E>(self, _value: String) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonU64Field::Invalid)
    }

    fn visit_none<E>(self) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonU64Field::Invalid)
    }

    fn visit_unit<E>(self) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonU64Field::Invalid)
    }
}

#[derive(Debug)]
enum JsonBoolField {
    Value(bool),
    Invalid,
}

impl<'de> Deserialize<'de> for JsonBoolField {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        deserializer.deserialize_any(JsonBoolFieldVisitor)
    }
}

struct JsonBoolFieldVisitor;

impl<'de> Visitor<'de> for JsonBoolFieldVisitor {
    type Value = JsonBoolField;

    fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("a JSON boolean")
    }

    fn visit_bool<E>(self, value: bool) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonBoolField::Value(value))
    }

    fn visit_seq<A>(self, mut seq: A) -> Result<Self::Value, A::Error>
    where
        A: SeqAccess<'de>,
    {
        while seq.next_element::<IgnoredAny>()?.is_some() {}
        Ok(JsonBoolField::Invalid)
    }

    fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
    where
        M: MapAccess<'de>,
    {
        while map.next_entry::<IgnoredAny, IgnoredAny>()?.is_some() {}
        Ok(JsonBoolField::Invalid)
    }

    fn visit_i64<E>(self, _value: i64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonBoolField::Invalid)
    }

    fn visit_u64<E>(self, _value: u64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonBoolField::Invalid)
    }

    fn visit_f64<E>(self, _value: f64) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonBoolField::Invalid)
    }

    fn visit_str<E>(self, _value: &str) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonBoolField::Invalid)
    }

    fn visit_borrowed_str<E>(self, _value: &'de str) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonBoolField::Invalid)
    }

    fn visit_string<E>(self, _value: String) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonBoolField::Invalid)
    }

    fn visit_none<E>(self) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonBoolField::Invalid)
    }

    fn visit_unit<E>(self) -> Result<Self::Value, E>
    where
        E: de::Error,
    {
        Ok(JsonBoolField::Invalid)
    }
}

fn required_u64(
    value: Option<JsonU64Field>,
    field: &'static str,
) -> Result<u64, EngineStatsParseError> {
    optional_u64(value, field)?.ok_or(EngineStatsParseError::MissingField(field))
}

fn optional_u64(
    value: Option<JsonU64Field>,
    field: &'static str,
) -> Result<Option<u64>, EngineStatsParseError> {
    match value {
        Some(JsonU64Field::Value(value)) => Ok(Some(value)),
        Some(JsonU64Field::Invalid) => Err(EngineStatsParseError::InvalidField(field)),
        None => Ok(None),
    }
}

fn optional_bool(
    value: Option<JsonBoolField>,
    field: &'static str,
) -> Result<Option<bool>, EngineStatsParseError> {
    match value {
        Some(JsonBoolField::Value(value)) => Ok(Some(value)),
        Some(JsonBoolField::Invalid) => Err(EngineStatsParseError::InvalidField(field)),
        None => Ok(None),
    }
}

fn required_str<'a>(
    value: Option<&'a JsonStringField<'a>>,
    field: &'static str,
) -> Result<&'a str, EngineStatsParseError> {
    match value {
        Some(JsonStringField::Value(value)) => Ok(value.as_ref()),
        Some(JsonStringField::Invalid) => Err(EngineStatsParseError::InvalidField(field)),
        None => Err(EngineStatsParseError::MissingField(field)),
    }
}

fn required_nonempty_string(
    value: Option<JsonStringField<'_>>,
    field: &'static str,
) -> Result<String, EngineStatsParseError> {
    let value = match value {
        Some(JsonStringField::Value(value)) => value,
        Some(JsonStringField::Invalid) => return Err(EngineStatsParseError::InvalidField(field)),
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
    mut stop_rx: watch::Receiver<bool>,
) {
    let client = reqwest::Client::new();
    let mut backoff = config.initial_reconnect_backoff;
    let mut valid_event_seen = false;
    loop {
        if stop_channel_requested(&stop_rx) {
            return;
        }
        match read_stream_once(
            &config,
            &client,
            &stats_update_tx,
            &mut stop_rx,
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
                let _ = stats_update_tx
                    .send_async(StatsAggregatorUpdate::EnableOpenAiFallback)
                    .await;
                return;
            }
            StreamReadOutcome::Unsupported => {
                observe_reconnect(&config, "unsupported");
            }
            StreamReadOutcome::Retry(reason) => {
                observe_reconnect(&config, reason);
            }
        }

        if sleep_or_stop(backoff, &mut stop_rx).await {
            return;
        }
        backoff = (backoff * 2).min(config.max_reconnect_backoff);
    }
}

enum StreamReadOutcome {
    Stopped,
    Unsupported,
    Retry(&'static str),
}

async fn read_stream_once(
    config: &EngineStatsStreamConfig,
    client: &reqwest::Client,
    stats_update_tx: &flume::Sender<StatsAggregatorUpdate>,
    stop_rx: &mut watch::Receiver<bool>,
    valid_event_seen: &mut bool,
) -> StreamReadOutcome {
    let response = tokio::select! {
        changed = stop_rx.changed() => {
            if stop_channel_changed(changed, stop_rx) {
                return StreamReadOutcome::Stopped;
            }
            return StreamReadOutcome::Retry("stop_watch");
        }
        response = client
            .get(&config.url)
            .header(reqwest::header::ACCEPT, HEADER_ACCEPT_NDJSON)
            .send() => response,
    };
    let response = match response {
        Ok(response) => response,
        Err(error) => {
            tracing::warn!(url = config.url, error = %error, "engine stats stream connect failed");
            return StreamReadOutcome::Retry("connect_error");
        }
    };
    if permanent_unsupported_status(response.status()) {
        tracing::warn!(
            url = config.url,
            status = %response.status(),
            "engine stats stream endpoint is unsupported"
        );
        return StreamReadOutcome::Unsupported;
    }
    if !response.status().is_success() {
        tracing::warn!(
            url = config.url,
            status = %response.status(),
            "engine stats stream returned non-success status"
        );
        return StreamReadOutcome::Retry("http_status");
    }
    observe_connected(config, true);
    let mut stream = response.bytes_stream();
    let mut line_buffer = Vec::with_capacity(1024);

    loop {
        let chunk = tokio::select! {
            changed = stop_rx.changed() => {
                if stop_channel_changed(changed, stop_rx) {
                    observe_connected(config, false);
                    return StreamReadOutcome::Stopped;
                }
                continue;
            }
            chunk = stream.next() => chunk,
        };
        let Some(chunk) = chunk else {
            observe_connected(config, false);
            return StreamReadOutcome::Retry("eof");
        };
        let chunk = match chunk {
            Ok(chunk) => chunk,
            Err(error) => {
                tracing::warn!(url = config.url, error = %error, "engine stats stream read failed");
                observe_connected(config, false);
                return StreamReadOutcome::Retry("read_error");
            }
        };
        line_buffer.extend_from_slice(&chunk);
        let mut consumed = 0;
        while let Some(relative_newline_index) = memchr::memchr(b'\n', &line_buffer[consumed..]) {
            let newline_index = consumed + relative_newline_index;
            if newline_index - consumed > config.max_line_bytes {
                observe_invalid(config, "line_too_large");
                consumed = newline_index + 1;
                continue;
            }
            let mut line_end = newline_index;
            if line_end > consumed && line_buffer[line_end - 1] == b'\r' {
                line_end -= 1;
            }
            if line_end == consumed {
                consumed = newline_index + 1;
                continue;
            }
            let observed_at = TokioInstant::now();
            let parsed_event = {
                let line = &line_buffer[consumed..line_end];
                parse_engine_stats_line(line, observed_at)
            };
            match parsed_event {
                Ok(event) => {
                    *valid_event_seen = true;
                    observe_event(config, event.event_type());
                    let update_sent = match event.into_update() {
                        Some(update) => send_stats_update(stats_update_tx, update).await,
                        None => true,
                    };
                    if !update_sent {
                        observe_connected(config, false);
                        return StreamReadOutcome::Stopped;
                    }
                }
                Err(error) => {
                    tracing::warn!(url = config.url, error = %error, "invalid engine stats event");
                    observe_invalid(config, error.metric_reason());
                }
            }
            consumed = newline_index + 1;
        }
        if consumed == line_buffer.len() {
            line_buffer.clear();
        } else if consumed > 0 {
            line_buffer.drain(..consumed);
        }
        if line_buffer.len() > config.max_line_bytes {
            observe_invalid(config, "line_too_large");
            line_buffer.clear();
        }
    }
}

async fn send_stats_update(
    stats_update_tx: &flume::Sender<StatsAggregatorUpdate>,
    update: StatsAggregatorUpdate,
) -> bool {
    match stats_update_tx.try_send(update) {
        Ok(()) => true,
        Err(flume::TrySendError::Full(update)) => stats_update_tx.send_async(update).await.is_ok(),
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

fn observe_event(config: &EngineStatsStreamConfig, event_type: &'static str) {
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_stream_event(event_type);
    }
}

fn observe_invalid(config: &EngineStatsStreamConfig, reason: &'static str) {
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_invalid_event(reason);
    }
}

fn observe_reconnect(config: &EngineStatsStreamConfig, reason: &'static str) {
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_reconnect(reason);
    }
}

fn observe_connected(config: &EngineStatsStreamConfig, connected: bool) {
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_stream_connected(config.mode.as_str(), connected);
    }
}

fn permanent_unsupported_status(status: StatusCode) -> bool {
    matches!(
        status,
        StatusCode::NOT_FOUND | StatusCode::METHOD_NOT_ALLOWED | StatusCode::NOT_IMPLEMENTED
    )
}

async fn sleep_or_stop(duration: Duration, stop_rx: &mut watch::Receiver<bool>) -> bool {
    tokio::select! {
        _ = tokio::time::sleep(duration) => false,
        changed = stop_rx.changed() => stop_channel_changed(changed, stop_rx),
    }
}

fn stop_channel_requested(stop_rx: &watch::Receiver<bool>) -> bool {
    *stop_rx.borrow()
}

fn stop_channel_changed(
    changed: Result<(), watch::error::RecvError>,
    stop_rx: &watch::Receiver<bool>,
) -> bool {
    changed.is_err() || *stop_rx.borrow()
}

fn join_base_url_path(base_url: &str, path: &str) -> String {
    format!(
        "{}/{}",
        base_url.trim_end_matches('/'),
        path.trim_start_matches('/')
    )
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

    fn parse(line: &[u8]) -> Result<ParsedEngineStatsEvent, EngineStatsParseError> {
        parse_engine_stats_line(line, TokioInstant::now())
    }

    #[test]
    fn parses_valid_engine_stats_events() {
        let event = parse(
            br#"{"v":1,"type":"stats","request_id":"req-1","model":"llama","tokens_processed":4096,"tokens_generated":128,"finished":true}"#,
        )
        .expect("stats event should parse");
        let ParsedEngineStatsEvent::Update(StatsAggregatorUpdate::RequestCounters(update)) = event
        else {
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
    }

    #[tokio::test]
    async fn auto_mode_enables_openai_fallback_when_endpoint_is_unsupported_before_events() {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("listener should bind");
        let addr = listener.local_addr().expect("listener should have addr");
        let server = tokio::spawn(async move {
            let app = Router::new().route(
                "/pylon/v1/stats/stream",
                get(|| async { StatusCode::NOT_FOUND }),
            );
            axum::serve(listener, app).await.expect("server should run");
        });

        let (tx, rx) = flume::bounded(1);
        let (_stop_tx, stop_rx) = watch::channel(false);
        let mut config = EngineStatsStreamConfig::new(
            &format!("http://{addr}"),
            "/pylon/v1/stats/stream",
            EngineStatsStreamMode::Auto,
        );
        config.initial_reconnect_backoff = Duration::from_millis(1);
        config.max_reconnect_backoff = Duration::from_millis(1);

        let handle =
            start_engine_stats_stream(config, tx, stop_rx).expect("auto stats stream should start");
        let update = tokio::time::timeout(Duration::from_secs(2), rx.recv_async())
            .await
            .expect("auto mode should enable fallback")
            .expect("control update should be sent");

        assert!(matches!(
            update,
            StatsAggregatorUpdate::EnableOpenAiFallback
        ));

        handle.shutdown().await;
        server.abort();
    }

    #[tokio::test]
    async fn required_mode_does_not_enable_openai_fallback_for_unsupported_endpoint() {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("listener should bind");
        let addr = listener.local_addr().expect("listener should have addr");
        let server = tokio::spawn(async move {
            let app = Router::new().route(
                "/pylon/v1/stats/stream",
                get(|| async { StatusCode::NOT_FOUND }),
            );
            axum::serve(listener, app).await.expect("server should run");
        });

        let (tx, rx) = flume::bounded(1);
        let (stop_tx, stop_rx) = watch::channel(false);
        let mut config = EngineStatsStreamConfig::new(
            &format!("http://{addr}"),
            "/pylon/v1/stats/stream",
            EngineStatsStreamMode::Required,
        );
        config.initial_reconnect_backoff = Duration::from_millis(1);
        config.max_reconnect_backoff = Duration::from_millis(1);

        let handle = start_engine_stats_stream(config, tx, stop_rx)
            .expect("required stats stream should start");
        assert!(
            tokio::time::timeout(Duration::from_millis(50), rx.recv_async())
                .await
                .is_err(),
            "required mode must not enable OpenAI fallback"
        );

        stop_tx.send(true).expect("stream should receive stop");
        handle.shutdown().await;
        server.abort();
    }

    #[tokio::test]
    async fn auto_mode_retries_unsupported_endpoint_after_valid_event_without_fallback() {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("listener should bind");
        let addr = listener.local_addr().expect("listener should have addr");
        let attempts = Arc::new(AtomicUsize::new(0));
        let server_attempts = attempts.clone();
        let server = tokio::spawn(async move {
            let app = Router::new().route(
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
            );
            axum::serve(listener, app).await.expect("server should run");
        });

        let (tx, rx) = flume::bounded(4);
        let (stop_tx, stop_rx) = watch::channel(false);
        let mut config = EngineStatsStreamConfig::new(
            &format!("http://{addr}"),
            "/pylon/v1/stats/stream",
            EngineStatsStreamMode::Auto,
        );
        config.initial_reconnect_backoff = Duration::from_millis(1);
        config.max_reconnect_backoff = Duration::from_millis(1);

        let handle =
            start_engine_stats_stream(config, tx, stop_rx).expect("auto stats stream should start");
        let update = tokio::time::timeout(Duration::from_secs(2), rx.recv_async())
            .await
            .expect("valid stats event should be sent")
            .expect("stats update should be sent");
        assert!(matches!(update, StatsAggregatorUpdate::RequestCounters(_)));
        assert!(
            tokio::time::timeout(Duration::from_millis(50), rx.recv_async())
                .await
                .is_err(),
            "auto mode must not switch to fallback after any valid stream event"
        );

        stop_tx.send(true).expect("stream should receive stop");
        handle.shutdown().await;
        server.abort();
    }
}
