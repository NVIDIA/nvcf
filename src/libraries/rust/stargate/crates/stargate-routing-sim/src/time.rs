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

use std::time::Duration;

/// Virtual simulation time in microseconds.
pub type Micros = u64;

pub fn micros_from_secs(seconds: f64) -> Micros {
    (seconds * 1_000_000.0).round().max(0.0) as Micros
}

pub fn micros_from_ms(milliseconds: f64) -> Micros {
    micros_from_secs(milliseconds / 1000.0)
}

/// Rounds up: a nonzero load-balancer wait must not read as an expired one.
pub fn micros_from_duration(duration: Duration) -> Micros {
    Micros::try_from(duration.as_nanos().div_ceil(1000)).unwrap_or(Micros::MAX)
}

pub fn ms(micros: Micros) -> f64 {
    micros as f64 / 1000.0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn durations_round_up_to_whole_micros() {
        assert_eq!(micros_from_duration(Duration::ZERO), 0);
        assert_eq!(micros_from_duration(Duration::from_nanos(1)), 1);
        assert_eq!(micros_from_duration(Duration::from_nanos(1_001)), 2);
        assert_eq!(micros_from_duration(Duration::MAX), Micros::MAX);
    }
}
