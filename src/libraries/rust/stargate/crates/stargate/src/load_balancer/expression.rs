// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::HashSet;

use sfv::{BareItem, Item, Parameters, Parser, Version};

use super::target_state::LoadBalancerDefinition;
use super::{
    ClusterComparator, LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig,
    LoadBalancerAlgorithmOverride, LoadBalancerAlgorithmSettings, LoadBalancerRouter,
    LoadBalancerRoutingAlgorithmError, MAX_POWER_OF_N_SAMPLE_COUNT,
};

// The affinity ring allocates candidates times this value; static config stays operator-trusted.
const MAX_EXPRESSION_VIRTUAL_NODES: usize = 1024;

#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
#[error("{message}")]
pub(crate) struct RejectionError {
    pub(crate) class: &'static str,
    pub(crate) message: String,
    pub(crate) requested: String,
}

impl RejectionError {
    pub(crate) fn new(class: &'static str, message: impl Into<String>, requested: &str) -> Self {
        Self {
            class,
            message: message.into(),
            requested: requested.to_owned(),
        }
    }

    pub(crate) fn algorithm(error: LoadBalancerRoutingAlgorithmError, requested: &str) -> Self {
        match error {
            LoadBalancerRoutingAlgorithmError::Unknown { raw } => Self::new(
                "unknown_method",
                format!("unknown routing method '{raw}'"),
                requested,
            ),
            LoadBalancerRoutingAlgorithmError::Unavailable { algorithm, .. } => Self::new(
                "unavailable",
                format!("routing method {algorithm} is not configured for this model"),
                requested,
            ),
        }
    }
}

#[derive(Debug)]
pub(crate) struct RoutingExpression {
    raw: String,
    algorithm: LoadBalancerAlgorithmOverride,
    parameters: Parameters,
}

impl RoutingExpression {
    pub(crate) fn parse(raw: &str) -> Result<Self, RejectionError> {
        let malformed = |message| RejectionError::new("malformed_expression", message, raw);
        if raw.len() > 1024 {
            return Err(malformed("P7: expression exceeds 1024 bytes".to_owned()));
        }
        if raw.contains(',') {
            return Err(malformed("P3: commas are not allowed".to_owned()));
        }

        // Semicolons inside strings must be rejected before splitting parameters.
        let mut quoted = false;
        let mut escaped = false;
        for byte in raw.bytes() {
            if escaped {
                escaped = false;
            } else if quoted && byte == b'\\' {
                escaped = true;
            } else if byte == b'"' {
                quoted = !quoted;
            } else if quoted && byte == b';' {
                return Err(malformed(
                    "P9: semicolons in strings are not allowed".to_owned(),
                ));
            }
        }
        let mut keys = HashSet::new();
        for parameter in raw.split(';').skip(1) {
            if keys.len() == 32 {
                return Err(malformed(
                    "P11: expression exceeds 32 parameters".to_owned(),
                ));
            }
            let (key, _) = parameter
                .trim_start_matches(' ')
                .split_once('=')
                .ok_or_else(|| {
                    malformed(format!(
                        "P4: parameter {parameter:?} requires an explicit value"
                    ))
                })?;
            if !key.as_bytes().first().is_some_and(u8::is_ascii_lowercase)
                || !key
                    .bytes()
                    .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_')
            {
                return Err(malformed(format!("P6: invalid parameter key {key:?}")));
            }
            if !keys.insert(key) {
                return Err(malformed(format!("P5: duplicate parameter key {key:?}")));
            }
        }
        // parse_item consumes the whole input, including checking trailing characters.
        let item: Item = Parser::new(raw)
            .with_version(Version::Rfc8941)
            .parse_item()
            .map_err(|error| malformed(format!("RFC 8941 (P1/P2/P4/P8): {error}")))?;
        let BareItem::Token(token) = item.bare_item else {
            return Err(malformed("P1: routing method must be a token".to_owned()));
        };
        let token = token.as_str();
        if !token
            .as_bytes()
            .first()
            .is_some_and(u8::is_ascii_alphabetic)
            || !token
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
        {
            return Err(malformed(format!("P10: invalid algorithm token {token:?}")));
        }
        for (key, value) in &item.params {
            if matches!(value, BareItem::Boolean(_)) {
                return Err(malformed(format!(
                    "P4: {key} takes true or false, not ?1 or ?0"
                )));
            }
            if !matches!(
                value,
                BareItem::Integer(_)
                    | BareItem::Decimal(_)
                    | BareItem::Token(_)
                    | BareItem::String(_)
            ) {
                return Err(malformed(format!("P2: unsupported type for {key}")));
            }
        }
        let algorithm = LoadBalancerAlgorithmOverride::parse(token)
            .map_err(|error| RejectionError::algorithm(error, raw))?;
        Ok(Self {
            raw: raw.to_owned(),
            algorithm,
            parameters: item.params,
        })
    }

