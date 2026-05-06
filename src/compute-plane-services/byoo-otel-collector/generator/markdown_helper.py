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

from typing import Any, Dict, List

# ------------------------------
# Markdown generation helpers
# ------------------------------


class GlobalTableGenerator:
    """Generates markdown table for the *global* section."""

    _HEADERS: List[str] = ["Backend", "Workload", "Required Attributes"]

    def generate(self, items: List[Dict[str, Any]]) -> str:
        """Returns a markdown table for the given global items."""
        lines: List[str] = [
            f"| {' | '.join(self._HEADERS)} |",
            f"| {' | '.join(['---'] * len(self._HEADERS))} |",
        ]
        for item in items:
            backend = str(item.get("backend", ""))
            workload = str(item.get("workload", ""))
            attributes = item.get("metrics", {}).get("required_attributes", [])
            attr_name = [attr.get("name", "") for attr in attributes]
            attr_str = ", ".join(attr_name)
            lines.append(f"| {backend} | {workload} | {attr_str} |")
        return "\n".join(lines) + "\n"


class _MetricFormatter:
    """Utility class for rendering metric bullets."""

    @staticmethod
    def format(metric: Dict[str, Any]) -> str:
        """Formats a single metric as a markdown bullet."""
        name = metric.get("name", "")
        comment = metric.get("comment")
        return f"* {name} ({comment})" if comment else f"* {name}"


class MetricsSectionGenerator:
    """Generates detailed markdown for the *metrics* section."""

    def __init__(self, metric_formatter: _MetricFormatter | None = None):
        self._formatter = metric_formatter or _MetricFormatter()

    def generate(self, items: List[Dict[str, Any]]) -> str:
        """Builds metrics section from YAML data."""
        sections: List[str] = []
        for item in items:
            function_type = item.get("function_type", "unknown")
            sections.append(f"## {function_type}")
            for job in item.get("jobs", []):
                job_name = job.get("name", "")
                sections.append(f"### {job_name}")

                metric_lists = (
                    job.get("metric_allow_list")
                    or []
                )
                for category in metric_lists:
                    # Insert heading level 3 for non-generic catagory values
                    cat_value = category.get("catagory")  # YAML uses 'catagory'
                    if cat_value and str(cat_value).lower() != "generic":
                        sections.append(f"##### {cat_value}")

                    docstring = category.get("docstring")
                    if docstring:
                        sections.append(docstring)
                    for metric in category.get("list", []):
                        # Support list item being either dict or plain string
                        if isinstance(metric, dict):
                            sections.append(self._formatter.format(metric))
                        else:
                            sections.append(f"* {metric}")
                    sections.append("")  # Blank line between categories
            sections.append("")  # Blank line between jobs
        return "\n".join(sections)


class MarkdownBuilder:
    """Composes the complete markdown document."""

    def __init__(
        self,
        global_generator: GlobalTableGenerator | None = None,
        metrics_generator: MetricsSectionGenerator | None = None,
    ):
        # Initialize generators, use default if not provided
        self._global_generator = global_generator or GlobalTableGenerator()
        self._metrics_generator = metrics_generator or MetricsSectionGenerator()

    def build(self, data: Dict[str, Any]) -> str:
        """Creates markdown documentation from parsed YAML data."""
        parts: List[str] = ["# Platform Metrics\n"]

        # Global section
        global_items = data.get("global", [])
        if global_items:
            parts.append("## Telemetry Attributes\n")
            parts.append("All traces, logs and metrics have the following attributes added to their metadata:")
            parts.append(self._global_generator.generate(global_items))

        # Metrics section
        metrics_items = data.get("metrics", [])
        if metrics_items:
            parts.append("## Metrics Details\n")
            parts.append(self._metrics_generator.generate(metrics_items))

        return "\n".join(parts)
