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

from dataclasses import dataclass
from enum import Enum
from typing import Dict, List, Union


class WrapperType(str, Enum):
    FUNCTION = "function"
    TASK = "task"


class WorkloadType(str, Enum):
    CONTAINER = "container"
    HELM = "helm"


class CloudProvider(str, Enum):
    GFN = "gfn"
    NON_GFN = "non-gfn"


class MetricsJob(str, Enum):
    NVIDIA_DCGM_EXPORTER = "nvidia-dcgm-exporter"
    KUBE_STATE_METRICS = "kube-state-metrics"
    KUBERNETES_CADVISOR = "kubernetes-cadvisor"
    OPENTELEMETRY_COLLECTOR = "opentelemetry-collector"
    NVCF_WORKER = "nvcf-worker"
    BYOO_TEST = "byoo-test"
    BYOO_TASK_TEST = "byoo-task-test"
    OTHER = "other"

    @classmethod
    def from_string(cls, value: str) -> "MetricsJob":
        try:
            return cls(value)
        except ValueError:
            return cls.OTHER


class MetricsValidationResult(str, Enum):
    VALID = "[green]Valid[/]"
    VALID_WITH_WARNINGS = "[yellow]Valid with warnings[/]"
    INVALID = "[red]Invalid[/]"
    SKIPPED = "Skipped"


@dataclass
class Metric:
    metric: Dict[str, str]
    values: List[List[Union[str, float]]]


@dataclass
class MetricsJobConfig:
    name: str
    metrics_allow_list: List[str]
    attr_allow_list: List[str]


@dataclass
class WorkloadConfig:
    container: List[MetricsJobConfig]
    helm: List[MetricsJobConfig]


@dataclass
class MetadataAttributes:
    gfn: Dict[str, List[str]]
    non_gfn: Dict[str, List[str]]


@dataclass
class Config:
    metadata_attributes: MetadataAttributes
    validating_rules: WorkloadConfig