    pub(crate) fn compile(
        &self,
        router: &LoadBalancerRouter,
        model_id: &str,
    ) -> Result<LoadBalancerDefinition, RejectionError> {
        let base = router
            .resolve_algorithm_override(model_id, Some(&self.algorithm))
            .map_err(|error| RejectionError::algorithm(error, &self.raw))?;
        let config = self.overlay(base.config())?;
        LoadBalancerDefinition::new(config)
            .map_err(|error| self.reject("invalid_value", error.to_string()))
    }

    fn reject(&self, class: &'static str, message: impl Into<String>) -> RejectionError {
        RejectionError::new(class, message, &self.raw)
    }

    fn overlay(
        &self,
        base: &LoadBalancerAlgorithmConfig,
    ) -> Result<LoadBalancerAlgorithmConfig, RejectionError> {
        let mut config = base.clone();
        let algorithm = config.algorithm();
        for (key, value) in &self.parameters {
            let key = key.as_str();
            self.check_applicability(key, algorithm)?;
            let parameter = Parameter {
                key,
                value,
                expression: self,
            };
            match key {
                "require_cache_affinity_key" => {
                    config.request_policy.require_cache_affinity_key = parameter.boolean()?
                }
                "require_input_tokens" => {
                    config.request_policy.require_input_tokens = parameter.boolean()?
                }
                "consider_kv_free_tokens" => {
                    config.request_policy.consider_kv_free_tokens = parameter.boolean()?
                }
                "max_input_work_seconds" => {
                    let value = parameter.decimal()?;
                    parameter.require_positive(value > 0.0)?;
                    config.max_input_work_seconds = Some(value);
                }
                "seed" => config
                    .set_seed(Some(parameter.text()?.to_owned()))
                    .map_err(|error| parameter.invalid(error.to_string()))?,
                _ => match &mut config.settings {
                    LoadBalancerAlgorithmSettings::PowerOfN(settings) => match key {
                        "sample_count" => {
                            let value = parameter.unsigned()?;
                            if !(1..=MAX_POWER_OF_N_SAMPLE_COUNT).contains(&value) {
                                return Err(parameter.invalid(format!(
                                    "must be between 1 and {MAX_POWER_OF_N_SAMPLE_COUNT}"
                                )));
                            }
                            settings.sample_count = value;
                        }
                        "comparator" => settings.comparator = parameter.comparator()?,
                        _ => return Err(parameter.invalid("unsupported power-of-n parameter")),
                    },
                    LoadBalancerAlgorithmSettings::WaitAndWiden(settings)
                    | LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(settings) => match key {
                        "n" => settings.n = Some(parameter.positive_unsigned()?),
                        "max_queued" => settings.max_queued = Some(parameter.unsigned()?),
                        "max_queue_time_floor_ms" => {
                            settings.max_queue_time_floor_ms = Some(parameter.unsigned()?)
                        }
                        "max_queue_time_ceil_ms" => {
                            settings.max_queue_time_ceil_ms = Some(parameter.unsigned()?)
                        }
                        "ttft_bucket_size_ms" => {
                            settings.ttft_bucket_size_ms = Some(parameter.positive_unsigned()?)
                        }
                        "next_bucket_unlock_factor" => {
                            settings.next_bucket_unlock_factor = Some(parameter.decimal()?)
                        }
                        "ignore_queue_time" => {
                            settings.ignore_queue_time = Some(parameter.boolean()?)
                        }
                        "ignore_input_processing_time" => {
                            settings.ignore_input_processing_time = Some(parameter.boolean()?)
                        }
                        "comparator" => settings.comparator = Some(parameter.comparator()?),
                        "cache_affinity_virtual_nodes" => {
                            let value = parameter.positive_unsigned()?;
                            if value > MAX_EXPRESSION_VIRTUAL_NODES {
                                return Err(parameter.invalid(format!(
                                    "must be at most {MAX_EXPRESSION_VIRTUAL_NODES}"
                                )));
                            }
                            settings.cache_affinity_virtual_nodes = Some(value);
                        }
                        "cache_affinity_backend_selection_count" => {
                            settings.cache_affinity_backend_selection_count =
                                Some(parameter.positive_unsigned()?)
                        }
                        "cache_affinity_input_tokens_scale" => {
                            let value = parameter.decimal()?;
                            if !(0.0..=1.0).contains(&value) {
                                return Err(parameter.invalid("must be between 0.0 and 1.0"));
                            }
                            settings.cache_affinity_input_tokens_scale = Some(value);
                        }
                        "cache_affinity_wait_ms" => {
                            settings.cache_affinity_wait_ms = Some(parameter.unsigned()?)
                        }
                        _ => return Err(parameter.invalid("unsupported wait-and-widen parameter")),
                    },
                    _ => return Err(parameter.invalid("unsupported parameter")),
                },
            }
        }
        if (self.parameters.contains_key("max_queue_time_floor_ms")
            || self.parameters.contains_key("max_queue_time_ceil_ms"))
            && let Some(settings) = config.wait_and_widen_settings()
            && (settings.max_queue_time_floor_ms.is_none()
                || settings.max_queue_time_ceil_ms.is_none())
        {
            return Err(self.reject(
                "inert_combination",
                "max_queue_time_floor_ms and max_queue_time_ceil_ms must both be configured",
            ));
        }
        Ok(config)
    }

