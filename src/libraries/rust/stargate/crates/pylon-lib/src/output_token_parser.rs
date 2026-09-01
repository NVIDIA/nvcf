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

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct OutputCharacters {
    pub(crate) ascii: u64,
    pub(crate) non_ascii: u64,
}

impl OutputCharacters {
    pub(crate) fn from_text(text: &str) -> Self {
        text.chars().fold(Self::default(), |mut count, character| {
            if character.is_ascii() {
                count.ascii = count.ascii.saturating_add(1);
            } else {
                count.non_ascii = count.non_ascii.saturating_add(1);
            }
            count
        })
    }

    pub(crate) fn saturating_add(self, other: Self) -> Self {
        Self {
            ascii: self.ascii.saturating_add(other.ascii),
            non_ascii: self.non_ascii.saturating_add(other.non_ascii),
        }
    }

    fn bootstrap_units(self) -> u64 {
        let ascii_units = (self.ascii / 4).saturating_add(u64::from(!self.ascii.is_multiple_of(4)));
        ascii_units.saturating_add(self.non_ascii)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct EstimatedOutputUpdate {
    pub(crate) displayed_tokens: u64,
    pub(crate) delta: Option<u64>,
    pub(crate) raw_bootstrap_units: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ExactOutputUpdate {
    Applied { tokens: u64, delta: u64 },
    Regressed { prior: u64, observed: u64 },
}

#[derive(Debug, Default)]
pub(crate) struct OutputTokenParser {
    all_characters: OutputCharacters,
    estimated_tail_characters: OutputCharacters,
    exact_baseline: Option<u64>,
    displayed_tokens: u64,
}

impl OutputTokenParser {
    pub(crate) fn new() -> Self {
        Self::default()
    }

    pub(crate) fn observe_generated_characters(
        &mut self,
        characters: OutputCharacters,
    ) -> EstimatedOutputUpdate {
        self.all_characters = self.all_characters.saturating_add(characters);
        self.estimated_tail_characters = self.estimated_tail_characters.saturating_add(characters);
        let displayed_tokens = self
            .exact_baseline
            .unwrap_or_default()
            .saturating_add(self.estimated_tail_characters.bootstrap_units());
        let delta = (displayed_tokens != self.displayed_tokens)
            .then(|| displayed_tokens.saturating_sub(self.displayed_tokens));
        self.displayed_tokens = displayed_tokens;
        EstimatedOutputUpdate {
            displayed_tokens,
            delta,
            raw_bootstrap_units: self.all_characters.bootstrap_units(),
        }
    }

    pub(crate) fn observe_exact_output_tokens(
        &mut self,
        completion_tokens: u64,
    ) -> ExactOutputUpdate {
        if let Some(prior) = self.exact_baseline
            && completion_tokens < prior
        {
            return ExactOutputUpdate::Regressed {
                prior,
                observed: completion_tokens,
            };
        }
        let delta = completion_tokens.saturating_sub(self.exact_baseline.unwrap_or_default());
        self.exact_baseline = Some(completion_tokens);
        self.estimated_tail_characters = OutputCharacters::default();
        self.displayed_tokens = completion_tokens;
        ExactOutputUpdate::Applied {
            tokens: completion_tokens,
            delta,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn text(value: &str) -> OutputCharacters {
        OutputCharacters::from_text(value)
    }

    #[test]
    fn bootstrap_estimate_counts_ascii_quarters_and_unicode_scalars() {
        assert_eq!(text("abcd").bootstrap_units(), 1);
        assert_eq!(text("abcde").bootstrap_units(), 2);
        assert_eq!(text("   ").bootstrap_units(), 1);
        assert_eq!(text("a。").bootstrap_units(), 2);
    }

    fn assert_all_fragmentations_match(text_value: &str) {
        let mut whole = OutputTokenParser::new();
        let expected = whole.observe_generated_characters(text(text_value));
        let boundaries = text_value
            .char_indices()
            .map(|(index, _)| index)
            .skip(1)
            .collect::<Vec<_>>();
        assert!(boundaries.len() < usize::BITS as usize);

        for cut_mask in 0..(1_usize << boundaries.len()) {
            let mut parser = OutputTokenParser::new();
            let mut start = 0;
            for (cut_index, end) in boundaries.iter().copied().enumerate() {
                if cut_mask & (1_usize << cut_index) != 0 {
                    parser.observe_generated_characters(text(&text_value[start..end]));
                    start = end;
                }
            }
            let fragmented = parser.observe_generated_characters(text(&text_value[start..]));
            assert_eq!(
                (fragmented.displayed_tokens, fragmented.raw_bootstrap_units),
                (expected.displayed_tokens, expected.raw_bootstrap_units),
                "fragmentation mask {cut_mask:#b} changed the estimate for {text_value:?}"
            );
        }
    }

    #[test]
    fn estimate_is_invariant_to_every_fragmentation_of_representative_text() {
        for text_value in ["hello", "你好世界", "🤖✨", "x += 1", r#"{"x":1}"#] {
            assert_all_fragmentations_match(text_value);
        }
    }

    #[test]
    fn sparse_exact_usage_resets_only_the_estimated_tail() {
        let mut parser = OutputTokenParser::new();

        assert_eq!(
            parser.observe_generated_characters(text("abcdefgh")),
            EstimatedOutputUpdate {
                displayed_tokens: 2,
                delta: Some(2),
                raw_bootstrap_units: 2,
            }
        );
        assert_eq!(
            parser.observe_exact_output_tokens(5),
            ExactOutputUpdate::Applied {
                tokens: 5,
                delta: 5,
            }
        );
        assert_eq!(
            parser.observe_generated_characters(text("x")),
            EstimatedOutputUpdate {
                displayed_tokens: 6,
                delta: Some(1),
                raw_bootstrap_units: 3,
            }
        );
        assert_eq!(
            parser.observe_exact_output_tokens(6),
            ExactOutputUpdate::Applied {
                tokens: 6,
                delta: 1,
            }
        );
    }

    #[test]
    fn exact_usage_can_correct_the_displayed_estimate_downward() {
        let mut parser = OutputTokenParser::new();
        parser.observe_generated_characters(text("abcdefghijkl"));

        assert_eq!(
            parser.observe_exact_output_tokens(2),
            ExactOutputUpdate::Applied {
                tokens: 2,
                delta: 2,
            }
        );
    }

    #[test]
    fn exact_estimate_exact_sequence_replaces_the_cumulative_display() {
        let mut parser = OutputTokenParser::new();
        assert_eq!(
            parser
                .observe_generated_characters(text("abcdefgh"))
                .displayed_tokens,
            2
        );
        assert_eq!(
            parser.observe_exact_output_tokens(5),
            ExactOutputUpdate::Applied {
                tokens: 5,
                delta: 5,
            }
        );
        assert_eq!(
            parser
                .observe_generated_characters(text("abcdefgh"))
                .displayed_tokens,
            7
        );
        assert_eq!(
            parser.observe_exact_output_tokens(6),
            ExactOutputUpdate::Applied {
                tokens: 6,
                delta: 1,
            }
        );
    }

    #[test]
    fn exact_regression_is_compared_to_the_prior_exact_counter() {
        let mut parser = OutputTokenParser::new();
        parser.observe_exact_output_tokens(5);
        parser.observe_generated_characters(text("abcdefgh"));

        assert_eq!(
            parser.observe_exact_output_tokens(4),
            ExactOutputUpdate::Regressed {
                prior: 5,
                observed: 4,
            }
        );
        assert_eq!(
            parser.observe_exact_output_tokens(7),
            ExactOutputUpdate::Applied {
                tokens: 7,
                delta: 2,
            }
        );
    }

    #[test]
    fn unchanged_integer_estimate_does_not_emit_a_delta() {
        let mut parser = OutputTokenParser::new();
        assert_eq!(
            parser.observe_generated_characters(text("a")).delta,
            Some(1)
        );
        for fragment in ["b", "c", "d"] {
            assert_eq!(
                parser.observe_generated_characters(text(fragment)).delta,
                None
            );
        }
    }
}
