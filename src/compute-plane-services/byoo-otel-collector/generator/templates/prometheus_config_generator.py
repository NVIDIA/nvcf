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
from typing import Any, Dict, List

import yaml

__all__ = ["PrometheusConfigGenerator"]


class PrometheusConfigGenerator:  # pylint: disable=too-few-public-methods
    """Generates Prometheus Jinja2 variables from *source-config.yaml*."""

    _DEFAULT_CONFIG = Path(__file__).resolve().parent.parent / "source-config.yaml"

    def __init__(self, config_path: str | Path | None = None) -> None:
        self._config_path = Path(config_path) if config_path else self._DEFAULT_CONFIG
        if not self._config_path.exists():
            raise FileNotFoundError(f"Configuration file not found: {self._config_path}")

        self._config: Dict[str, Any] = yaml.safe_load(self._config_path.read_text(encoding="utf-8"))
        if not isinstance(self._config, dict) or "metrics" not in self._config:
            raise ValueError("Invalid configuration: missing top-level 'metrics' key")

    # ---------------------------------------------------------------------
    # Public helpers
    # ---------------------------------------------------------------------

    def build_variables(self) -> Dict[str, str]:
        """Return a mapping of variable names → substitution strings."""
        variables: Dict[str, str] = {}
        for section in self._config.get("metrics", []):
            function_type = section.get("function_type")
            for job in section.get("jobs", []):
                job_key = job.get("name", "").replace("-", "_")
                if not job_key:
                    continue  # Skip unnamed jobs

                # 1) metric allow / keep lists --------------------------------------------------
                self._process_metric_lists(job, job_key, variables, function_type)

                # 2) attribute allow list -------------------------------------------------------
                self._process_attr_allow_list(job, job_key, variables, function_type)

                # 3) drop label patterns --------------------------------------------------------
                self._process_drop_label_patterns(job, job_key, variables, function_type)
        return variables

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    @staticmethod
    def _flatten_names(categories: List[Dict[str, Any]]) -> List[str]:
        """Return flattened list of item names across categories."""
        names: List[str] = []
        for cat in categories:
            for item in cat.get("list", []):
                # Each item may be dict with 'name' or string
                if isinstance(item, dict):
                    name = item.get("name")
                    if name:
                        names.append(str(name))
                else:
                    names.append(str(item))
        return names

    def _process_metric_lists(
        self,
        job: Dict[str, Any],
        job_key: str,
        variables: Dict[str, str],
        function_type: str,
    ) -> None:
        allow_key = f"{function_type}_{job_key}_metric_allow_list"

        if "metric_allow_list" in job:
            names = self._flatten_names(job["metric_allow_list"])
            if names:
                variables[allow_key] = "|".join(names)


    def _process_attr_allow_list(
        self,
        job: Dict[str, Any],
        job_key: str,
        variables: Dict[str, str],
        function_type: str,
    ) -> None:
        if "attr_allow_list" not in job:
            return
        names = self._flatten_names(job["attr_allow_list"])
        if names:
            variables[f"{function_type}_{job_key}_attr_allow_list"] = "|".join(names)

    def _process_drop_label_patterns(
        self,
        job: Dict[str, Any],
        job_key: str,
        variables: Dict[str, str],
        function_type: str,
    ) -> None:
        if "metrics_drop_label_patterns" not in job:
            return
        
        backend_list = {"vm": [], "k8s": []}
        for cat in job["metrics_drop_label_patterns"]:

            src_label = cat.get("catagory")
            backend = cat.get("backend")

            if not src_label:
                continue
            names = [str(item.get("name")) for item in cat.get("list", [])]
            if not names:
                continue
            joined = "|".join(names)
            pattern_lines = (
                [
                    f"- source_labels: [{src_label}]",
                    f"  regex: \"({joined})\"",
                    "  action: drop",
                ]
            )
            if backend is not None:
                trans_backend = "vm" if backend == "gfn" else "k8s"
                backend_list[trans_backend].extend(pattern_lines)
            else:
                backend_list["vm"].extend(pattern_lines)
                backend_list["k8s"].extend(pattern_lines)

        if backend_list["vm"]:
            variables[f"{function_type}_vm_{job_key}_metrics_drop_label_patterns"] = "\n".join(backend_list["vm"])
        if backend_list["k8s"]:
            variables[f"{function_type}_k8s_{job_key}_metrics_drop_label_patterns"] = "\n".join(backend_list["k8s"])
