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

use sonic_rs::{JsonContainerTrait, JsonValueTrait, Value};

#[derive(Debug, Default)]
pub(crate) struct OutputTokenParser {
    last_reported_tokens: u64,
    saw_explicit_counter: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum OutputTokenProgress {
    ExplicitCumulative { tokens: u64, delta: u64 },
    EstimatedDelta { delta: u64 },
}

impl OutputTokenProgress {
    #[cfg(test)]
    pub(crate) fn delta(self) -> u64 {
        match self {
            Self::ExplicitCumulative { delta, .. } | Self::EstimatedDelta { delta } => delta,
        }
    }
}

impl OutputTokenParser {
    pub(crate) fn new() -> Self {
        Self {
            last_reported_tokens: 0,
            saw_explicit_counter: false,
        }
    }

    pub(crate) fn observe_json(&mut self, value: Option<&Value>) -> Option<OutputTokenProgress> {
        if let Some(completion_tokens) = explicit_completion_tokens(value) {
            let tokens = if self.saw_explicit_counter {
                self.last_reported_tokens.max(completion_tokens)
            } else {
                completion_tokens
            };
            let delta = tokens.saturating_sub(self.last_reported_tokens);
            self.last_reported_tokens = tokens;
            self.saw_explicit_counter = true;
            return Some(OutputTokenProgress::ExplicitCumulative { tokens, delta });
        }

        if self.saw_explicit_counter {
            return None;
        }

        let delta = estimate_completion_delta_from_text(value?)?;
        // Text-derived token estimates are telemetry counters; saturate instead of wrapping.
        self.last_reported_tokens = self.last_reported_tokens.saturating_add(delta);
        Some(OutputTokenProgress::EstimatedDelta { delta })
    }
}

fn explicit_completion_tokens(value: Option<&Value>) -> Option<u64> {
    let value = value?;
    value["usage"]["completion_tokens"]
        .as_u64()
        .or_else(|| value["output_tokens_so_far"].as_u64())
        .or_else(|| value["response"]["usage"]["output_tokens"].as_u64())
}

fn estimate_completion_delta_from_text(value: &Value) -> Option<u64> {
    let total = value["choices"]
        .as_array()
        .map(|choices| {
            choices
                .iter()
                .filter_map(|choice| choice["delta"]["content"].as_str())
                .map(estimate_token_like_units)
                .sum()
        })
        .unwrap_or_default();
    if total > 0 {
        return Some(total);
    }
    let total = value["delta"]
        .as_str()
        .map(estimate_token_like_units)
        .unwrap_or_default();
    Some(total).filter(|units| *units > 0)
}

fn estimate_token_like_units(content: &str) -> u64 {
    let trimmed = content.trim();
    if trimmed.is_empty() {
        return 0;
    }
    let whitespace_units = trimmed.split_whitespace().count();
    let units = if whitespace_units > 0 {
        whitespace_units
    } else {
        trimmed.chars().count()
    };
    u64::try_from(units).unwrap_or(u64::MAX)
}

#[cfg(test)]
mod tests {
    use super::*;

    impl OutputTokenParser {
        fn parse_output_token_progress(&mut self, raw_data: &str) -> Option<OutputTokenProgress> {
            let value = sonic_rs::from_str(raw_data).ok();
            self.observe_json(value.as_ref())
        }

        fn parse_incremental_output_tokens(&mut self, raw_data: &str) -> Option<u64> {
            self.parse_output_token_progress(raw_data)
                .map(OutputTokenProgress::delta)
        }
    }

