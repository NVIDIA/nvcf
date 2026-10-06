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

//! Runs the shared `mock-engine` model in real time.
//!
//! One task owns the engine. Handlers submit requests and receive that
//! request's engine events over a channel. Dropping a handle before the
//! request completes cancels it, as a client disconnect frees engine capacity.

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use mock_engine::{Engine, EngineConfig, EngineEvent, RequestId, RequestSpec, WorkerStats};
use tokio::sync::{mpsc, oneshot};
use tokio::time::Instant;

enum Command {
    Submit {
        id: RequestId,
        spec: RequestSpec,
        events: mpsc::UnboundedSender<EngineEvent>,
    },
    Cancel(RequestId),
    Stats(oneshot::Sender<Vec<WorkerStats>>),
}

#[derive(Clone)]
pub(crate) struct EngineDriver {
    commands: mpsc::UnboundedSender<Command>,
    next_id: Arc<AtomicU64>,
    max_concurrency: u64,
}

/// One submitted request's view of the engine.
pub(crate) struct EngineRequest {
    id: RequestId,
    events: mpsc::UnboundedReceiver<EngineEvent>,
    commands: mpsc::UnboundedSender<Command>,
    completed: bool,
}

impl EngineDriver {
    pub(crate) fn spawn(config: EngineConfig) -> Result<Self, String> {
        let max_concurrency = config.max_concurrency();
        let engine = Engine::new(config, true)?;
        let (commands, receiver) = mpsc::unbounded_channel();
        tokio::spawn(run(engine, receiver));
        Ok(Self {
            commands,
            next_id: Arc::new(AtomicU64::new(0)),
            max_concurrency,
        })
    }

    /// Requests the deployment can run at once across all workers.
    pub(crate) fn max_concurrency(&self) -> u64 {
        self.max_concurrency
    }

    pub(crate) fn submit(
        &self,
        cache_affinity_key: Option<&str>,
        input_tokens: usize,
        output_tokens: usize,
    ) -> EngineRequest {
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let (events_tx, events) = mpsc::unbounded_channel();
        let spec = RequestSpec {
            cache_key: cache_affinity_key.map(cache_key),
            input_tokens: input_tokens as u64,
            output_tokens: output_tokens as u64,
        };
        self.commands
            .send(Command::Submit {
                id,
                spec,
                events: events_tx,
            })
            .expect("engine driver runs for the process lifetime");
        EngineRequest {
            id,
            events,
            commands: self.commands.clone(),
            completed: false,
        }
    }

    pub(crate) async fn worker_stats(&self) -> Vec<WorkerStats> {
        let (reply, stats) = oneshot::channel();
        self.commands
            .send(Command::Stats(reply))
            .expect("engine driver runs for the process lifetime");
        stats
            .await
            .expect("engine driver replies to stats requests")
    }
}

impl EngineRequest {
    /// Waits for prefill to finish and returns the reused input tokens.
    pub(crate) async fn first_token(&mut self) -> u64 {
        loop {
            match self.next_event().await {
                EngineEvent::FirstToken {
                    reused_input_tokens,
                    ..
                } => return reused_input_tokens,
                EngineEvent::Completed { .. } => {
                    unreachable!("a request produces its first token before completing")
                }
                EngineEvent::Token { .. } => {}
            }
        }
    }

    /// Waits for the next output token after the first.
    pub(crate) async fn next_token(&mut self) {
        loop {
            match self.next_event().await {
                EngineEvent::Token { .. } => return,
                EngineEvent::Completed { .. } => {
                    self.completed = true;
                    return;
                }
                EngineEvent::FirstToken { .. } => {}
            }
        }
    }

    /// Waits until the request leaves the engine.
    pub(crate) async fn completion(&mut self) {
        while !self.completed {
            if let EngineEvent::Completed { .. } = self.next_event().await {
                self.completed = true;
            }
        }
    }

    async fn next_event(&mut self) -> EngineEvent {
        self.events
            .recv()
            .await
            .expect("engine driver keeps live request channels open")
    }
}

impl Drop for EngineRequest {
    fn drop(&mut self) {
        if !self.completed {
            let _ = self.commands.send(Command::Cancel(self.id));
        }
    }
}

async fn run(mut engine: Engine, mut commands: mpsc::UnboundedReceiver<Command>) {
    let origin = Instant::now();
    let now = || u64::try_from(origin.elapsed().as_micros()).unwrap_or(u64::MAX);
    let mut subscribers: HashMap<RequestId, mpsc::UnboundedSender<EngineEvent>> = HashMap::new();
    loop {
        let deadline = engine
            .next_event_time()
            .map(|at| origin + Duration::from_micros(at));
        tokio::select! {
            command = commands.recv() => match command {
                None => return,
                Some(Command::Submit { id, spec, events }) => {
                    engine.submit(now(), id, spec);
                    subscribers.insert(id, events);
                }
                Some(Command::Cancel(id)) => {
                    engine.cancel(now(), id);
                    subscribers.remove(&id);
                }
                Some(Command::Stats(reply)) => {
                    let _ = reply.send(engine.worker_stats());
                }
            },
            () = tokio::time::sleep_until(deadline.unwrap_or_else(Instant::now)),
                if deadline.is_some() => {}
        }
        for event in engine.advance_to(now()) {
            let (id, completed) = match event {
                EngineEvent::FirstToken { id, .. } | EngineEvent::Token { id, .. } => (id, false),
                EngineEvent::Completed { id, .. } => (id, true),
            };
            if let Some(subscriber) = subscribers.get(&id) {
                let _ = subscriber.send(event);
            }
            if completed {
                subscribers.remove(&id);
            }
        }
    }
}

/// Stable 64-bit FNV-1a hash of a cache affinity key.
fn cache_key(key: &str) -> u64 {
    key.bytes().fold(0xcbf2_9ce4_8422_2325, |hash, byte| {
        (hash ^ u64::from(byte)).wrapping_mul(0x0100_0000_01b3)
    })
}
