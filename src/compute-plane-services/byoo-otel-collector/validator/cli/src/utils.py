# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import json
import logging
import os
import re
from difflib import unified_diff
from pathlib import Path
from typing import Dict, List, Union

import requests
import yaml
from rich.console import Console
from rich.logging import RichHandler

from .models import *

console = Console(width=120, force_terminal=True)
logging.basicConfig(
    level=logging.INFO,
    format="%(message)s",
    handlers=[
        RichHandler(
            enable_link_path=False,
            markup=True,
            rich_tracebacks=True,
            show_path=False,
            show_time=False,
            console=console,
        )
    ],
)

logger = logging.getLogger(__name__)


class PrometheusClient:
    def __init__(self):
        self.base_url = os.getenv("GRAFANA_CLOUD_PROMETHEUS_URL")
        self.username = os.getenv("GRAFANA_CLOUD_PROMETHEUS_USERNAME")
        self.password = os.getenv("GRAFANA_CLOUD_PROMETHEUS_PASSWORD")

        if not all([self.base_url, self.username, self.password]):
            raise ValueError("Missing required environment variables for Prometheus client")

    def query_metrics(
        self,
        wrapper_type: WrapperType,
        id: str,
        start: str,
        end: str,
        extra_filters: str = "",
    ) -> Dict:
        query_url = f"https://{self.base_url}/api/v1/query_range"
        query_string = f'{{ {wrapper_type.value}_id="{id}", {extra_filters} }}'

        logger.debug(f"Prometheus query string: `{query_string}`")

        params = {"query": query_string, "start": start, "end": end, "step": "30s"}

        response = requests.post(query_url, data=params, auth=(self.username, self.password))

        if response.status_code >= 400:
            logger.error(f"Error status code: {response.status_code}. Error message: {response.text}")
            raise Exception(f"error status code: {response.status_code}")

        return response.json()


def print_diff_colorized(diff: str):
    """
    Print the diff with colorized output.
    """
    red = lambda text: f"\033[38;2;255;0;0m{text}\033[38;2;255;255;255m"
    green = lambda text: f"\033[38;2;0;255;0m{text}\033[38;2;255;255;255m"
    blue = lambda text: f"\033[38;2;0;0;255m{text}\033[38;2;255;255;255m"
    white = lambda text: f"\033[38;2;255;255;255m{text}\033[38;2;255;255;255m"
    logger.info("Printing diff with colorized output:")
    if not diff:
        logger.info("\tN/A")
        return
    for line in diff:
        if line.startswith("@@"):
            print(blue(line))
        elif line.startswith("- "):
            print(red(line))
        elif line.startswith("+ "):
            print(green(line))
        elif line.startswith(" "):
            print(white(line))
        else:
            print(line)