    #[test]
    fn vllm_parser_tracks_continuous_usage_deltas() {
        let mut parser = OutputTokenParser::new();
        let first = r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}],"usage":{"prompt_tokens":8,"completion_tokens":1,"total_tokens":9}}"#;
        let second = r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":" world"}}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}"#;
        let final_usage = r#"{"object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}"#;

        assert_eq!(
            parser.parse_output_token_progress(first),
            Some(OutputTokenProgress::ExplicitCumulative {
                tokens: 1,
                delta: 1
            })
        );
        assert_eq!(
            parser.parse_output_token_progress(second),
            Some(OutputTokenProgress::ExplicitCumulative {
                tokens: 2,
                delta: 1
            })
        );
        assert_eq!(
            parser.parse_output_token_progress(final_usage),
            Some(OutputTokenProgress::ExplicitCumulative {
                tokens: 2,
                delta: 0
            })
        );
    }

    #[test]
    fn parser_reads_terminal_usage_chunk() {
        let mut parser = OutputTokenParser::new();
        let content = r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"。"}}],"usage":null}"#;
        let terminal = r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":7,"total_tokens":7}}"#;

        assert_eq!(
            parser.parse_output_token_progress(content),
            Some(OutputTokenProgress::EstimatedDelta { delta: 1 })
        );
        assert_eq!(
            parser.parse_output_token_progress(terminal),
            Some(OutputTokenProgress::ExplicitCumulative {
                tokens: 7,
                delta: 6
            })
        );
    }

    #[test]
    fn parser_reads_output_tokens_so_far() {
        let mut parser = OutputTokenParser::new();
        let chunk = r#"{"object":"chat.completion.chunk","output_tokens_so_far":4,"choices":[{"delta":{"content":"hello"}}]}"#;

        assert_eq!(
            parser.parse_output_token_progress(chunk),
            Some(OutputTokenProgress::ExplicitCumulative {
                tokens: 4,
                delta: 4
            })
        );
    }

    #[test]
    fn parser_reads_responses_delta_and_terminal_usage() {
        let mut parser = OutputTokenParser::new();
        let delta = r#"{"type":"response.output_text.delta","delta":"hello world"}"#;
        let terminal = r#"{"type":"response.completed","response":{"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7}}}"#;

        assert_eq!(
            parser.parse_output_token_progress(delta),
            Some(OutputTokenProgress::EstimatedDelta { delta: 2 })
        );
        assert_eq!(
            parser.parse_output_token_progress(terminal),
            Some(OutputTokenProgress::ExplicitCumulative {
                tokens: 3,
                delta: 1
            })
        );
    }

    #[test]
    fn explicit_counter_disables_later_text_estimates() {
        let mut parser = OutputTokenParser::new();
        let explicit = r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":2},"choices":[{"delta":{"content":"alpha beta"}}]}"#;
        let text_only =
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"gamma delta"}}]}"#;

        assert!(matches!(
            parser.parse_output_token_progress(explicit),
            Some(OutputTokenProgress::ExplicitCumulative { tokens: 2, .. })
        ));
        assert_eq!(parser.parse_output_token_progress(text_only), None);
    }

    #[test]
    fn text_fallback_counts_all_choices_in_chunk() {
        let mut parser = OutputTokenParser::new();
        let chunk = r#"{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"alpha beta"}},{"index":1,"delta":{"content":"loop loop loop"}}]}"#;

        assert_eq!(
            parser.parse_output_token_progress(chunk),
            Some(OutputTokenProgress::EstimatedDelta { delta: 5 })
        );
    }

    #[test]
    fn parser_never_subtracts_when_cumulative_usage_regresses() {
        let mut parser = OutputTokenParser::new();
        let five = r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":5}}"#;
        let three = r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":3}}"#;
        let seven = r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":7}}"#;

        assert_eq!(parser.parse_incremental_output_tokens(five), Some(5));
        assert_eq!(parser.parse_incremental_output_tokens(three), Some(0));
        assert_eq!(parser.parse_incremental_output_tokens(seven), Some(2));
    }

    #[test]
    fn parser_returns_monotonic_tokens_after_first_explicit_counter() {
        let mut parser = OutputTokenParser::new();
        let ten = r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":10}}"#;
        let five = r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":5}}"#;

        assert_eq!(
            parser.parse_output_token_progress(ten),
            Some(OutputTokenProgress::ExplicitCumulative {
                tokens: 10,
                delta: 10
            })
        );
        assert_eq!(
            parser.parse_output_token_progress(five),
            Some(OutputTokenProgress::ExplicitCumulative {
                tokens: 10,
                delta: 0
            })
        );
    }

    #[test]
    fn first_explicit_counter_can_correct_text_estimate_downward() {
        let mut parser = OutputTokenParser::new();
        let text = r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"one two three four five"}}]}"#;
        let three = r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":3}}"#;
        let four = r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":4}}"#;

        assert_eq!(
            parser.parse_output_token_progress(text),
            Some(OutputTokenProgress::EstimatedDelta { delta: 5 })
        );
        assert_eq!(
            parser.parse_output_token_progress(three),
            Some(OutputTokenProgress::ExplicitCumulative {
                tokens: 3,
                delta: 0
            })
        );
        assert_eq!(
            parser.parse_output_token_progress(four),
            Some(OutputTokenProgress::ExplicitCumulative {
                tokens: 4,
                delta: 1
            })
        );
    }

    #[test]
    fn parser_ignores_missing_null_and_non_integer_usage() {
        let mut parser = OutputTokenParser::new();

        for raw_data in [
            r#"{"object":"chat.completion.chunk"}"#,
            r#"{"object":"chat.completion.chunk","usage":null}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":null}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":"4"}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":4.5}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":-1}}"#,
            r#"not-json"#,
        ] {
            assert_eq!(parser.parse_incremental_output_tokens(raw_data), None);
        }
    }
}
