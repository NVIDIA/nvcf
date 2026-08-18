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

use std::collections::VecDeque;
use std::time::Duration;

use tokio::time::Instant;

pub(crate) const SNAPSHOT_THRESHOLD: usize = 5;
pub(crate) const DEFAULT_INPUT_TPS_CAPACITY_WINDOW: Duration = Duration::from_secs(24 * 60 * 60);
const WINDOW_SEGMENTS: u32 = 1_440;
const MAX_WINDOW_BUCKETS: usize = WINDOW_SEGMENTS as usize + 1;

#[derive(Debug)]
struct WindowMaxBucket {
    end: Instant,
    max: f64,
}

#[derive(Debug)]
pub(crate) struct WindowedMax {
    window: Duration,
    bucket_width: Duration,
    buckets: VecDeque<WindowMaxBucket>,
}

impl Default for WindowedMax {
    fn default() -> Self {
        Self::new(DEFAULT_INPUT_TPS_CAPACITY_WINDOW)
    }
}

impl WindowedMax {
    pub(crate) fn new(window: Duration) -> Self {
        assert!(!window.is_zero(), "windowed max requires a nonzero window");
        let floor = window / WINDOW_SEGMENTS;
        let bucket_width = if floor.saturating_mul(WINDOW_SEGMENTS) < window {
            floor
                .checked_add(Duration::from_nanos(1))
                .expect("window bucket width should fit Duration")
        } else {
            floor
        };
        Self {
            window,
            bucket_width,
            buckets: VecDeque::new(),
        }
    }

    pub(crate) fn observe(&mut self, now: Instant, value: f64) -> bool {
        if value <= 0.0 || !value.is_finite() {
            return false;
        }
        let previous = self.current();
        self.expire_buckets(now);
        match self.buckets.back_mut() {
            Some(bucket) if now < bucket.end => bucket.max = bucket.max.max(value),
            _ => self.buckets.push_back(WindowMaxBucket {
                end: now
                    .checked_add(self.bucket_width)
                    .expect("window bucket end should fit Instant"),
                max: value,
            }),
        }
        debug_assert!(self.buckets.len() <= MAX_WINDOW_BUCKETS);
        self.current() != previous
    }

    pub(crate) fn expire(&mut self, now: Instant) -> bool {
        let previous = self.current();
        self.expire_buckets(now);
        self.current() != previous
    }

    pub(crate) fn current(&self) -> Option<f64> {
        self.buckets
            .iter()
            .map(|bucket| bucket.max)
            .max_by(f64::total_cmp)
    }

    fn expire_buckets(&mut self, now: Instant) {
        let Some(cutoff) = now.checked_sub(self.window) else {
            return;
        };
        while self
            .buckets
            .front()
            .is_some_and(|bucket| bucket.end <= cutoff)
        {
            self.buckets.pop_front();
        }
    }
}

#[derive(Debug, Clone, Default)]
pub(crate) struct TpsDistribution {
    pub(crate) min: f64,
    pub(crate) max: f64,
    pub(crate) mean: f64,
    pub(crate) variance: f64,
    pub(crate) count: usize,
    m2: f64,
}

impl TpsDistribution {
    pub(crate) fn bootstrap(value: f64) -> Option<Self> {
        (value > 0.0 && value.is_finite()).then_some(Self {
            min: value,
            max: value,
            mean: value,
            variance: 0.0,
            count: SNAPSHOT_THRESHOLD,
            m2: 0.0,
        })
    }

    pub(crate) fn update(&mut self, value: f64) {
        if value <= 0.0 || !value.is_finite() {
            return;
        }

        if self.count == 0 || value < self.min {
            self.min = value;
        }
        if self.count == 0 || value > self.max {
            self.max = value;
        }

        self.count += 1;
        let delta = value - self.mean;
        self.mean += delta / self.count as f64;
        let delta2 = value - self.mean;
        self.m2 += delta * delta2;

        if self.count > 1 {
            self.variance = self.m2 / (self.count - 1) as f64;
        }
    }