    fn check_applicability(
        &self,
        key: &str,
        algorithm: LoadBalancerAlgorithm,
    ) -> Result<(), RejectionError> {
        use LoadBalancerAlgorithm::{PowerOfN, Pulsar, PulsarWaitAndWiden, WaitAndWiden};
        let applicable = match key {
            "require_cache_affinity_key" | "require_input_tokens" | "max_input_work_seconds" => {
                true
            }
            "sample_count" => algorithm == PowerOfN,
            "comparator" => matches!(algorithm, PowerOfN | WaitAndWiden),
            "seed" => matches!(algorithm, Pulsar | WaitAndWiden | PulsarWaitAndWiden),
            "consider_kv_free_tokens" => matches!(algorithm, Pulsar | PulsarWaitAndWiden),
            "n"
            | "max_queued"
            | "max_queue_time_floor_ms"
            | "max_queue_time_ceil_ms"
            | "ttft_bucket_size_ms"
            | "next_bucket_unlock_factor"
            | "ignore_queue_time"
            | "ignore_input_processing_time" => {
                matches!(algorithm, WaitAndWiden | PulsarWaitAndWiden)
            }
            "cache_affinity_virtual_nodes"
            | "cache_affinity_backend_selection_count"
            | "cache_affinity_input_tokens_scale"
            | "cache_affinity_wait_ms" => algorithm == WaitAndWiden,
            _ => {
                return Err(self.reject(
                    "unknown_parameter",
                    format!("unknown parameter '{key}' for algorithm {algorithm}"),
                ));
            }
        };
        if applicable {
            Ok(())
        } else {
            Err(self.reject(
                "not_applicable",
                format!("{key} is not applicable to {algorithm}"),
            ))
        }
    }
}

