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

use std::fs;
use std::path::{Path, PathBuf};

use anyhow::Context;

use crate::config::{AlgorithmConfig, BenchmarkConfig};
use crate::runtime::slugify;

use super::images::{ImageRefs, resolve_image_refs};
use super::kubectl::resolve_nodeport_host;
use super::render::{RenderManifestConfig, render_manifest};

#[derive(Clone)]
pub struct BenchmarkK8sRun {
    pub algorithm_name: String,
    pub manifest_path: PathBuf,
    pub run_dir: PathBuf,
    pub stargate_ns: String,
    pub backends_ns: String,
    pub stargate_count: usize,
    pub nodeport_host: String,
    pub stargate_http_endpoint: String,
    pub stargate_metrics_endpoint: String,
    pub collector_metrics_endpoint: String,
    pub backend_upstream_indices: Vec<usize>,
    pub upstream_backend_indices: Vec<usize>,
}

pub fn prepare_benchmark_k8s_run(
    config: &BenchmarkConfig,
    algorithm: &AlgorithmConfig,
    manifest_path: &Path,
    output_dir: &Path,
    run_index: usize,
) -> anyhow::Result<BenchmarkK8sRun> {
    let service_host = resolve_nodeport_host()?;
    prepare_benchmark_k8s_run_with_nodeport_host(
        config,
        algorithm,
        manifest_path,
        output_dir,
        run_index,
        service_host,
    )
}

pub(super) fn prepare_benchmark_k8s_run_with_nodeport_host(
    config: &BenchmarkConfig,
    algorithm: &AlgorithmConfig,
    manifest_path: &Path,
    output_dir: &Path,
    run_index: usize,
    service_host: String,
) -> anyhow::Result<BenchmarkK8sRun> {
    let image_refs = resolve_image_refs()?;
    prepare_benchmark_k8s_run_with_resolved_dependencies(
        config,
        algorithm,
        manifest_path,
        output_dir,
        run_index,
        service_host,
        image_refs,
    )
}

pub(super) fn prepare_benchmark_k8s_run_with_resolved_dependencies(
    config: &BenchmarkConfig,
    algorithm: &AlgorithmConfig,
    manifest_path: &Path,
    output_dir: &Path,
    run_index: usize,
    service_host: String,
    image_refs: ImageRefs,
) -> anyhow::Result<BenchmarkK8sRun> {
    let run_slug = slugify(&algorithm.name);
    let run_dir = output_dir.join(format!("run-{run_slug}"));
    fs::create_dir_all(&run_dir)
        .with_context(|| format!("failed to create run dir {}", run_dir.display()))?;

    let stargate_ns = format!("sgbench-sg-{run_slug}");
    let backends_ns = format!("sgbench-be-{run_slug}");
    let http_node_port = 30080 + run_index as u16;
    let metrics_node_port = 31080 + run_index as u16;
    let collector_metrics_node_port = 32080 + run_index as u16;

    config.validate()?;
    let backend_upstream_indices = (0..config.backends.count)
        .map(|index| config.backends.upstream_index_for_index(index))
        .collect::<Vec<_>>();
    let upstream_backend_indices = config.backends.upstream_indices();
    let lb_config_json = serde_json::to_string_pretty(&algorithm.config)
        .with_context(|| format!("failed to serialize LB config for {}", algorithm.name))?;
    let manifests = render_manifest(RenderManifestConfig {
        config,
        algorithm,
        image_refs: &image_refs,
        stargate_ns: &stargate_ns,
        backends_ns: &backends_ns,
        lb_config_json: &lb_config_json,
        http_node_port,
        metrics_node_port,
        collector_metrics_node_port,
    });
    let manifest_out = run_dir.join("k8s-manifest.yaml");
    let stargate_manifest_out = run_dir.join("k8s-stargate-manifest.yaml");
    let backends_manifest_out = run_dir.join("k8s-backends-manifest.yaml");
    fs::write(
        &manifest_out,
        format!("{}{}", manifests.stargate, manifests.backends),
    )
    .with_context(|| format!("failed to write {}", manifest_out.display()))?;
    fs::write(&stargate_manifest_out, manifests.stargate)
        .with_context(|| format!("failed to write {}", stargate_manifest_out.display()))?;
    fs::write(&backends_manifest_out, manifests.backends)
        .with_context(|| format!("failed to write {}", backends_manifest_out.display()))?;

    let run_info = serde_json::json!({
        "algorithm_name": algorithm.name,
        "stargate_namespace": stargate_ns,
        "backends_namespace": backends_ns,
        "k8s_manifest_path": manifest_out,
        "stargate_k8s_manifest_path": stargate_manifest_out,
        "backends_k8s_manifest_path": backends_manifest_out,
        "http_node_port": http_node_port,
        "metrics_node_port": metrics_node_port,
        "collector_metrics_node_port": collector_metrics_node_port,
        "stargate_http_endpoint": format!("http://{service_host}:{http_node_port}"),
        "stargate_metrics_endpoint": format!("http://{service_host}:{metrics_node_port}/metrics"),
        "collector_metrics_endpoint": format!("http://{service_host}:{collector_metrics_node_port}/metrics"),
        "pylon_queue_admission": algorithm.pylon_queue_admission,
        "manifest_path": manifest_path,
    });
    fs::write(
        run_dir.join("run-info.json"),
        serde_json::to_vec_pretty(&run_info).context("failed to serialize run info")?,
    )
    .with_context(|| {
        format!(
            "failed to write {}",
            run_dir.join("run-info.json").display()
        )
    })?;

    Ok(BenchmarkK8sRun {
        algorithm_name: algorithm.name.clone(),
        manifest_path: manifest_path.to_path_buf(),
        run_dir,
        stargate_ns,
        backends_ns,
        stargate_count: config.stargates.count,
        nodeport_host: service_host.clone(),
        stargate_http_endpoint: format!("http://{service_host}:{http_node_port}"),
        stargate_metrics_endpoint: format!("http://{service_host}:{metrics_node_port}/metrics"),
        collector_metrics_endpoint: format!(
            "http://{service_host}:{collector_metrics_node_port}/metrics"
        ),
        backend_upstream_indices,
        upstream_backend_indices,
    })
}
