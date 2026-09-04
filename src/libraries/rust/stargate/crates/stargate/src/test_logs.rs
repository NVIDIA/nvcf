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

//! Test-only helpers that capture `tracing` output so tests can assert on the
//! log lines operators rely on during incidents.

use std::io::{self, Write};
use std::sync::Arc;

use parking_lot::Mutex;
use tracing::instrument::WithSubscriber;

#[derive(Clone)]
struct CapturedLogWriter(Arc<Mutex<Vec<u8>>>);

impl Write for CapturedLogWriter {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        self.0.lock().extend_from_slice(bytes);
        Ok(bytes.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

fn capture_subscriber(
    level: tracing::Level,
) -> (impl tracing::Subscriber + Send + Sync, Arc<Mutex<Vec<u8>>>) {
    let output = Arc::new(Mutex::new(Vec::new()));
    let subscriber = tracing_subscriber::fmt()
        .with_ansi(false)
        .without_time()
        .with_target(false)
        .with_max_level(level)
        .with_writer({
            let output = Arc::clone(&output);
            move || CapturedLogWriter(Arc::clone(&output))
        })
        .finish();
    (subscriber, output)
}

fn captured_text(output: &Mutex<Vec<u8>>) -> String {
    String::from_utf8(output.lock().clone()).expect("captured logs should be UTF-8")
}

/// Runs `operation` with a capturing subscriber and returns its result plus the emitted log text.
pub(crate) fn capture_logs<T>(level: tracing::Level, operation: impl FnOnce() -> T) -> (T, String) {
    let (subscriber, output) = capture_subscriber(level);
    let result = tracing::subscriber::with_default(subscriber, operation);
    (result, captured_text(&output))
}

/// Async variant of [`capture_logs`]; the subscriber stays attached across await points.
pub(crate) async fn capture_logs_async<T>(
    level: tracing::Level,
    operation: impl Future<Output = T>,
) -> (T, String) {
    let (subscriber, output) = capture_subscriber(level);
    let result = operation.with_subscriber(subscriber).await;
    (result, captured_text(&output))
}