    pub(crate) fn has_sufficient_data(&self) -> bool {
        self.count >= SNAPSHOT_THRESHOLD
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn seconds(value: u64) -> Duration {
        Duration::from_secs(value)
    }

    #[test]
    fn windowed_max_tracks_and_expires_bucket_maxima() {
        let start = Instant::now();
        let mut max = WindowedMax::new(seconds(60));

        assert_eq!(max.current(), None);
        assert!(max.observe(start, 10.0));
        assert!(max.observe(start + seconds(1), 20.0));
        assert!(!max.observe(start + seconds(2), 15.0));
        assert_eq!(max.current(), Some(20.0));

        assert!(!max.observe(start + seconds(31), 12.0));
        assert_eq!(max.current(), Some(20.0));
        assert!(max.expire(start + Duration::from_millis(61_050)));
        assert_eq!(max.current(), Some(15.0));
        assert!(max.expire(start + Duration::from_millis(62_050)));
        assert_eq!(max.current(), Some(12.0));
        assert!(max.expire(start + seconds(92)));
        assert_eq!(max.current(), None);
    }

    #[test]
    fn windowed_max_rejects_invalid_values() {
        let start = Instant::now();
        let mut max = WindowedMax::new(seconds(60));

        for value in [0.0, -1.0, f64::NAN, f64::INFINITY, f64::NEG_INFINITY] {
            assert!(!max.observe(start, value));
        }
        assert_eq!(max.current(), None);
    }

    #[test]
    fn windowed_max_never_expires_early_at_oldest_boundary() {
        let start = Instant::now();
        let mut max = WindowedMax::new(Duration::from_millis(1_440));
        assert!(max.observe(start, 10.0));

        assert!(!max.expire(start + Duration::from_millis(1_440)));
        assert_eq!(max.current(), Some(10.0));
        assert!(max.expire(start + Duration::from_millis(1_441)));
        assert_eq!(max.current(), None);
    }

    #[test]
    fn windowed_max_memory_is_bounded() {
        let start = Instant::now();
        let mut max = WindowedMax::new(Duration::from_millis(1_440));

        for offset_ms in 0..10_000 {
            max.observe(
                start + Duration::from_millis(offset_ms),
                offset_ms as f64 + 1.0,
            );
            assert!(max.buckets.len() <= MAX_WINDOW_BUCKETS);
        }
    }

    #[test]
    fn tps_distribution_ignores_non_positive_and_non_finite_samples() {
        let mut distribution = TpsDistribution::default();

        for sample in [0.0, -1.0, f64::NAN, f64::INFINITY, f64::NEG_INFINITY] {
            distribution.update(sample);
        }

        assert_eq!(distribution.count, 0);
        assert_eq!(distribution.mean, 0.0);
        assert!(!distribution.has_sufficient_data());
    }

    #[test]
    fn tps_distribution_requires_five_valid_samples() {
        let mut distribution = TpsDistribution::default();

        for sample in [10.0, 20.0, 30.0, 40.0] {
            distribution.update(sample);
        }
        assert!(!distribution.has_sufficient_data());

        distribution.update(50.0);
        assert!(distribution.has_sufficient_data());
        assert_eq!(distribution.mean, 30.0);
    }

    #[test]
    fn bootstrap_populates_a_ready_distribution_that_real_samples_update() {
        let mut distribution = TpsDistribution::bootstrap(100.0)
            .expect("positive finite bootstrap should be accepted");

        assert_eq!(distribution.count, SNAPSHOT_THRESHOLD);
        assert_eq!(distribution.min, 100.0);
        assert_eq!(distribution.max, 100.0);
        assert_eq!(distribution.mean, 100.0);
        assert_eq!(distribution.variance, 0.0);
        assert!(distribution.has_sufficient_data());

        distribution.update(160.0);

        assert_eq!(distribution.count, SNAPSHOT_THRESHOLD + 1);
        assert_eq!(distribution.mean, 110.0);
        assert_eq!(distribution.min, 100.0);
        assert_eq!(distribution.max, 160.0);
        assert!(distribution.variance > 0.0);
    }
}
