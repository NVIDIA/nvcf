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

use std::path::PathBuf;

use anyhow::Context;

use crate::metadata::{
    BenchmarkTier, DriverMode, ReliabilityMode, RunMetadata, collect_run_metadata,
    write_run_metadata,
};

use super::{
    TransportBenchConfig, render_transport_benchmark_report, run_transport_benchmark,
    write_transport_benchmark_artifacts,
};

pub fn run_transport_benchmark_command(
    config: TransportBenchConfig,
    reliability_mode: ReliabilityMode,
    output_dir: Option<PathBuf>,
) -> anyhow::Result<()> {
    run_transport_benchmark_with_metadata(
        config,
        collect_run_metadata(
            BenchmarkTier::TransportLoopback,
            reliability_mode,
            DriverMode::LocalProcess,
        ),
        output_dir,
    )
}

fn run_transport_benchmark_with_metadata(
    config: TransportBenchConfig,
    metadata: RunMetadata,
    output_dir: Option<PathBuf>,
) -> anyhow::Result<()> {
    if let Some(output_dir) = &output_dir {
        std::fs::create_dir_all(output_dir)
            .with_context(|| format!("failed to create {}", output_dir.display()))?;
        write_run_metadata(&output_dir.join("run-metadata.json"), &metadata)?;
    }
    if metadata.preflight.should_fail {
        anyhow::bail!(
            "strict reliability preflight failed with {} failure(s); inspect run-metadata.json when --output-dir is set",
            metadata.preflight.failure_count
        );
    }
    let outcome = tokio::runtime::Runtime::new()
        .context("failed to create tokio runtime")?
        .block_on(run_transport_benchmark(config))?;
    println!("{}", render_transport_benchmark_report(&outcome));
    if let Some(output_dir) = &output_dir {
        write_transport_benchmark_artifacts(output_dir, &outcome)?;
        println!(
            "wrote transport benchmark artifacts to {}",
            output_dir.display()
        );
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use crate::metadata::{
        BenchmarkTier, DriverMode, GitMetadata, HostMetadata, KubernetesMetadata, PreflightCheck,
        PreflightLevel, PreflightReport, ReliabilityMode, RunMetadata, RustMetadata,
    };
    use crate::transport::TransportBenchConfig;

    fn tiny_transport_config() -> TransportBenchConfig {
        TransportBenchConfig {
            request_count: 1,
            concurrency: 1,
            quic_connections: 1,
            warmup_requests: 0,
            request_body_bytes: 1,
            response_body_bytes: 1,
            request_chunk_bytes: 1,
            response_chunk_bytes: 1,
            quic_send_fairness: true,
            http3_send_grease: true,
            trials: 1,
            warmup_trials: 0,
            cooldown_ms: 0,
            randomize_order: false,
            noise_threshold_cv: 0.02,
            min_effect_size_percent: 1.0,
        }
    }

    fn strict_failing_metadata() -> RunMetadata {
        RunMetadata {
            schema_version: 3,
            benchmark_tier: BenchmarkTier::TransportLoopback,
            reliability_mode: ReliabilityMode::Strict,
            driver_mode: DriverMode::LocalProcess,
            command_line: vec!["stargate-bench".to_string(), "transport-bench".to_string()],
            started_at_unix_seconds: 0,
            current_exe: None,
            working_dir: None,
            git: GitMetadata::default(),
            rust: RustMetadata::default(),
            host: HostMetadata::default(),
            kubernetes: KubernetesMetadata::default(),
            preflight: PreflightReport {
                checks: vec![PreflightCheck {
                    name: "release-binary".to_string(),
                    level: PreflightLevel::Failure,
                    message: "debug build".to_string(),
                }],
                warning_count: 0,
                failure_count: 1,
                should_fail: true,
            },
            known_limitations: Vec::new(),
        }
    }

    #[test]
    fn transport_command_writes_metadata_before_strict_preflight_failure() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let error = super::run_transport_benchmark_with_metadata(
            tiny_transport_config(),
            strict_failing_metadata(),
            Some(PathBuf::from(tempdir.path())),
        )
        .expect_err("strict preflight should fail before benchmark execution");

        let metadata_path = tempdir.path().join("run-metadata.json");
        assert!(metadata_path.exists());
        let metadata = serde_json::from_slice::<RunMetadata>(
            &std::fs::read(&metadata_path).expect("metadata should read"),
        )
        .expect("metadata should parse");
        assert_eq!(metadata.preflight.failure_count, 1);
        assert!(
            error
                .to_string()
                .contains("strict reliability preflight failed with 1 failure(s)")
        );
    }
}
