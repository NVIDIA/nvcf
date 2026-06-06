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

pub(crate) const SNAPSHOT_THRESHOLD: usize = 5;

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
}