class MetricsValidator:
    def __init__(self, config_path: Union[str, Path]):
        self.config = self._load_config(config_path)
        self.prometheus = PrometheusClient()

    def _load_config(self, config_path: Union[str, Path]) -> Config:
        try:
            with open(config_path, "r") as file:
                config_data = yaml.safe_load(file)
                logger.debug(f"Loaded config data: {config_data}")

                return Config(**config_data)
        except Exception as e:
            logger.error(f"Failed to load config file {config_path}: {str(e)}")
            raise

    def _get_metadata_attributes(self, cloud_provider: CloudProvider, wrapper_type: WrapperType) -> List[str]:
        metadata_attributes = self.config.metadata_attributes

        logger.debug(f"Metadata attributes: {metadata_attributes}")

        attrs = metadata_attributes[cloud_provider.value]

        return attrs[wrapper_type.value]

    def _get_allow_list(self, workload_type: WorkloadType, metrics_job: MetricsJob) -> MetricsJobConfig:
        validating_rules = WorkloadConfig(**self.config.validating_rules)

        logger.debug(f"Allow list: {validating_rules}")
        logger.debug(f"Workload type: {workload_type.value}")
        logger.debug(f"Metrics job: {metrics_job.value}")
        workload_configs = getattr(validating_rules, workload_type.value)

        # Find the matching config by name
        for config in workload_configs:
            if MetricsJobConfig(**config).name == metrics_job.value:
                return config

        raise ValueError(f"No configuration found for job {metrics_job.value}")

    def _validate_metrics(
        self,
        metrics: List[Metric],
        validating_rules: MetricsJobConfig,
        metadata_attributes: List[str],
    ) -> MetricsValidationResult:
        err = False
        warn = False
        validating_rules.attr_allow_list.extend(metadata_attributes)

        # If no metrics are found for this job, return an error
        if len(metrics) == 0:
            err = True
            logger.error("No metrics found for this job.")
        elif len(metrics) == 1:
            err = True
            logger.error("Only one metric (up) found for this job.")
        else:
            logger.info(f"Found {len(metrics)} metrics for this job.")

        # Check if the "up" metric is 0
        up_metrics = [m for m in metrics if m.metric.get("__name__") == "up"]
        if up_metrics and up_metrics[0].values[-1][1] == "0":
            err = True
            logger.error("Value for metric 'up' is 0, indicating the target is down")

        # Check if the metrics are in the allow list
        actual_metrics = {metric.metric["__name__"] for metric in metrics if "__name__" in metric.metric}
        invalid_metrics = actual_metrics - set(validating_rules.metrics_allow_list)

        if invalid_metrics:
            err = True
            logger.error(f"Metrics not in allow list: {invalid_metrics}")

        # Check if the allow list metrics are not present
        missing_metrics = set(validating_rules.metrics_allow_list) - actual_metrics
        if missing_metrics:
            warn = True
            logger.warning(f"Metrics not found: {missing_metrics}")

        # Check attributes
        for metric_entry in metrics:

            metric_name = metric_entry.metric["__name__"]

            # Check if the attributes are in the allow list
            invalid_attrs = set(metric_entry.metric.keys()) - set(validating_rules.attr_allow_list) - {"__name__"}
            if invalid_attrs:
                err = True
                logger.error(f"Attributes not in allow list: {invalid_attrs}, metric: {metric_name}")

            # Check if the metadata attributes are missing
            missing_metadata = set(metadata_attributes) - set(metric_entry.metric.keys())
            if missing_metadata:
                err = True
                logger.error(f"Missing necessary metadata attributes: {missing_metadata}, metric: {metric_name}")

        if err:
            return MetricsValidationResult.INVALID
        elif warn:
            return MetricsValidationResult.VALID_WITH_WARNINGS
        else:
            return MetricsValidationResult.VALID

    def _diff(self, golden: Dict, actual: Dict) -> bool:
        """
        Compare two objects and return a string with the differences.
        """

        if not isinstance(golden, dict) or not isinstance(actual, dict):
            raise ValueError("Both inputs must be dict")

        # Empty the values of all metrics in the actual data to avoid false positives in the diff
        for metric in actual.get("result", []):
            metric["values"] = []

        # Replace the values of following keys with "<SKIP_VALIDATION>" to avoid false positives in the diff
        # These keys may contain values that change frequently per deployment or environment
        ignore_value_of_keys = [
            "cloud_provider",
            "cloud_region",
            "DCGM_FI_DRIVER_VERSION",
            "device",
            "exporter",
            "function_id",
            "function_version_id",
            "host_id",
            "image",
            "instance_id",
            "modelName",
            "nca_id",
            "pci_bus_id",
            "task_id",
            "transport",
            "zone_name",
        ]

        # Ignore metrics that match these patterns to avoid false positives in the diff
        # These metrics may change frequently per deployment or environment
        ignore_metrics = [
            r"http_.*",
            r"kube_pod_container_status_waiting_reason",
            r"kube_pod_container_status_terminated_reason",
        ]

        # Ignore metrics that have certain labels with specific values
        # to avoid false positives in the diff
        # These metrics may change frequently per deployment or environment
        ignore_metrics_if_label = [
            (r"DCGM_FI_DEV_GPU_UTIL", "container", None),
        ]

        # Redact the values of the following keys if they match the pattern to avoid false positives in the diff
        # These keys are randomly generated and may change per deployment or environment
        redact_keys = [
            # non-gfn function container
            ("pod", r"0-sr-.*", "0-sr-<SKIP_VALIDATION>"),
            # non-gfn function helm
            ("pod", r"byoo-test-.*", "byoo-test-<SKIP_VALIDATION>"),
            ("created_by_name", r"byoo-test-.*", "byoo-test-<SKIP_VALIDATION>"),
            ("replicaset", r"byoo-test-.*", "byoo-test-<SKIP_VALIDATION>"),
            # non-gfn task helm
            ("pod", r"task-helmchart-byoo-.*", "task-helmchart-byoo-<SKIP_VALIDATION>"),
        ]

        for i in range(len(actual["result"]) - 1, -1, -1):
            # Iterate backwards to avoid index issues when deleting items
            # delete the item if the metric name match ignore_metrics
            if any(re.match(pattern, actual["result"][i]["metric"].get("__name__", "")) for pattern in ignore_metrics):
                del actual["result"][i]
            for pattern, label, value in ignore_metrics_if_label:
                if re.match(pattern, actual["result"][i]["metric"].get("__name__", "")):
                    if actual["result"][i]["metric"].get(label) == value:
                        del actual["result"][i]
                        break
            for key in ignore_value_of_keys:
                if key in actual["result"][i]["metric"]:
                    actual["result"][i]["metric"][key] = "<SKIP_VALIDATION>"
            for key, pattern, replacement in redact_keys:
                if key in actual["result"][i]["metric"]:
                    if re.match(pattern, actual["result"][i]["metric"][key]):
                        actual["result"][i]["metric"][key] = replacement

        logger.info(f"Number of golden metrics: {len(golden['result'])}")
        logger.info(f"Number of actual metrics: {len(actual['result'])}")

        golden_str = json.dumps(golden, indent=2, sort_keys=True)
        actual_str = json.dumps(actual, indent=2, sort_keys=True)

        diff = list(unified_diff(golden_str.splitlines(), actual_str.splitlines(), n=15, lineterm=""))
        print_diff_colorized(diff)

        if diff:
            return True
        return False

    def validate(
        self,
        wrapper_type: WrapperType,
        workload_type: WorkloadType,
        cloud_provider: CloudProvider,
        id: str,
        start: str,
        end: str,
        extra_filters: str = "",
        golden: bool = False,
    ) -> Dict[str, MetricsValidationResult]:
        result = self.prometheus.query_metrics(wrapper_type, id, start, end, extra_filters)

        if not result.get("data", {}).get("result"):
            logger.error(f"No metrics found for the {wrapper_type.value} id '{id}' from {start} to {end}.")
            return {"metrics_validation_result": MetricsValidationResult.INVALID}

        if golden:
            logger.info("Validating against golden metrics...")
            with open(f"../golden/metrics_{cloud_provider.value}_{wrapper_type.value}_{workload_type.value}.json") as f:
                golden_metrics = json.load(f)
            result_metrics = result.get("data", {})
            diff = self._diff(golden_metrics, result_metrics)
            if diff:
                logger.error(f"Differences found between the metrics and golden metric.")
                return {"golden_metrics_compare_result": MetricsValidationResult.INVALID}
            else:
                return {"golden_metrics_compare_result": MetricsValidationResult.VALID}

        # Group metrics by job
        metrics_by_job: Dict[str, List[Metric]] = {}
        for entry in result["data"]["result"]:
            job = entry["metric"].get("job")
            if not job:
                continue
            metrics_by_job.setdefault(job, []).append(Metric(entry["metric"], entry["values"]))

        # Validate metrics for all jobs
        metadata_attributes = self._get_metadata_attributes(cloud_provider, wrapper_type)

        logger.info(f"Found {len(metrics_by_job)} jobs in the metrics data: {list(metrics_by_job.keys())}")

        # Platform metrics jobs
        required_jobs = [
            MetricsJob.KUBE_STATE_METRICS,
            MetricsJob.KUBERNETES_CADVISOR,
            MetricsJob.NVIDIA_DCGM_EXPORTER,
            MetricsJob.OPENTELEMETRY_COLLECTOR,
            MetricsJob.NVCF_WORKER,
        ]
        # Custom metrics jobs
        required_jobs += [MetricsJob.BYOO_TEST] if wrapper_type == WrapperType.FUNCTION else [MetricsJob.BYOO_TASK_TEST]

        # Check do required jobs exist in the metrics data
        missing_jobs = set(required_jobs) - set(metrics_by_job.keys())
        extra_jobs = set(metrics_by_job.keys()) - set(required_jobs)

        if missing_jobs:
            logger.error(f"Missing required jobs: {missing_jobs}")
        else:
            logger.info("All required jobs are present")

        if extra_jobs:
            logger.warning(f"Found unexpected jobs: {extra_jobs}\n\n")
        else:
            logger.info("No unexpected jobs found\n\n")

        validating_results = {}
        for job in metrics_by_job.keys():
            logger.info(f"=== Start validating metrics for job '{job}' ===")
            metrics_component = MetricsJob.from_string(job)

            if metrics_component == MetricsJob.OTHER:
                validating_results[job] = MetricsValidationResult.SKIPPED
                logger.warning(f"Skip the validating for job {job}")
            else:
                validating_rules = self._get_allow_list(workload_type, metrics_component)

                validating_results[job] = self._validate_metrics(
                    metrics_by_job.get(job, []),
                    MetricsJobConfig(**validating_rules),
                    metadata_attributes,
                )

            logger.info("=== Finished metrics validation for job ===\n\n")

        return validating_results