struct Parameter<'a> {
    key: &'a str,
    value: &'a BareItem,
    expression: &'a RoutingExpression,
}

impl Parameter<'_> {
    fn invalid(&self, message: impl std::fmt::Display) -> RejectionError {
        self.expression
            .reject("invalid_value", format!("{}: {message}", self.key))
    }

    fn text(&self) -> Result<&str, RejectionError> {
        match self.value {
            BareItem::Token(value) => Ok(value.as_str()),
            BareItem::String(value) => Ok(value.as_str()),
            _ => Err(self.invalid("expected a token or string")),
        }
    }

    fn boolean(&self) -> Result<bool, RejectionError> {
        match self.text()? {
            "true" => Ok(true),
            "false" => Ok(false),
            _ => Err(self.invalid("expected true or false")),
        }
    }

    fn comparator(&self) -> Result<ClusterComparator, RejectionError> {
        use serde::Deserialize;
        ClusterComparator::deserialize(
            serde::de::value::StrDeserializer::<serde::de::value::Error>::new(self.text()?),
        )
        .map_err(|_| self.invalid("unknown comparator"))
    }

    fn unsigned<T>(&self) -> Result<T, RejectionError>
    where
        T: TryFrom<i64> + std::str::FromStr,
    {
        match self.value {
            BareItem::Integer(value) => T::try_from(i64::from(*value))
                .map_err(|_| self.invalid("expected an unsigned integer in range")),
            BareItem::String(value) if is_plain_number(value.as_str()) => value
                .as_str()
                .parse()
                .map_err(|_| self.invalid("expected an unsigned integer in range")),
            _ => Err(self.invalid("expected an integer or quoted integer")),
        }
    }

    fn decimal(&self) -> Result<f64, RejectionError> {
        let value = match self.value {
            BareItem::Integer(value) => i64::from(*value) as f64,
            BareItem::Decimal(value) => f64::from(*value),
            BareItem::String(value) if is_plain_number(value.as_str()) => value
                .as_str()
                .parse()
                .map_err(|_| self.invalid("expected a finite decimal"))?,
            _ => return Err(self.invalid("expected a number or quoted plain number")),
        };
        if value.is_finite() {
            Ok(value)
        } else {
            Err(self.invalid("expected a finite decimal"))
        }
    }

    fn require_positive(&self, is_positive: bool) -> Result<(), RejectionError> {
        if is_positive {
            Ok(())
        } else {
            Err(self
                .expression
                .reject("inert_value", format!("{} must be positive", self.key)))
        }
    }

    fn positive_unsigned<T>(&self) -> Result<T, RejectionError>
    where
        T: TryFrom<i64> + std::str::FromStr + Default + PartialOrd,
    {
        let value = self.unsigned()?;
        self.require_positive(value > T::default())?;
        Ok(value)
    }
}

