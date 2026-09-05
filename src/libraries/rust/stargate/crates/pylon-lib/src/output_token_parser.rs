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

#[derive(Debug, Default)]
pub(crate) struct OutputTokenParser {
    reported_tokens: u64,
    saw_exact_tokens: bool,
}

impl OutputTokenParser {
    pub(crate) fn new() -> Self {
        Self::default()
    }

    pub(crate) fn observe_estimated_output_tokens(&mut self, delta: u64) -> Option<u64> {
        if self.saw_exact_tokens || delta == 0 {
            return None;
        }
        self.reported_tokens = self.reported_tokens.saturating_add(delta);
        Some(delta)
    }

    pub(crate) fn observe_exact_output_tokens(&mut self, completion_tokens: u64) -> u64 {
        self.reported_tokens = if self.saw_exact_tokens {
            self.reported_tokens.max(completion_tokens)
        } else {
            completion_tokens
        };
        self.saw_exact_tokens = true;
        self.reported_tokens
    }
}

pub(crate) fn estimate_token_like_units(content: &str) -> u64 {
    let trimmed = content.trim();
    if trimmed.is_empty() {
        return 0;
    }
    u64::try_from(trimmed.split_whitespace().count()).unwrap_or(u64::MAX)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parser_tracks_continuous_exact_usage() {
        let mut parser = OutputTokenParser::new();

        assert_eq!(parser.observe_exact_output_tokens(1), 1);
        assert_eq!(parser.observe_exact_output_tokens(2), 2);
        assert_eq!(parser.observe_exact_output_tokens(2), 2);
    }

    #[test]
    fn exact_usage_replaces_text_estimation() {
        let mut parser = OutputTokenParser::new();

        assert_eq!(parser.observe_estimated_output_tokens(1), Some(1));
        assert_eq!(parser.observe_exact_output_tokens(7), 7);
    }

    #[test]
    fn explicit_counter_disables_later_text_estimates() {
        let mut parser = OutputTokenParser::new();

        assert_eq!(parser.observe_exact_output_tokens(2), 2);
        assert_eq!(parser.observe_estimated_output_tokens(2), None);
    }

    #[test]
    fn token_like_estimator_counts_words_and_ignores_whitespace() {
        assert_eq!(estimate_token_like_units("alpha beta"), 2);
        assert_eq!(estimate_token_like_units("。"), 1);
        assert_eq!(estimate_token_like_units("   "), 0);
    }

    #[test]
    fn parser_returns_monotonic_tokens_after_first_explicit_counter() {
        let mut parser = OutputTokenParser::new();

        assert_eq!(parser.observe_exact_output_tokens(10), 10);
        assert_eq!(parser.observe_exact_output_tokens(5), 10);
    }

    #[test]
    fn first_explicit_counter_can_correct_text_estimate_downward() {
        let mut parser = OutputTokenParser::new();

        assert_eq!(parser.observe_estimated_output_tokens(5), Some(5));
        assert_eq!(parser.observe_exact_output_tokens(3), 3);
        assert_eq!(parser.observe_exact_output_tokens(4), 4);
    }

    #[test]
    fn parser_ignores_zero_estimates() {
        let mut parser = OutputTokenParser::new();
        assert_eq!(parser.observe_estimated_output_tokens(0), None);
    }
}
