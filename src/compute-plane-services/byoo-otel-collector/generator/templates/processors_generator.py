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

from pathlib import Path
import yaml
from enum import StrEnum


class CONSTANT(StrEnum):
    """Constants for the common variables."""
    CLOUD_PROVIDER = "cloud_provider"
    NCA_ID = "nca_id"
    INSTANCE_ID = "instance_id"
    FUNCTION_ID = "function_id"
    FUNCTION_VERSION_ID = "function_version_id"
    TASK_ID = "task_id"
    ZONE_NAME = "zone_name"
    BACKEND = "backend"
    WORKLOAD = "workload"
    CLOUD_REGION = "cloud_region"
    HOST_ID = "host.id"
    GFN_BACKEND = "gfn"
    NON_GFN_BACKEND = "non-gfn"
    WORKLOAD_FUNCTION = "function"
    WORKLOAD_TASK = "task"


class AttributeMetricsTransformGenerator:
    """Generates the `attributes/add-metadata` and `metricstransform` YAML content for different back-ends.

    This utility reads the *source-config.yaml* file once during initialization
    and subsequently produces correctly formatted YAML content that can be
    embedded in the different Jinja2 templates (k8s/vm, helm/container).

    Usage example:
        generator = AttributeMetricsTransformGenerator()
        gfn_snippet, non_gfn_snippet = generator.generate_snippet()
        print(gfn_snippet)
        print(non_gfn_snippet)
    """

    _DEFAULT_CONFIG = Path(__file__).parent / "source-config.yaml"

    def __init__(self, config_path: str | Path | None = None) -> None:
        """Initialises the generator.

        Args:
            config_path: Optional explicit path to *source-config.yaml*. If not
                provided, the generator looks for the file next to this module.
        """
        config_path = Path(config_path) if config_path else self._DEFAULT_CONFIG
        if not config_path.exists():
            raise FileNotFoundError(f"Cannot find configuration file at: {config_path}")

        self._config = yaml.safe_load(config_path.read_text(encoding="utf-8"))
        if not self._config or "global" not in self._config:
            raise ValueError("Invalid configuration format: missing 'global' section")

        # build the config_value_map
        # example:
        # {"gfn":
        #   {"nca_id":
        #   {"value": "${env:NCA_ID:-unknown}", "workload": ["function", "task"]}},
        #   {"function_id":
        #   {"value": "$${env:NVCF_FUNCTION_ID:-unknown}", "workload": ["function"]}},
        #   ...
        # }
        self._config_value_map = {}
        for entry in self._config.get("global", []):
            backend = entry.get(CONSTANT.BACKEND)
            workload = entry.get(CONSTANT.WORKLOAD)
            req_attrs = entry.get("metrics", {}).get("required_attributes", [])
            for attr in req_attrs:
                name, value = attr.get("name"), attr.get("value")
                self._config_value_map.setdefault(backend, {})
                if name not in self._config_value_map[backend]:
                    self._config_value_map[backend][name] = {
                        "value": value,
                        "workload": [workload],
                    }
                else:
                    if value == self._config_value_map[backend][name]["value"]:
                        self._config_value_map[backend][name]["workload"].append(workload)
                    else:
                        self._config_value_map[backend][name][workload] = [value]

    def generate_snippet(self, backend: str) -> tuple[str, str]:
        """Generates two YAML snippets: one for *gfn* (vm) backend and one for
        *non-gfn* (k8s) backend.

        The workload-specific differences (function vs task) are handled via a
        single Go-template conditional block.

        Returns:
            Tuple `(gfn_snippet, non_gfn_snippet)`.
        """
        return self._build_snippet(backend)

    def _build_snippet(self, backend: str) -> str:
        """Constructs YAML snippet for a single backend type."""

        # attributes/add-metadata
        attr_lines: list[str] = []

        # metricstransform
        metricstransform_lines: list[str] = []

        for attr_name, attr_info in self._config_value_map[backend].items():
            additional_condition = self._check_additional_condition(attr_name, attr_info["workload"])
            self._append_attributes_lines(attr_lines, attr_name, attr_info["value"], additional_condition)
            self._append_metricstransform_lines(metricstransform_lines, attr_name, attr_info["value"], additional_condition)

        attributes_yaml = "\n".join(attr_lines)
        metricstransform_yaml = "\n".join(metricstransform_lines)
        return attributes_yaml, metricstransform_yaml

    def _append_attributes_lines(
        self,
        lines: list[str],
        attr_name: str,
        value_template: str,
        additional_condition: str | None,
    ) -> None:
        """Appends action block lines for a given attribute name."""
        indent = 4 * " "  # four spaces
        if additional_condition:
            lines.append(f"{indent}{{{{- if {additional_condition} }}}}")
        lines.extend([
            f"{indent}- action: insert",
            f"{indent}  key: {attr_name}",
            f"{indent}  value: \"{value_template}\"",
        ])
        if additional_condition:
            lines.append(f"{indent}{{{{ end }}}}")

    def _append_metricstransform_lines(
        self,
        lines: list[str],
        attr_name: str,
        value_template: str,
        additional_condition: str | None,
    ) -> None:
        """Appends metricstransform operation lines."""
        indent = 8 * " "  # eight spaces
        if additional_condition:
            lines.append(f"{indent}{{{{- if {additional_condition} }}}}")
        lines.extend([
            f"{indent}- action: add_label",
            f"{indent}  new_label: {attr_name}",
            f"{indent}  new_value: \"{value_template}\"",
        ])
        if additional_condition:
            lines.append(f"{indent}{{{{ end }}}}")

    def _check_additional_condition(self, attr_name: str, workloads: list[str]) -> str | None:
        """Check workload and attribute name, return the Go-template guard.
        Return None if no guard is needed.
        """
        if attr_name in (CONSTANT.CLOUD_REGION, CONSTANT.ZONE_NAME):
            return ".ZoneName"

        wset = set(workloads)
        if wset == {CONSTANT.WORKLOAD_FUNCTION}:
            return ".FunctionID"
        if wset == {CONSTANT.WORKLOAD_TASK}:
            return ".TaskID"
        # return None if workloads contains both function and task
        return None
