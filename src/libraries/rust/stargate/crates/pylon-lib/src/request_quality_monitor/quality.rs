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

use super::RequestQualityMonitorConfig;
use std::collections::HashSet;

#[derive(Debug, Clone, Copy, Default, PartialEq)]
pub struct TextQualityMetrics {
    pub compression_ratio: f64,
    pub repetition_1gram: f64,
    pub repetition_2gram: f64,
    pub repetition_3gram: f64,
    pub degeneracy_score: f64,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct QualityCheckResult {
    pub evaluated: bool,
    pub threshold_match_reason: Option<&'static str>,
}

pub fn evaluate_quality(
    output_text: &str,
    output_tokens: u64,
    median_logprob: Option<f32>,
    config: &RequestQualityMonitorConfig,
) -> (TextQualityMetrics, QualityCheckResult) {
    let min_tokens = u64::from(config.collect_quality_metrics_min_tokens);
    let units = tokenize_quality_units(output_text);
    let computed_metrics = (output_tokens >= min_tokens && !units.is_empty())
        .then(|| compute_text_quality_metrics(&units));
    let metrics = computed_metrics.unwrap_or_default();
    let text_thresholds_enabled = config.output_compression_threshold_max.is_some()
        || config.output_repetition_1gram_threshold_min.is_some()
        || config.output_repetition_2gram_threshold_min.is_some()
        || config.output_repetition_3gram_threshold_min.is_some()
        || config.output_degeneracy_threshold_min.is_some();
    let evaluated = config.output_tokens_threshold_min.is_some()
        || ((config.collect_quality_metrics || text_thresholds_enabled)
            && computed_metrics.is_some())
        || (config.median_logprob_threshold_max.is_some() && median_logprob.is_some());

    // The thresholds are evaluated as OR conditions in priority order so metrics can
    // emit a single canonical "match reason" label instead of a bag of booleans.
    let text_threshold_match_reason = computed_metrics.and_then(|metrics| {
        config
            .output_compression_threshold_max
            .filter(|threshold| metrics.compression_ratio < *threshold)
            .map(|_| "compression_ratio")
            .or_else(|| {
                config
                    .output_repetition_1gram_threshold_min
                    .filter(|threshold| metrics.repetition_1gram > *threshold)
                    .map(|_| "repetition_1gram")
            })
            .or_else(|| {
                config
                    .output_repetition_2gram_threshold_min
                    .filter(|threshold| metrics.repetition_2gram > *threshold)
                    .map(|_| "repetition_2gram")
            })
            .or_else(|| {
                config
                    .output_repetition_3gram_threshold_min
                    .filter(|threshold| metrics.repetition_3gram > *threshold)
                    .map(|_| "repetition_3gram")
            })
            .or_else(|| {
                config
                    .output_degeneracy_threshold_min
                    .filter(|threshold| metrics.degeneracy_score > *threshold)
                    .map(|_| "degeneracy_score")
            })
    });

    let threshold_match_reason = config
        .output_tokens_threshold_min
        .filter(|threshold| output_tokens > u64::from(*threshold))
        .map(|_| "output_tokens")
        .or(text_threshold_match_reason)
        .or_else(|| {
            config
                .median_logprob_threshold_max
                .zip(median_logprob)
                .filter(|(threshold, observed)| *observed < *threshold)
                .map(|_| "median_logprob")
        });

    (
        metrics,
        QualityCheckResult {
            evaluated,
            threshold_match_reason,
        },
    )
}

pub(crate) fn approximate_output_token_count(output_text: &str) -> u64 {
    tokenize_quality_units(output_text).len() as u64
}

fn compute_text_quality_metrics(units: &[&str]) -> TextQualityMetrics {
    // These text-only heuristics treat the streamed response as a sequence of
    // token-like units. ASCII words stay grouped, while no-space scripts and
    // structured punctuation still contribute units we can reason about.
    debug_assert!(!units.is_empty());

    // Compression ratio here is a simple lexical diversity score: lower values mean
    // fewer unique units relative to total output, which is often a gibberish signal.
    let unique_units = units.iter().copied().collect::<HashSet<_>>().len();
    let compression_ratio = unique_units as f64 / units.len() as f64;

    TextQualityMetrics {
        compression_ratio,
        // One-unit repetition is exactly the complement of lexical diversity.
        repetition_1gram: 1.0 - compression_ratio,
        repetition_2gram: repetition_ngram(units, 2),
        repetition_3gram: repetition_ngram(units, 3),
        degeneracy_score: degeneracy_score(units),
    }
}

fn tokenize_quality_units(output_text: &str) -> Vec<&str> {
    let mut tokens = Vec::new();
    let mut chars = output_text.char_indices().peekable();

    while let Some((start, ch)) = chars.next() {
        if ch.is_whitespace() {
            continue;
        }

        let mut end = start + ch.len_utf8();
        if should_group_unit_char(ch) {
            while let Some((next_start, next_ch)) =
                chars.next_if(|&(_, next_ch)| should_group_unit_char(next_ch))
            {
                end = next_start + next_ch.len_utf8();
            }
        }

        tokens.push(&output_text[start..end]);
    }

    tokens
}

fn should_group_unit_char(ch: char) -> bool {
    ch == '_' || ch.is_numeric() || (ch.is_alphabetic() && (ch.is_uppercase() || ch.is_lowercase()))
}

fn repetition_ngram(units: &[&str], n: usize) -> f64 {
    // Measure how often n-unit windows repeat. A value near 1.0 means most windows
    // are duplicates, which catches outputs that keep reusing the same local pattern.
    if units.len() < n {
        return 0.0;
    }
    let total = units.len() - n + 1;
    let unique = units.windows(n).collect::<HashSet<_>>().len();
    (1.0 - unique as f64 / total as f64).clamp(0.0, 1.0)
}

fn degeneracy_score(units: &[&str]) -> f64 {
    // Degeneracy looks for the longest obvious loop in the response, either the same
    // token repeated consecutively or a two-token alternation like "a b a b a b".
    let n = units.len();
    if n < 2 {
        return 0.0;
    }

    let mut max_ident = 1usize;
    let mut cur_ident = 1usize;
    let mut max_alt = 1usize;
    let mut alt_len = 1usize;
    for index in 1..n {
        cur_ident = if units[index] == units[index - 1] {
            cur_ident + 1
        } else {
            1
        };
        max_ident = max_ident.max(cur_ident);

        if index >= 2 && units[index] == units[index - 2] && units[index] != units[index - 1] {
            alt_len = if alt_len >= 2 { alt_len + 1 } else { 3 };
        } else {
            alt_len = 1;
        }
        max_alt = max_alt.max(alt_len);
    }

    (max_ident.max(max_alt) as f64 / n as f64).clamp(0.0, 1.0)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn metrics_config() -> RequestQualityMonitorConfig {
        RequestQualityMonitorConfig {
            collect_quality_metrics: true,
            collect_quality_metrics_min_tokens: 1,
            ..RequestQualityMonitorConfig::default()
        }
    }

    fn assert_evaluated(result: QualityCheckResult, reason: Option<&'static str>) {
        assert!(result.evaluated);
        assert_eq!(result.threshold_match_reason, reason);
    }

    fn assert_skipped(result: QualityCheckResult) {
        assert!(!result.evaluated);
        assert_eq!(result.threshold_match_reason, None);
    }

    #[test]
    fn repetition_and_degeneracy_are_high_for_repeated_output() {
        let config = metrics_config();
        let (metrics, result) = evaluate_quality("hello hello hello hello hello", 5, None, &config);

        assert!(
            metrics.repetition_1gram > 0.5,
            "expected high repetition_1gram, got {}",
            metrics.repetition_1gram
        );
        assert!(
            metrics.degeneracy_score > 0.5,
            "expected high degeneracy_score, got {}",
            metrics.degeneracy_score
        );
        assert_evaluated(result, None);
    }

    #[test]
    fn threshold_match_uses_or_semantics() {
        let config = RequestQualityMonitorConfig {
            output_repetition_1gram_threshold_min: Some(0.3),
            ..metrics_config()
        };
        let (_metrics, result) =
            evaluate_quality("word word word word word word", 6, None, &config);
        assert_evaluated(result, Some("repetition_1gram"));
    }

    #[test]
    fn compression_threshold_matches_low_diversity_output() {
        let config = RequestQualityMonitorConfig {
            output_compression_threshold_max: Some(0.5),
            ..metrics_config()
        };

        let (metrics, result) = evaluate_quality("loop loop loop loop", 4, None, &config);

        assert!(metrics.compression_ratio < 0.5);
        assert_evaluated(result, Some("compression_ratio"));
    }

    #[test]
    fn repetition_2gram_threshold_matches_repeated_bigrams() {
        let config = RequestQualityMonitorConfig {
            output_repetition_2gram_threshold_min: Some(0.5),
            ..metrics_config()
        };

        let (metrics, result) = evaluate_quality("a b a b a b", 6, None, &config);

        assert!(metrics.repetition_2gram > 0.5);
        assert_evaluated(result, Some("repetition_2gram"));
    }

    #[test]
    fn repetition_3gram_threshold_matches_repeated_trigrams() {
        let config = RequestQualityMonitorConfig {
            output_repetition_3gram_threshold_min: Some(0.5),
            ..metrics_config()
        };

        let (metrics, result) = evaluate_quality("a b c a b c a b c", 9, None, &config);

        assert!(metrics.repetition_3gram > 0.5);
        assert_evaluated(result, Some("repetition_3gram"));
    }

    #[test]
    fn degeneracy_threshold_matches_alternating_loop_output() {
        let config = RequestQualityMonitorConfig {
            output_degeneracy_threshold_min: Some(0.8),
            ..metrics_config()
        };

        let (metrics, result) = evaluate_quality("a b a b a b", 6, None, &config);

        assert!(metrics.degeneracy_score > 0.8);
        assert_evaluated(result, Some("degeneracy_score"));
    }

    #[test]
    fn degeneracy_threshold_does_not_match_ordinary_short_output() {
        let config = RequestQualityMonitorConfig {
            output_degeneracy_threshold_min: Some(0.5),
            ..metrics_config()
        };

        let (metrics, result) = evaluate_quality("a b c", 3, None, &config);

        assert!(metrics.degeneracy_score < 0.5);
        assert_evaluated(result, None);
    }

    #[test]
    fn no_space_loop_output_is_detected_as_degenerate() {
        let config = RequestQualityMonitorConfig {
            output_degeneracy_threshold_min: Some(0.8),
            ..metrics_config()
        };

        let (metrics, result) = evaluate_quality("哈哈哈哈哈哈", 6, None, &config);

        assert!(metrics.degeneracy_score > 0.8);
        assert_evaluated(result, Some("degeneracy_score"));
    }

    #[test]
    fn output_tokens_threshold_works_without_logprobs() {
        let config = RequestQualityMonitorConfig {
            output_tokens_threshold_min: Some(10),
            ..RequestQualityMonitorConfig::default()
        };
        let (_metrics, result) = evaluate_quality("short output", 11, None, &config);
        assert_evaluated(result, Some("output_tokens"));
    }

    #[test]
    fn text_quality_thresholds_do_not_match_below_min_token_gate() {
        let config = RequestQualityMonitorConfig {
            collect_quality_metrics: true,
            collect_quality_metrics_min_tokens: 20,
            output_compression_threshold_max: Some(0.5),
            ..RequestQualityMonitorConfig::default()
        };

        let (metrics, result) =
            evaluate_quality("alpha beta gamma delta epsilon", 5, None, &config);

        assert_eq!(metrics, TextQualityMetrics::default());
        assert_skipped(result);
    }

    #[test]
    fn empty_output_text_does_not_count_as_evaluated_quality_metrics() {
        let config = RequestQualityMonitorConfig {
            collect_quality_metrics: true,
            collect_quality_metrics_min_tokens: 4,
            output_compression_threshold_max: Some(0.8),
            ..RequestQualityMonitorConfig::default()
        };

        let (metrics, result) = evaluate_quality("", 8, None, &config);

        assert_eq!(metrics, TextQualityMetrics::default());
        assert_skipped(result);
    }

    #[test]
    fn median_logprob_threshold_does_not_match_when_logprobs_are_absent() {
        let config = RequestQualityMonitorConfig {
            median_logprob_threshold_max: Some(-7.0),
            ..RequestQualityMonitorConfig::default()
        };

        let (_metrics, result) = evaluate_quality("alpha beta gamma", 3, None, &config);

        assert_skipped(result);
    }

    #[test]
    fn min_token_gate_still_allows_output_tokens_threshold() {
        let config = RequestQualityMonitorConfig {
            collect_quality_metrics: true,
            collect_quality_metrics_min_tokens: 20,
            output_tokens_threshold_min: Some(4),
            ..RequestQualityMonitorConfig::default()
        };

        let (metrics, result) =
            evaluate_quality("alpha beta gamma delta epsilon", 5, None, &config);

        assert_eq!(metrics.compression_ratio, 0.0);
        assert_evaluated(result, Some("output_tokens"));
    }

    #[test]
    fn min_token_gate_still_allows_median_logprob_threshold() {
        let config = RequestQualityMonitorConfig {
            collect_quality_metrics: true,
            collect_quality_metrics_min_tokens: 20,
            median_logprob_threshold_max: Some(-7.0),
            ..RequestQualityMonitorConfig::default()
        };

        let (metrics, result) = evaluate_quality("alpha", 1, Some(-8.0), &config);

        assert_eq!(metrics.compression_ratio, 0.0);
        assert_evaluated(result, Some("median_logprob"));
    }

    #[test]
    fn multiple_thresholds_choose_stable_first_reason() {
        let config = RequestQualityMonitorConfig {
            output_tokens_threshold_min: Some(4),
            output_compression_threshold_max: Some(0.8),
            output_repetition_1gram_threshold_min: Some(0.3),
            output_degeneracy_threshold_min: Some(0.5),
            median_logprob_threshold_max: Some(-2.0),
            ..metrics_config()
        };

        let (_metrics, result) =
            evaluate_quality("loop loop loop loop loop", 5, Some(-10.0), &config);

        assert_evaluated(result, Some("output_tokens"));
    }

    #[test]
    fn unevaluated_text_thresholds_are_marked_skipped() {
        let config = RequestQualityMonitorConfig {
            collect_quality_metrics: true,
            collect_quality_metrics_min_tokens: 20,
            output_repetition_1gram_threshold_min: Some(0.3),
            ..RequestQualityMonitorConfig::default()
        };

        let (_metrics, result) = evaluate_quality("alpha beta gamma", 3, None, &config);

        assert_skipped(result);
    }
}
