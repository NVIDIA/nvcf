// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, BTreeSet, btree_map::Entry};
use std::fmt;
use std::marker::PhantomData;
use std::num::{NonZeroU64, NonZeroUsize};
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, ensure};
use serde::de::{Error, MapAccess, Visitor};
use serde::{Deserialize, Deserializer, Serialize};

use crate::workload::Workload;

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Algorithm {
    WaitAndWiden,
    PowerOfN,
}

impl Algorithm {
    pub const ALL: [Self; 2] = [Self::WaitAndWiden, Self::PowerOfN];

    pub fn as_str(self) -> &'static str {
        match self {
            Self::WaitAndWiden => "wait-and-widen",
            Self::PowerOfN => "power-of-n",
        }
    }
}

impl fmt::Display for Algorithm {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Plan {
    pub arms: Vec<Arm>,
    pub workloads: BTreeMap<String, Workload>,
    pub routing_headers: BTreeMap<Algorithm, Option<String>>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Arm {
    pub directory: PathBuf,
    pub algorithm: Algorithm,
    pub scenario: String,
    pub reset_cache: bool,
    pub cooldown_seconds: u64,
    pub warmup: Option<Stream>,
    pub streams: Vec<Stream>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Stream {
    pub name: Option<String>,
    pub workload: String,
    pub rate: usize,
    pub workers: usize,
    pub limit: RunLimit,
    pub limits: RequestLimits,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(tag = "kind", content = "value", rename_all = "kebab-case")]
pub enum RunLimit {
    Requests(usize),
    DurationSeconds(u64),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RequestLimits {
    pub max_tokens: usize,
    pub timeout_seconds: u64,
    pub request_slo_ms: u64,
    pub max_wait_ms: u64,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Suite {
    version: u32,
    defaults: Defaults,
    #[serde(deserialize_with = "unique_map")]
    algorithms: BTreeMap<Algorithm, AlgorithmConfig>,
    #[serde(deserialize_with = "unique_map")]
    pub suites: BTreeMap<String, Vec<String>>,
    #[serde(deserialize_with = "unique_map")]
    scenarios: BTreeMap<String, Scenario>,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Defaults {
    max_tokens: usize,
    timeout_seconds: NonZeroU64,
    request_slo_ms: NonZeroU64,
    max_wait_ms: NonZeroU64,
    cooldown_seconds: u64,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct AlgorithmConfig {
    // Require the field while allowing null to select the deployed policy.
    #[serde(deserialize_with = "Deserialize::deserialize")]
    routing_header: Option<String>,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(
    tag = "kind",
    rename_all = "kebab-case",
    rename_all_fields = "camelCase",
    deny_unknown_fields
)]
enum Scenario {
    Requests {
        requests: NonZeroUsize,
        rate: NonZeroUsize,
        workers: NonZeroUsize,
        prompt_bytes: NonZeroUsize,
    },
    Sweep {
        rates: Vec<NonZeroUsize>,
        duration_seconds: NonZeroU64,
        minimum_workers: NonZeroUsize,
        workers_per_rps: NonZeroUsize,
        prompt_bytes: NonZeroUsize,
    },
    SessionAffinity {
        sessions: NonZeroUsize,
        turns_per_session: NonZeroUsize,
        repeats: NonZeroUsize,
        rate: NonZeroUsize,
        workers: NonZeroUsize,
        stable_prefix_bytes: NonZeroUsize,
        turn_bytes: NonZeroUsize,
        request_slo_ms: Option<NonZeroU64>,
        max_wait_ms: Option<NonZeroU64>,
        timeout_seconds: Option<NonZeroU64>,
    },
    SessionWorkers {
        workers: NonZeroUsize,
        session_tasks: Vec<Vec<usize>>,
        repeats: NonZeroUsize,
        rate: NonZeroUsize,
        request_slo_ms: Option<NonZeroU64>,
        max_wait_ms: Option<NonZeroU64>,
        timeout_seconds: Option<NonZeroU64>,
    },
    MixedSessions {
        duration_seconds: NonZeroU64,
        repeats: NonZeroUsize,
        hot: Hot,
        short: Short,
    },
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Hot {
    rate: NonZeroUsize,
    workers: NonZeroUsize,
    min_prompt_bytes: NonZeroUsize,
    max_prompt_bytes: NonZeroUsize,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Short {
    rate: NonZeroUsize,
    workers: NonZeroUsize,
    prompt_bytes_by_turn: Vec<usize>,
}

fn unique_map<'de, D, K, V>(deserializer: D) -> std::result::Result<BTreeMap<K, V>, D::Error>
where
    D: Deserializer<'de>,
    K: Deserialize<'de> + Ord + fmt::Display,
    V: Deserialize<'de>,
{
    struct UniqueMap<K, V>(PhantomData<(K, V)>);

    impl<'de, K, V> Visitor<'de> for UniqueMap<K, V>
    where
        K: Deserialize<'de> + Ord + fmt::Display,
        V: Deserialize<'de>,
    {
        type Value = BTreeMap<K, V>;

        fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
            formatter.write_str("a mapping with unique keys")
        }

        fn visit_map<A>(self, mut source: A) -> std::result::Result<Self::Value, A::Error>
        where
            A: MapAccess<'de>,
        {
            let mut entries = BTreeMap::new();
            while let Some((key, value)) = source.next_entry()? {
                match entries.entry(key) {
                    Entry::Vacant(entry) => {
                        entry.insert(value);
                    }
                    Entry::Occupied(entry) => {
                        return Err(A::Error::custom(format!("duplicate key {}", entry.key())));
                    }
                }
            }
            Ok(entries)
        }
    }

    deserializer.deserialize_map(UniqueMap(PhantomData))
}

fn validate_name(name: &str) -> Result<()> {
    ensure!(
        name.as_bytes()
            .first()
            .is_some_and(u8::is_ascii_alphanumeric)
            && name
                .bytes()
                .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_')),
        "unsafe suite or scenario name {name:?}: use ASCII letters, digits, hyphens or underscores, starting with a letter or digit"
    );
    Ok(())
}

fn reserved_count(rate: usize, seconds: u64) -> Result<usize> {
    let base = rate
        .checked_mul(usize::try_from(seconds).context("duration exceeds platform size")?)
        .context("rate times duration exceeds platform size")?;
    base.checked_add(base.div_ceil(20))
        .context("prompt count with five percent reserve exceeds platform size")
}

fn algorithm_order(selected: &[Algorithm], reverse: bool) -> impl Iterator<Item = Algorithm> + '_ {
    let mut order = Algorithm::ALL;
    if reverse {
        order.reverse();
    }
    order
        .into_iter()
        .filter(|algorithm| selected.contains(algorithm))
}

impl Scenario {
    fn limits(&self, defaults: &Defaults) -> RequestLimits {
        let (request_slo_ms, max_wait_ms, timeout_seconds) = match self {
            Self::SessionAffinity {
                request_slo_ms,
                max_wait_ms,
                timeout_seconds,
                ..
            }
            | Self::SessionWorkers {
                request_slo_ms,
                max_wait_ms,
                timeout_seconds,
                ..
            } => (*request_slo_ms, *max_wait_ms, *timeout_seconds),
            _ => (None, None, None),
        };
        RequestLimits {
            max_tokens: defaults.max_tokens,
            timeout_seconds: timeout_seconds.unwrap_or(defaults.timeout_seconds).get(),
            request_slo_ms: request_slo_ms.unwrap_or(defaults.request_slo_ms).get(),
            max_wait_ms: max_wait_ms.unwrap_or(defaults.max_wait_ms).get(),
        }
    }

    fn affinity_workload(&self) -> Option<Workload> {
        match self {
            Self::SessionAffinity {
                sessions,
                turns_per_session,
                stable_prefix_bytes,
                turn_bytes,
                ..
            } => Some(Workload::Sessions {
                sessions: sessions.get(),
                turns: turns_per_session.get(),
                stable_prefix_bytes: stable_prefix_bytes.get(),
                turn_bytes: turn_bytes.get(),
            }),
            Self::SessionWorkers {
                workers,
                session_tasks,
                ..
            } => Some(Workload::SessionWorkers {
                workers: workers.get(),
                session_tasks: session_tasks.clone(),
            }),
            _ => None,
        }
    }

    fn validate(&self) -> Result<()> {
        if let Some(workload) = self.affinity_workload() {
            return workload.validate();
        }
        match self {
            Self::Requests {
                requests,
                prompt_bytes,
                ..
            } => Workload::Unique {
                count: requests.get(),
                bytes: prompt_bytes.get(),
                start: 0,
            }
            .validate(),
            Self::Sweep {
                rates,
                duration_seconds,
                workers_per_rps,
                prompt_bytes,
                ..
            } => {
                ensure!(!rates.is_empty(), "sweep must include at least one rate");
                ensure!(
                    rates.iter().collect::<BTreeSet<_>>().len() == rates.len(),
                    "sweep rates must be distinct"
                );
                for rate in rates {
                    rate.get()
                        .checked_mul(workers_per_rps.get())
                        .context("sweep worker count exceeds platform size")?;
                    Workload::Unique {
                        count: reserved_count(rate.get(), duration_seconds.get())?,
                        bytes: prompt_bytes.get(),
                        start: 0,
                    }
                    .validate()?;
                }
                Ok(())
            }
            Self::MixedSessions {
                duration_seconds,
                hot,
                short,
                ..
            } => {
                Workload::Hot {
                    count: reserved_count(hot.rate.get(), duration_seconds.get())?,
                    minimum: hot.min_prompt_bytes.get(),
                    maximum: hot.max_prompt_bytes.get(),
                }
                .validate()?;
                Workload::Short {
                    count: reserved_count(short.rate.get(), duration_seconds.get())?,
                    sizes: short.prompt_bytes_by_turn.clone(),
                }
                .validate()
            }
            Self::SessionAffinity { .. } | Self::SessionWorkers { .. } => unreachable!(),
        }
    }
}

impl Suite {
    pub fn load(path: &Path) -> Result<Self> {
        let contents = std::fs::read_to_string(path)
            .with_context(|| format!("read benchmark suite {}", path.display()))?;
        Self::from_yaml(&contents)
            .with_context(|| format!("load benchmark suite {}", path.display()))
    }

    pub fn from_yaml(contents: &str) -> Result<Self> {
        let suite: Self = serde_yaml_ng::from_str(contents).context("parse benchmark suite")?;
        ensure!(suite.version == 1, "benchmark suite version must be 1");
        ensure!(!suite.suites.is_empty(), "suite must define named suites");
        ensure!(!suite.scenarios.is_empty(), "suite must define scenarios");
        for algorithm in Algorithm::ALL {
            let config = suite
                .algorithms
                .get(&algorithm)
                .with_context(|| format!("missing algorithm {algorithm}"))?;
            ensure!(
                config
                    .routing_header
                    .as_ref()
                    .is_none_or(|header| !header.is_empty()),
                "routing header for {algorithm} must be nonempty or null"
            );
        }
        for (name, scenario) in &suite.scenarios {
            validate_name(name)?;
            scenario
                .validate()
                .with_context(|| format!("invalid scenario {name}"))?;
        }
        for (name, scenarios) in &suite.suites {
            validate_name(name)?;
            ensure!(
                !scenarios.is_empty(),
                "suite {name} must include a scenario"
            );
            let mut seen = BTreeSet::new();
            for scenario in scenarios {
                ensure!(
                    seen.insert(scenario),
                    "suite {name} repeats scenario {scenario}"
                );
                ensure!(
                    suite.scenarios.contains_key(scenario),
                    "suite {name} refers to unknown scenario {scenario}"
                );
            }
        }
        Ok(suite)
    }

    pub fn plan(&self, name: &str, algorithms: &[Algorithm]) -> Result<Plan> {
        ensure!(!algorithms.is_empty(), "select at least one algorithm");
        ensure!(
            algorithms.iter().collect::<BTreeSet<_>>().len() == algorithms.len(),
            "selected algorithms must be distinct"
        );
        let scenarios = self
            .suites
            .get(name)
            .with_context(|| format!("unknown suite {name:?}"))?;
        let arm_count = scenarios.iter().try_fold(0_usize, |total, scenario_name| {
            let scenario = self
                .scenarios
                .get(scenario_name)
                .with_context(|| format!("unknown scenario {scenario_name}"))?;
            let pairs = match scenario {
                Scenario::Requests { .. } => 1,
                Scenario::Sweep { rates, .. } => rates.len(),
                Scenario::SessionAffinity { repeats, .. }
                | Scenario::SessionWorkers { repeats, .. }
                | Scenario::MixedSessions { repeats, .. } => repeats.get(),
            };
            pairs
                .checked_mul(algorithms.len())
                .and_then(|count| total.checked_add(count))
                .context("planned arm count exceeds platform size")
        })?;
        let mut plan = Plan {
            arms: Vec::new(),
            workloads: BTreeMap::new(),
            routing_headers: algorithms
                .iter()
                .map(|algorithm| {
                    (
                        *algorithm,
                        self.algorithms[algorithm].routing_header.clone(),
                    )
                })
                .collect(),
        };
        plan.arms
            .try_reserve_exact(arm_count)
            .context("allocate planned arms")?;

        let mut next_unique = 0_u64;
        for (scenario_name, scenario) in &self.scenarios {
            if let Scenario::Requests {
                requests,
                prompt_bytes,
                ..
            } = scenario
            {
                if scenarios.contains(scenario_name) {
                    plan.add_workload(
                        format!("{scenario_name}.yaml"),
                        Workload::Unique {
                            count: requests.get(),
                            bytes: prompt_bytes.get(),
                            start: next_unique,
                        },
                    )?;
                }
                next_unique = next_unique
                    .checked_add(
                        u64::try_from(requests.get()).context("request count exceeds u64")?,
                    )
                    .context("unique prompt range exceeds u64")?;
            }
        }

        for scenario_name in scenarios {
            let scenario = self
                .scenarios
                .get(scenario_name)
                .with_context(|| format!("unknown scenario {scenario_name}"))?;
            let limits = scenario.limits(&self.defaults);
            match scenario {
                Scenario::Requests {
                    requests,
                    rate,
                    workers,
                    ..
                } => {
                    for algorithm in algorithm_order(algorithms, false) {
                        plan.arms.push(Arm {
                            directory: Path::new(scenario_name).join(algorithm.as_str()),
                            algorithm,
                            scenario: scenario_name.clone(),
                            reset_cache: false,
                            cooldown_seconds: 0,
                            warmup: None,
                            streams: vec![Stream {
                                name: None,
                                workload: format!("{scenario_name}.yaml"),
                                rate: rate.get(),
                                workers: workers.get(),
                                limit: RunLimit::Requests(requests.get()),
                                limits,
                            }],
                        });
                    }
                }
                Scenario::Sweep {
                    rates,
                    duration_seconds,
                    minimum_workers,
                    workers_per_rps,
                    prompt_bytes,
                } => {
                    for (index, rate) in rates.iter().enumerate() {
                        let count = reserved_count(rate.get(), duration_seconds.get())?;
                        let mut files = BTreeMap::new();
                        for algorithm in algorithm_order(algorithms, false) {
                            let file = format!("{scenario_name}-r{rate}-{algorithm}.yaml");
                            plan.add_workload(
                                file.clone(),
                                Workload::Unique {
                                    count,
                                    bytes: prompt_bytes.get(),
                                    start: next_unique,
                                },
                            )?;
                            next_unique = next_unique
                                .checked_add(
                                    u64::try_from(count).context("prompt count exceeds u64")?,
                                )
                                .context("unique prompt range exceeds u64")?;
                            files.insert(algorithm, file);
                        }
                        for algorithm in algorithm_order(algorithms, index % 2 == 1) {
                            let workers = minimum_workers.get().max(
                                rate.get()
                                    .checked_mul(workers_per_rps.get())
                                    .context("sweep worker count exceeds platform size")?,
                            );
                            plan.arms.push(Arm {
                                directory: Path::new(scenario_name)
                                    .join(format!("r{:02}", rate.get()))
                                    .join(algorithm.as_str()),
                                algorithm,
                                scenario: scenario_name.clone(),
                                reset_cache: false,
                                cooldown_seconds: self.defaults.cooldown_seconds,
                                warmup: None,
                                streams: vec![Stream {
                                    name: None,
                                    workload: files
                                        .remove(&algorithm)
                                        .context("missing sweep workload")?,
                                    rate: rate.get(),
                                    workers,
                                    limit: RunLimit::DurationSeconds(duration_seconds.get()),
                                    limits,
                                }],
                            });
                        }
                    }
                }
                Scenario::SessionAffinity {
                    repeats,
                    rate,
                    workers,
                    ..
                }
                | Scenario::SessionWorkers {
                    repeats,
                    rate,
                    workers,
                    ..
                } => {
                    let workload = scenario
                        .affinity_workload()
                        .context("missing affinity workload")?;
                    let requests = workload.prompt_count()?;
                    let file = format!("{scenario_name}.yaml");
                    plan.add_workload(file.clone(), workload)?;
                    for repeat in 1..=repeats.get() {
                        for algorithm in algorithm_order(algorithms, repeat % 2 == 0) {
                            plan.arms.push(Arm {
                                directory: Path::new(scenario_name)
                                    .join(format!("repeat-{repeat:02}"))
                                    .join(algorithm.as_str()),
                                algorithm,
                                scenario: scenario_name.clone(),
                                reset_cache: true,
                                cooldown_seconds: 0,
                                warmup: None,
                                streams: vec![Stream {
                                    name: None,
                                    workload: file.clone(),
                                    rate: rate.get(),
                                    workers: workers.get(),
                                    limit: RunLimit::Requests(requests),
                                    limits,
                                }],
                            });
                        }
                    }
                }
                Scenario::MixedSessions {
                    duration_seconds,
                    repeats,
                    hot,
                    short,
                } => {
                    let hot_file = format!("{scenario_name}-hot.yaml");
                    let short_file = format!("{scenario_name}-short.yaml");
                    plan.add_workload(
                        hot_file.clone(),
                        Workload::Hot {
                            count: reserved_count(hot.rate.get(), duration_seconds.get())?,
                            minimum: hot.min_prompt_bytes.get(),
                            maximum: hot.max_prompt_bytes.get(),
                        },
                    )?;
                    plan.add_workload(
                        short_file.clone(),
                        Workload::Short {
                            count: reserved_count(short.rate.get(), duration_seconds.get())?,
                            sizes: short.prompt_bytes_by_turn.clone(),
                        },
                    )?;
                    for repeat in 1..=repeats.get() {
                        for algorithm in algorithm_order(algorithms, repeat % 2 == 1) {
                            plan.arms.push(Arm {
                                directory: Path::new(scenario_name)
                                    .join(format!("repeat-{repeat:02}"))
                                    .join(algorithm.as_str()),
                                algorithm,
                                scenario: scenario_name.clone(),
                                reset_cache: true,
                                cooldown_seconds: 0,
                                warmup: Some(Stream {
                                    name: Some("warm".into()),
                                    workload: hot_file.clone(),
                                    rate: 1,
                                    workers: 1,
                                    limit: RunLimit::Requests(1),
                                    limits,
                                }),
                                streams: vec![
                                    Stream {
                                        name: Some("hot".into()),
                                        workload: hot_file.clone(),
                                        rate: hot.rate.get(),
                                        workers: hot.workers.get(),
                                        limit: RunLimit::DurationSeconds(duration_seconds.get()),
                                        limits,
                                    },
                                    Stream {
                                        name: Some("short".into()),
                                        workload: short_file.clone(),
                                        rate: short.rate.get(),
                                        workers: short.workers.get(),
                                        limit: RunLimit::DurationSeconds(duration_seconds.get()),
                                        limits,
                                    },
                                ],
                            });
                        }
                    }
                }
            }
        }
        if scenarios.first().is_some_and(|name| {
            matches!(
                self.scenarios.get(name),
                Some(Scenario::Requests { .. } | Scenario::Sweep { .. })
            )
        }) && let Some(arm) = plan.arms.first_mut()
        {
            arm.reset_cache = true;
        }
        for arm in &plan.arms {
            for stream in arm.warmup.iter().chain(&arm.streams) {
                stream.validate().with_context(|| {
                    format!(
                        "invalid Spark stream {} in {}",
                        stream.name.as_deref().unwrap_or("main"),
                        arm.directory.display()
                    )
                })?;
            }
        }
        Ok(plan)
    }
}

impl Stream {
    fn validate(&self) -> Result<()> {
        ensure!(
            self.workers <= i32::MAX as usize,
            "workers {} exceeds Spark's i32 limit ({})",
            self.workers,
            i32::MAX
        );
        ensure!(
            self.limits.max_tokens <= i32::MAX as usize,
            "maxTokens {} exceeds Spark's i32 limit ({})",
            self.limits.max_tokens,
            i32::MAX
        );
        ensure!(
            self.rate <= 1_000_000_000,
            "rate {} exceeds Spark's limit (1000000000 requests per second)",
            self.rate
        );
        if let RunLimit::Requests(requests) = self.limit {
            ensure!(
                requests <= i32::MAX as usize,
                "requests {requests} exceeds Spark's i32 limit ({})",
                i32::MAX
            );
        }
        Ok(())
    }
}

impl Plan {
    pub fn minimum_measured_minutes(&self) -> f64 {
        self.arms
            .iter()
            .map(|arm| {
                arm.streams
                    .iter()
                    .map(|stream| match stream.limit {
                        RunLimit::Requests(requests) => requests as f64 / stream.rate as f64,
                        RunLimit::DurationSeconds(seconds) => seconds as f64,
                    })
                    .fold(0.0, f64::max)
            })
            .sum::<f64>()
            / 60.0
    }

    fn add_workload(&mut self, name: String, workload: Workload) -> Result<()> {
        workload
            .validate()
            .with_context(|| format!("invalid workload {name}"))?;
        ensure!(
            self.workloads.insert(name.clone(), workload).is_none(),
            "workload filename conflict: {name}"
        );
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const CANONICAL: &str = include_str!("../../loadtest/suite.yaml");

    #[test]
    fn canonical_plan_preserves_order_and_execution_controls() -> Result<()> {
        let suite = Suite::from_yaml(CANONICAL)?;
        let plan = suite.plan("canonical", &Algorithm::ALL)?;
        assert_eq!(plan.arms.len(), 24);
        assert_eq!(plan.workloads.len(), 15);
        assert_eq!(plan.arms[0].directory, Path::new("smoke/wait-and-widen"));
        assert!(plan.arms[0].reset_cache);
        assert!(!plan.arms[1].reset_cache);
        let sweep: Vec<_> = plan
            .arms
            .iter()
            .filter(|arm| arm.scenario == "saturation")
            .collect();
        assert_eq!(
            sweep[0].directory,
            Path::new("saturation/r08/wait-and-widen")
        );
        assert_eq!(sweep[2].directory, Path::new("saturation/r16/power-of-n"));
        assert!(sweep.iter().all(|arm| arm.cooldown_seconds == 60));
        let affinity: Vec<_> = plan
            .arms
            .iter()
            .filter(|arm| arm.scenario == "session-affinity")
            .collect();
        assert_eq!(affinity[2].algorithm, Algorithm::PowerOfN);
        assert!(affinity.iter().all(|arm| arm.reset_cache));
        assert_eq!(affinity[0].streams[0].limit, RunLimit::Requests(6144));
        let long = plan
            .arms
            .iter()
            .find(|arm| arm.scenario == "long-context-affinity")
            .unwrap();
        assert_eq!(long.streams[0].limit, RunLimit::Requests(768));
        assert_eq!(
            long.streams[0].limits,
            RequestLimits {
                max_tokens: 256,
                timeout_seconds: 90,
                request_slo_ms: 60_000,
                max_wait_ms: 60_000
            }
        );
        let mixed: Vec<_> = plan
            .arms
            .iter()
            .filter(|arm| arm.scenario == "mixed-sessions")
            .collect();
        assert_eq!(mixed[0].algorithm, Algorithm::PowerOfN);
        assert_eq!(mixed[2].algorithm, Algorithm::WaitAndWiden);
        assert_eq!(
            mixed[0].warmup.as_ref().unwrap().limit,
            RunLimit::Requests(1)
        );
        assert_eq!(mixed[0].streams.len(), 2);
        assert_eq!(mixed[0].streams[0].limit, RunLimit::DurationSeconds(600));
        assert!(mixed.iter().all(|arm| arm.reset_cache));
        assert_eq!(plan.routing_headers[&Algorithm::WaitAndWiden], None);
        assert_eq!(
            plan.routing_headers[&Algorithm::PowerOfN].as_deref(),
            Some("powerOfN")
        );
        Ok(())
    }

    #[test]
    fn cold_prompt_ranges_are_disjoint_and_algorithm_filter_keeps_order() -> Result<()> {
        let suite = Suite::from_yaml(CANONICAL)?;
        let plan = suite.plan("capacity", &Algorithm::ALL)?;
        let mut ranges = Vec::new();
        for workload in plan.workloads.values() {
            let Workload::Unique { count, start, .. } = workload else {
                panic!("unexpected capacity workload")
            };
            ranges.push((*start, *start + *count as u64));
        }
        ranges.sort_unstable();
        assert_eq!(ranges[0], (0, 32));
        assert_eq!(ranges[1], (32, 1040));
        for pair in ranges.windows(2) {
            assert_eq!(pair[0].1, pair[1].0);
        }
        let single = suite.plan("canonical", &[Algorithm::PowerOfN])?;
        assert_eq!(single.arms.len(), 12);
        assert!(
            single
                .arms
                .iter()
                .all(|arm| arm.algorithm == Algorithm::PowerOfN)
        );
        let reversed = suite.plan("canonical", &[Algorithm::PowerOfN, Algorithm::WaitAndWiden])?;
        assert_eq!(
            serde_json::to_value(plan.arms[0].algorithm)?,
            serde_json::to_value(reversed.arms[0].algorithm)?
        );
        Ok(())
    }

    #[test]
    fn rejects_ambiguous_or_unsafe_input() {
        for source in [
            CANONICAL.replacen("version: 1", "version: 2", 1),
            CANONICAL.replacen("version: 1", "version: 1\nversion: 1", 1),
            CANONICAL.replacen("version: 1", "version: 1\nunknown: true", 1),
            CANONICAL.replacen(
                "  power-of-n:\n    routingHeader: powerOfN",
                "  power-of-n: {}",
                1,
            ),
            CANONICAL.replacen("requests: 32", "requests: 0", 1),
            CANONICAL.replacen("rate: 8", "rate: 0", 1),
            CANONICAL.replacen("workers: 8", "workers: 0", 1),
            CANONICAL.replacen("rates: [8, 16, 24, 32, 48]", "rates: [8, 8]", 1),
            CANONICAL.replacen("rates: [8, 16, 24, 32, 48]", "rates: []", 1),
            CANONICAL.replacen("promptBytes: 2048", "promptBytes: 2048\n    typo: 1", 1),
            CANONICAL.replacen("  smoke:\n    - smoke", "  smoke: [smoke, smoke]", 1),
            CANONICAL.replacen("  smoke:\n    - smoke", "  smoke: [missing]", 1),
            CANONICAL.replacen(
                "  smoke:\n    - smoke",
                "  smoke: [smoke]\n  smoke: [smoke]",
                1,
            ),
            CANONICAL.replace("smoke", "../escape"),
        ] {
            assert!(
                Suite::from_yaml(&source).is_err(),
                "accepted invalid suite: {source}"
            );
        }
    }

    #[test]
    fn checked_counts_reject_overflow_before_plan_io() -> Result<()> {
        assert_eq!(reserved_count(1, 1)?, 2);
        assert_eq!(reserved_count(20, 1)?, 21);
        assert!(reserved_count(usize::MAX, 1).is_err());
        assert!(reserved_count(usize::MAX, 2).is_err());
        let source = CANONICAL.replacen(
            "workersPerRps: 2",
            &format!("workersPerRps: {}", usize::MAX),
            1,
        );
        assert!(Suite::from_yaml(&source).is_err());
        let suite = Suite::from_yaml(CANONICAL)?;
        assert!(suite.plan("missing", &Algorithm::ALL).is_err());
        assert!(suite.plan("smoke", &[]).is_err());
        assert!(
            suite
                .plan("smoke", &[Algorithm::WaitAndWiden, Algorithm::WaitAndWiden])
                .is_err()
        );
        let source = CANONICAL.replacen("repeats: 3", &format!("repeats: {}", usize::MAX), 1);
        assert!(
            Suite::from_yaml(&source)?
                .plan("session-affinity", &Algorithm::ALL)
                .is_err()
        );
        Ok(())
    }

    #[test]
    fn resolved_streams_respect_spark_numeric_boundaries() -> Result<()> {
        let too_large = i32::MAX as usize + 1;
        for (source, suite, field) in [
            (
                CANONICAL.replacen("workers: 8", &format!("workers: {too_large}"), 1),
                "smoke",
                "workers",
            ),
            (
                CANONICAL.replacen("requests: 32", &format!("requests: {too_large}"), 1),
                "smoke",
                "requests",
            ),
            (
                CANONICAL.replacen("maxTokens: 256", &format!("maxTokens: {too_large}"), 1),
                "smoke",
                "maxTokens",
            ),
            (
                CANONICAL.replacen("rate: 8", "rate: 1000000001", 1),
                "smoke",
                "rate",
            ),
            (
                CANONICAL.replacen("workersPerRps: 2", "workersPerRps: 268435456", 1),
                "capacity",
                "workers",
            ),
            (
                CANONICAL.replacen("sessions: 512", "sessions: 178956971", 1),
                "session-affinity",
                "requests",
            ),
            (
                CANONICAL.replacen("workers: 32", "workers: 89478486", 1),
                "long-context",
                "requests",
            ),
            (
                CANONICAL.replacen("workers: 128", &format!("workers: {too_large}"), 1),
                "cache-affinity",
                "workers",
            ),
        ] {
            let error = Suite::from_yaml(&source)?
                .plan(suite, &Algorithm::ALL)
                .unwrap_err();
            assert!(format!("{error:#}").contains(field), "{error:#}");
        }
        let source = CANONICAL
            .replacen("workers: 8", &format!("workers: {}", i32::MAX), 1)
            .replacen("requests: 32", &format!("requests: {}", i32::MAX), 1)
            .replacen("maxTokens: 256", &format!("maxTokens: {}", i32::MAX), 1)
            .replacen("rate: 8", "rate: 1000000000", 1);
        Suite::from_yaml(&source)?.plan("smoke", &Algorithm::ALL)?;
        let source = CANONICAL.replacen("maxTokens: 256", "maxTokens: 0", 1);
        let plan = Suite::from_yaml(&source)?.plan("smoke", &Algorithm::ALL)?;
        assert_eq!(plan.arms[0].streams[0].limits.max_tokens, 0);
        Ok(())
    }

    #[test]
    fn measured_estimate_excludes_warmup_cooldown_and_concurrent_double_counting() -> Result<()> {
        let source = CANONICAL.replacen("cooldownSeconds: 60", "cooldownSeconds: 99999", 1);
        let suite = Suite::from_yaml(&source)?;
        let canonical = suite.plan("canonical", &Algorithm::ALL)?;
        assert!((canonical.minimum_measured_minutes() - 5192.0 / 60.0).abs() < 1e-10);
        let capacity = suite.plan("capacity", &Algorithm::ALL)?;
        assert!((capacity.minimum_measured_minutes() - 1208.0 / 60.0).abs() < 1e-10);
        let single = suite.plan("canonical", &[Algorithm::PowerOfN])?;
        assert!((single.minimum_measured_minutes() - 2596.0 / 60.0).abs() < 1e-10);
        Ok(())
    }

    #[test]
    fn scenario_names_do_not_select_behavior() -> Result<()> {
        let source = CANONICAL.replace("long-context-affinity", "screen-high-context");
        let suite = Suite::from_yaml(&source)?;
        let plan = suite.plan("long-context", &Algorithm::ALL)?;
        assert_eq!(plan.arms[2].scenario, "screen-high-context");
        assert_eq!(
            plan.arms[2].directory,
            Path::new("screen-high-context/repeat-01/wait-and-widen")
        );
        assert_eq!(plan.arms[2].streams[0].limit, RunLimit::Requests(768));
        Ok(())
    }
}
