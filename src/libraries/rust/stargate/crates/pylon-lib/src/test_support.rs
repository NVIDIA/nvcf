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

use std::collections::BTreeMap;
use std::ops::Deref;
use std::sync::{Arc, Mutex};

use axum::Router;
use tokio::net::TcpListener;
use tokio::task::JoinHandle;

#[derive(Clone, Debug)]
pub(crate) struct RecordedTracingEvent {
    pub(crate) level: tracing::Level,
    pub(crate) fields: BTreeMap<String, String>,
}

#[derive(Clone, Default)]
pub(crate) struct RecordingTracingSubscriber {
    events: Arc<Mutex<Vec<RecordedTracingEvent>>>,
}

impl RecordingTracingSubscriber {
    pub(crate) fn events(&self) -> Vec<RecordedTracingEvent> {
        self.events
            .lock()
            .expect("recorded tracing events should not be poisoned")
            .clone()
    }

    pub(crate) fn take_events(&self) -> Vec<RecordedTracingEvent> {
        std::mem::take(
            &mut *self
                .events
                .lock()
                .expect("recorded tracing events should not be poisoned"),
        )
    }

    pub(crate) fn event_count(&self, message: &str) -> usize {
        self.events
            .lock()
            .expect("recorded tracing events should not be poisoned")
            .iter()
            .filter(|event| event.fields.get("message").map(String::as_str) == Some(message))
            .count()
    }
}

pub(crate) fn tracing_event_by_message<'a>(
    events: &'a [RecordedTracingEvent],
    message: &str,
) -> &'a RecordedTracingEvent {
    events
        .iter()
        .find(|event| event.fields.get("message").map(String::as_str) == Some(message))
        .unwrap_or_else(|| panic!("missing tracing event {message:?}"))
}

pub(crate) fn assert_tracing_event_field(
    event: &RecordedTracingEvent,
    field: &str,
    expected: &str,
) {
    assert_eq!(
        event.fields.get(field).map(String::as_str),
        Some(expected),
        "unexpected {field} field in {event:?}"
    );
}

impl tracing::Subscriber for RecordingTracingSubscriber {
    fn enabled(&self, metadata: &tracing::Metadata<'_>) -> bool {
        metadata.level() <= &tracing::Level::DEBUG
    }

    fn max_level_hint(&self) -> Option<tracing::metadata::LevelFilter> {
        Some(tracing::metadata::LevelFilter::DEBUG)
    }

    fn new_span(&self, _attrs: &tracing::span::Attributes<'_>) -> tracing::span::Id {
        tracing::span::Id::from_u64(1)
    }

    fn record(&self, _span: &tracing::span::Id, _values: &tracing::span::Record<'_>) {}

    fn record_follows_from(&self, _span: &tracing::span::Id, _follows: &tracing::span::Id) {}

    fn event(&self, event: &tracing::Event<'_>) {
        let mut visitor = RecordingTracingFieldVisitor::default();
        event.record(&mut visitor);
        self.events
            .lock()
            .expect("recorded tracing events should not be poisoned")
            .push(RecordedTracingEvent {
                level: *event.metadata().level(),
                fields: visitor.fields,
            });
    }

    fn enter(&self, _span: &tracing::span::Id) {}

    fn exit(&self, _span: &tracing::span::Id) {}
}

#[derive(Default)]
struct RecordingTracingFieldVisitor {
    fields: BTreeMap<String, String>,
}

impl tracing::field::Visit for RecordingTracingFieldVisitor {
    fn record_bool(&mut self, field: &tracing::field::Field, value: bool) {
        self.fields
            .insert(field.name().to_string(), value.to_string());
    }

    fn record_i64(&mut self, field: &tracing::field::Field, value: i64) {
        self.fields
            .insert(field.name().to_string(), value.to_string());
    }

    fn record_u64(&mut self, field: &tracing::field::Field, value: u64) {
        self.fields
            .insert(field.name().to_string(), value.to_string());
    }

    fn record_str(&mut self, field: &tracing::field::Field, value: &str) {
        self.fields
            .insert(field.name().to_string(), value.to_string());
    }

    fn record_debug(&mut self, field: &tracing::field::Field, value: &dyn std::fmt::Debug) {
        self.fields
            .insert(field.name().to_string(), format!("{value:?}"));
    }
}

#[test]
fn recording_tracing_subscriber_records_debug_but_not_trace_events() {
    let subscriber = RecordingTracingSubscriber::default();
    let dispatch = tracing::Dispatch::new(subscriber.clone());
    let _default_guard = tracing::dispatcher::set_default(&dispatch);

    tracing::trace!("trace event");
    tracing::debug!("debug event");

    let messages = subscriber
        .events()
        .into_iter()
        .filter_map(|event| event.fields.get("message").cloned())
        .collect::<Vec<_>>();
    assert_eq!(messages, ["debug event"]);
}

pub(crate) struct TestHttpServer {
    base_url: String,
    task: Option<JoinHandle<()>>,
}

impl TestHttpServer {
    pub(crate) async fn spawn(app: Router) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("test HTTP listener should bind");
        let addr = listener
            .local_addr()
            .expect("test HTTP listener should have an address");
        let task = tokio::spawn(async move {
            axum::serve(listener, app)
                .await
                .expect("test HTTP server should run");
        });
        Self {
            base_url: format!("http://{addr}"),
            task: Some(task),
        }
    }

    pub(crate) fn as_str(&self) -> &str {
        &self.base_url
    }

    pub(crate) async fn shutdown(mut self) {
        if let Some(task) = self.task.take() {
            task.abort();
            let _ = task.await;
        }
    }
}

impl Deref for TestHttpServer {
    type Target = str;

    fn deref(&self) -> &Self::Target {
        &self.base_url
    }
}

impl Drop for TestHttpServer {
    fn drop(&mut self) {
        if let Some(task) = self.task.take() {
            task.abort();
        }
    }
}