fn is_plain_number(value: &str) -> bool {
    let value = value.strip_prefix(['+', '-']).unwrap_or(value);
    let (whole, fraction) = value
        .split_once('.')
        .map_or((value, None), |(whole, fraction)| (whole, Some(fraction)));
    !whole.is_empty()
        && whole.bytes().all(|b| b.is_ascii_digit())
        && fraction.is_none_or(|fraction| {
            !fraction.is_empty() && fraction.bytes().all(|b| b.is_ascii_digit())
        })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::load_balancer::{LoadBalancerConfig, LoadBalancerModelConfig};

    #[test]
    fn test_parse_profile_accepts_valid_items() {
        for raw in [
            "pulsar;seed=x",
            " pulsar; seed=x ",
            "PULSAR_WAIT_AND_WIDEN;seed=stable-a",
            "powerOf2;sample_count=2",
            r#"pulsar;seed="spaces and = signs""#,
            r#"pulsar;seed="escaped \" quote""#,
            "pulsar;seed=x;a=999999999999999;b=-999999999999999",
            "wait-and-widen;next_bucket_unlock_factor=0.125",
            r#"wait-and-widen;next_bucket_unlock_factor="0.0625""#,
        ] {
            assert!(RoutingExpression::parse(raw).is_ok(), "{raw:?}");
        }
        let limit = format!("pulsar;seed=\"{}\"", "x".repeat(1010));
        assert_eq!(limit.len(), 1024);
        assert!(RoutingExpression::parse(&limit).is_ok());
        let parameters = (0..32).map(|i| format!(";p{i}=1")).collect::<String>();
        assert!(RoutingExpression::parse(&format!("pulsar{parameters}")).is_ok());
    }

    #[test]
    fn test_parse_profile_rejects_malformed_items() {
        let cases = [
            ("P1 string", r#""pulsar";seed=x"#),
            ("P1 integer", "1;seed=x"),
            ("P2 bytes", "pulsar;seed=:eA==:"),
            ("P2 true", "pulsar;require_input_tokens=?1"),
            ("P2 false", "pulsar;require_input_tokens=?0"),
            ("P2 date", "pulsar;seed=@123"),
            ("P2 display", r#"pulsar;seed=%"x""#),
            ("P3 list", "pulsar;seed=x,random"),
            ("P3 quoted comma", r#"pulsar;seed="x,y""#),
            ("P4 absent value", "pulsar;seed"),
            ("P4 empty value", "pulsar;seed="),
            ("P5 duplicate", "pulsar;seed=x;seed=y"),
            ("P6 uppercase", "pulsar;Seed=x"),
            ("P6 star", "pulsar;*=x"),
            ("P6 hyphen", "pulsar;seed-key=x"),
            ("P6 dot", "pulsar;seed.key=x"),
            ("P6 number", "pulsar;1seed=x"),
            ("P8 integer digits", "pulsar;n=1000000000000000"),
            ("P8 decimal digits", "pulsar;n=1000000000000.0"),
            ("P8 precision", "pulsar;n=0.0625"),
            ("P9 quoted semicolon", r#"pulsar;seed="x;y""#),
            ("P9 escaped quote", r#"pulsar;seed="x\";y""#),
            ("P10 star", "*pulsar;seed=x"),
            ("P10 slash", "pul/sar;seed=x"),
            ("P10 colon", "pul:sar;seed=x"),
            ("space before equals", "pulsar;seed =x"),
            ("space after equals", "pulsar;seed= x"),
            ("space around equals", "pulsar;seed = x"),
            ("tab after semicolon", "pulsar;\tseed=x"),
            ("space before semicolon", "pulsar ;seed=x"),
            ("trailing input", "pulsar;seed=x trailing"),
            ("unclosed string", "pulsar;seed=\"x"),
        ];
        for (label, raw) in cases {
            let error = RoutingExpression::parse(raw).expect_err(label);
            assert_eq!(error.class, "malformed_expression", "{label}: {error}");
            assert_eq!(error.requested, raw, "{label}");
        }
        for raw in [
            format!("pulsar;seed=\"{}\"", "x".repeat(1011)),
            format!(
                "pulsar{}",
                (0..33).map(|i| format!(";p{i}=1")).collect::<String>()
            ),
        ] {
            assert_eq!(
                RoutingExpression::parse(&raw).unwrap_err().class,
                "malformed_expression"
            );
        }
    }

    #[test]
    fn test_parse_profile_rejection_names_the_rule() {
        for (raw, rule) in [
            ("pulsar;require_input_tokens=?1", "P4:"),
            ("pulsar;require_input_tokens=?0", "P4:"),
            ("pulsar;seed=:eA==:", "P2:"),
        ] {
            let error = RoutingExpression::parse(raw).expect_err(raw);
            assert!(error.message.starts_with(rule), "{raw}: {}", error.message);
        }
    }

    #[test]
    fn test_overlay_preserves_omitted_fields_for_every_algorithm() {
        for (algorithm, parameters) in [
            (LoadBalancerAlgorithm::RoundRobin, ""),
            (LoadBalancerAlgorithm::Random, ""),
            (
                LoadBalancerAlgorithm::PowerOfN,
                ";sample_count=3;comparator=queue-time",
            ),
            (
                LoadBalancerAlgorithm::Pulsar,
                ";seed=changed;consider_kv_free_tokens=true",
            ),
            (
                LoadBalancerAlgorithm::WaitAndWiden,
                ";seed=changed;n=3;max_queued=4;ttft_bucket_size_ms=50;next_bucket_unlock_factor=\"0.0625\";ignore_queue_time=true;ignore_input_processing_time=false;max_queue_time_floor_ms=100;max_queue_time_ceil_ms=500;comparator=utilization;cache_affinity_virtual_nodes=8;cache_affinity_backend_selection_count=2;cache_affinity_input_tokens_scale=0.5;cache_affinity_wait_ms=20",
            ),
            (
                LoadBalancerAlgorithm::PulsarWaitAndWiden,
                ";seed=changed;n=3;max_queued=4;ttft_bucket_size_ms=50;next_bucket_unlock_factor=\"0.0625\";ignore_queue_time=true;ignore_input_processing_time=false;max_queue_time_floor_ms=100;max_queue_time_ceil_ms=500;consider_kv_free_tokens=true",
            ),
        ] {
            let mut base = LoadBalancerAlgorithmConfig::from(algorithm);
            base.request_policy.require_input_tokens = true;
            base.max_input_work_seconds = Some(9.0);
            let mut expected = base.clone();
            expected.request_policy.require_cache_affinity_key = true;
            expected.max_input_work_seconds = Some(2.5);
            match &mut expected.settings {
                LoadBalancerAlgorithmSettings::PowerOfN(settings) => {
                    settings.sample_count = 3;
                    settings.comparator = ClusterComparator::QueueTime;
                }
                LoadBalancerAlgorithmSettings::Pulsar(seed) => {
                    *seed = Some("changed".to_owned());
                    expected.request_policy.consider_kv_free_tokens = true;
                }
                LoadBalancerAlgorithmSettings::WaitAndWiden(settings)
                | LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(settings) => {
                    settings.seed = Some("changed".to_owned());
                    settings.n = Some(3);
                    settings.max_queued = Some(4);
                    settings.ttft_bucket_size_ms = Some(50);
                    settings.next_bucket_unlock_factor = Some(0.0625);
                    settings.ignore_queue_time = Some(true);
                    settings.ignore_input_processing_time = Some(false);
                    settings.max_queue_time_floor_ms = Some(100);
                    settings.max_queue_time_ceil_ms = Some(500);
                    if algorithm == LoadBalancerAlgorithm::WaitAndWiden {
                        settings.comparator = Some(ClusterComparator::Utilization);
                        settings.cache_affinity_virtual_nodes = Some(8);
                        settings.cache_affinity_backend_selection_count = Some(2);
                        settings.cache_affinity_input_tokens_scale = Some(0.5);
                        settings.cache_affinity_wait_ms = Some(20);
                    } else {
                        expected.request_policy.consider_kv_free_tokens = true;
                    }
                }
                _ => {}
            }
            let expression = RoutingExpression::parse(&format!(
                "{algorithm};require_cache_affinity_key=true;max_input_work_seconds=2.5{parameters}"
            ))
            .unwrap();
            let actual = expression.overlay(&base).unwrap();
            assert_eq!(actual.settings, expected.settings, "{algorithm}");
            assert_eq!(
                actual.request_policy, expected.request_policy,
                "{algorithm}"
            );
            assert_eq!(
                actual.max_input_work_seconds, expected.max_input_work_seconds,
                "{algorithm}"
            );
            assert!(!base.request_policy.require_cache_affinity_key);
            let unchanged =
                RoutingExpression::parse(&format!("{algorithm};require_input_tokens=false"))
                    .unwrap()
                    .overlay(&expected)
                    .unwrap();
            assert_eq!(
                unchanged.settings, expected.settings,
                "{algorithm} omitted fields"
            );
            assert_eq!(
                unchanged.max_input_work_seconds,
                expected.max_input_work_seconds
            );
            assert!(!unchanged.request_policy.require_input_tokens);
        }
    }

    #[test]
    fn test_compile_classifies_semantic_rejections() {
        let router =
            LoadBalancerRouter::from_config(&LoadBalancerConfig::permissive_default()).unwrap();
        for (raw, class) in [
            ("fastest;seed=x", "unknown_method"),
            ("pulsar;widen=2", "unknown_parameter"),
            ("round-robin;seed=x", "not_applicable"),
            ("pulsar-wait-and-widen;comparator=ttft", "not_applicable"),
            (
                "pulsar-wait-and-widen;cache_affinity_virtual_nodes=1",
                "not_applicable",
            ),
            (
                "pulsar-wait-and-widen;cache_affinity_backend_selection_count=1",
                "not_applicable",
            ),
            (
                "pulsar-wait-and-widen;cache_affinity_input_tokens_scale=0.5",
                "not_applicable",
            ),
            (
                "pulsar-wait-and-widen;cache_affinity_wait_ms=1",
                "not_applicable",
            ),
            (
                "wait-and-widen;consider_kv_free_tokens=false",
                "not_applicable",
            ),
            ("power-of-n;sample_count=banana", "invalid_value"),
            ("power-of-n;sample_count=999", "invalid_value"),
            ("power-of-n;sample_count=0", "invalid_value"),
            ("power-of-n;sample_count=-1", "invalid_value"),
            ("power-of-n;sample_count=1.5", "invalid_value"),
            ("power-of-n;sample_count=\"1.0\"", "invalid_value"),
            ("power-of-n;comparator=fastest", "invalid_value"),
            (
                "wait-and-widen;next_bucket_unlock_factor=\"inf\"",
                "invalid_value",
            ),
            (
                "wait-and-widen;cache_affinity_input_tokens_scale=1.1",
                "invalid_value",
            ),
            (
                "wait-and-widen;cache_affinity_virtual_nodes=1025",
                "invalid_value",
            ),
            (
                "wait-and-widen;cache_affinity_virtual_nodes=999999999999999",
                "invalid_value",
            ),
            ("wait-and-widen;max_queued=-1", "invalid_value"),
            ("pulsar;require_input_tokens=True", "invalid_value"),
            ("pulsar;require_input_tokens=1", "invalid_value"),
            ("pulsar;seed=1", "invalid_value"),
            ("wait-and-widen;ttft_bucket_size_ms=0", "inert_value"),
            ("wait-and-widen;n=0", "inert_value"),
            (
                "wait-and-widen;cache_affinity_virtual_nodes=0",
                "inert_value",
            ),
            (
                "wait-and-widen;cache_affinity_backend_selection_count=0",
                "inert_value",
            ),
            ("pulsar;max_input_work_seconds=0", "inert_value"),
            ("pulsar;max_input_work_seconds=-1.0", "inert_value"),
            (
                "wait-and-widen;max_queue_time_floor_ms=100",
                "inert_combination",
            ),
            (
                "wait-and-widen;max_queue_time_ceil_ms=500",
                "inert_combination",
            ),
        ] {
            let error = RoutingExpression::parse(raw)
                .and_then(|expression| expression.compile(&router, "model"))
                .expect_err(raw);
            assert_eq!(error.class, class, "{raw}: {error}");
            assert_eq!(error.requested, raw);
            assert!(!error.message.is_empty());
        }
        let at_limit = RoutingExpression::parse("wait-and-widen;cache_affinity_virtual_nodes=1024")
            .unwrap()
            .compile(&router, "model")
            .unwrap();
        assert_eq!(
            at_limit
                .config()
                .wait_and_widen_settings()
                .unwrap()
                .cache_affinity_virtual_nodes,
            Some(1024)
        );
        let router = LoadBalancerRouter::from_config(&LoadBalancerConfig::default()).unwrap();
        let raw = "pulsar;seed=x";
        assert_eq!(
            RoutingExpression::parse(raw)
                .unwrap()
                .compile(&router, "model")
                .unwrap_err()
                .class,
            "unavailable"
        );
    }

    #[test]
    fn test_compile_rejection_messages_name_the_offending_input() {
        let router =
            LoadBalancerRouter::from_config(&LoadBalancerConfig::permissive_default()).unwrap();
        for (raw, message) in [
            ("fastest;seed=x", "unknown routing method 'fastest'"),
            (
                "pulsar;widen=2",
                "unknown parameter 'widen' for algorithm pulsar",
            ),
        ] {
            let error = RoutingExpression::parse(raw)
                .and_then(|expression| expression.compile(&router, "model"))
                .expect_err(raw);
            assert_eq!(error.message, message, "{raw}");
        }
    }

    #[test]
    fn test_quoted_numbers_require_plain_finite_content() {
        let base = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::WaitAndWiden);
        for (value, expected) in [
            ("0.0625", Some(0.0625)),
            ("+0.0625", Some(0.0625)),
            ("-0.0625", Some(-0.0625)),
            ("NaN", None),
            ("inf", None),
            ("-inf", None),
            ("1e2", None),
            ("0x10", None),
            (".5", None),
            ("1.", None),
            (" 1", None),
            ("1 ", None),
            ("+", None),
            ("", None),
            ("1.2.3", None),
        ] {
            let expression = RoutingExpression::parse(&format!(
                "wait-and-widen;next_bucket_unlock_factor=\"{value}\""
            ))
            .unwrap();
            let actual = expression.overlay(&base);
            match expected {
                Some(value) => assert_eq!(
                    actual
                        .unwrap()
                        .wait_and_widen_settings()
                        .unwrap()
                        .next_bucket_unlock_factor,
                    Some(value)
                ),
                None => assert_eq!(actual.unwrap_err().class, "invalid_value", "{value}"),
            }
        }
        let huge = "9".repeat(400);
        assert_eq!(
            RoutingExpression::parse(&format!(
                "wait-and-widen;next_bucket_unlock_factor=\"{huge}\""
            ))
            .unwrap()
            .overlay(&base)
            .unwrap_err()
            .class,
            "invalid_value"
        );
    }

    #[test]
    fn test_overlay_uses_resolved_base_and_completes_bounds() {
        let mut base = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::WaitAndWiden);
        let LoadBalancerAlgorithmSettings::WaitAndWiden(settings) = &mut base.settings else {
            unreachable!()
        };
        settings.max_queue_time_ceil_ms = Some(500);
        settings.n = Some(0); // Static inert values retain their existing behavior.
        let router = LoadBalancerRouter::from_config(&LoadBalancerConfig {
            default: LoadBalancerAlgorithm::Random,
            request_algorithms: std::collections::HashMap::from([(
                LoadBalancerAlgorithm::WaitAndWiden,
                LoadBalancerModelConfig::Detailed(Box::new(base)),
            )]),
            models: Default::default(),
        })
        .unwrap();
        let definition = RoutingExpression::parse(
            "wait-and-widen;max_queue_time_floor_ms=\"100\";ignore_queue_time=\"true\"",
        )
        .unwrap()
        .compile(&router, "model")
        .unwrap();
        let settings = definition.config().wait_and_widen_settings().unwrap();
        assert_eq!(settings.max_queue_time_floor_ms, Some(100));
        assert_eq!(settings.max_queue_time_ceil_ms, Some(500));
        assert_eq!(settings.n, Some(0));
        assert_eq!(settings.ignore_queue_time, Some(true));
    }
}
