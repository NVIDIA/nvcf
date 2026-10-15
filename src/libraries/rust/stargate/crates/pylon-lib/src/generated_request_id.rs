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

use std::sync::LazyLock;
use std::sync::atomic::{AtomicU64, Ordering};

use uuid::Uuid;

use crate::runtime_state::ModelGeneration;

static REQUEST_SCOPE: LazyLock<Uuid> = LazyLock::new(Uuid::new_v4);
static REQUEST_COUNTER: AtomicU64 = AtomicU64::new(1);

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum GeneratedRequestKind {
    Calibration,
    Canary,
}

impl GeneratedRequestKind {
    fn prefix(self) -> &'static str {
        match self {
            Self::Calibration => "calibration",
            Self::Canary => "canary",
        }
    }
}

pub(crate) fn generated_request_kind(request_id: &str) -> Option<GeneratedRequestKind> {
    generated_request_parts(request_id).map(|(kind, _)| kind)
}

pub(crate) fn next_generated_request_id(
    kind: GeneratedRequestKind,
    generation: &ModelGeneration,
) -> String {
    let counter = REQUEST_COUNTER
        .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |counter| {
            counter.checked_add(1)
        })
        .expect("pylon-generated request id counter exhausted");
    format!(
        "{}-{}-g{}-{counter}",
        kind.prefix(),
        *REQUEST_SCOPE,
        generation.sequence()
    )
}

pub(crate) fn generated_request_generation(
    request_id: &str,
    model_id: &str,
) -> Option<ModelGeneration> {
    generated_request_parts(request_id)
        .map(|(_, sequence)| ModelGeneration::new(model_id, sequence))
}

fn generated_request_parts(request_id: &str) -> Option<(GeneratedRequestKind, u64)> {
    let (without_counter, counter) = request_id.rsplit_once('-')?;
    counter.parse::<u64>().ok()?;
    let (scope_and_kind, generation) = without_counter.rsplit_once("-g")?;
    let sequence = generation.parse::<u64>().ok()?;
    let (kind, scope) = if let Some(scope) = scope_and_kind.strip_prefix("calibration-") {
        (GeneratedRequestKind::Calibration, scope)
    } else {
        (
            GeneratedRequestKind::Canary,
            scope_and_kind.strip_prefix("canary-")?,
        )
    };
    (Uuid::parse_str(scope).ok()? == *REQUEST_SCOPE).then_some((kind, sequence))
}

#[cfg(test)]
mod tests {
    use super::{
        GeneratedRequestKind, generated_request_generation, generated_request_kind,
        next_generated_request_id,
    };
    use crate::runtime_state::ModelGeneration;

    #[test]
    fn generated_request_id_round_trips_exact_generation_without_registry() {
        let generation = ModelGeneration::new("model-a", 42);
        let first = next_generated_request_id(GeneratedRequestKind::Calibration, &generation);
        let second = next_generated_request_id(GeneratedRequestKind::Calibration, &generation);

        assert_ne!(first, second);
        assert_eq!(
            generated_request_generation(&first, "model-a"),
            Some(generation)
        );
        assert_eq!(
            generated_request_kind(&first),
            Some(GeneratedRequestKind::Calibration)
        );
        assert_eq!(
            generated_request_generation("user-request", "model-a"),
            None
        );
        let foreign_scope = format!(
            "calibration-{}-g42-1",
            uuid::Uuid::from_u128(0x12345678123456781234567812345678)
        );
        assert_eq!(
            generated_request_generation(&foreign_scope, "model-a"),
            None,
            "a format-compatible user request must not claim this process's generation"
        );
    }
}
