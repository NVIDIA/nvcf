# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import tempfile
import unittest
from pathlib import Path

from template_helper import TemplateBuilder
from templates.prometheus_config_generator import PrometheusConfigGenerator


GENERATOR_DIR = Path(__file__).resolve().parent
SOURCE_CONFIG = GENERATOR_DIR / "source-config.yaml"
SOURCE_TEMPLATES_DIR = GENERATOR_DIR.parent / "internal" / "otelconfig" / "source_templates"

EXPORTER_DIAGNOSTIC_METRICS = {
    "helm": (
        "otelcol_exporter_enqueue_failed_log_records_total",
        "otelcol_exporter_enqueue_failed_spans_total",
        "otelcol_exporter_enqueue_failed_metric_points_total",
        "otelcol_exporter_queue_size",
        "otelcol_exporter_queue_capacity",
        "otelcol_http_client_request_duration_seconds.*",
        "otelcol_rpc_client_call_duration_seconds.*",
    ),
    "container": (
        "otelcol_exporter_enqueue_failed_metric_points_total",
        "otelcol_exporter_enqueue_failed_spans_total",
        "otelcol_exporter_enqueue_failed_log_records_total",
        "otelcol_exporter_queue_size",
        "otelcol_exporter_queue_capacity",
        "otelcol_http_client_request_duration_seconds.*",
        "otelcol_rpc_client_call_duration_seconds.*",
    ),
}

CONFIG_TEMPLATES = {
    "helm": ("generated_src-config-vm-helm.yaml.tmpl", "generated_src-config-k8s-helm.yaml.tmpl"),
    "container": (
        "generated_src-config-vm-container.yaml.tmpl",
        "generated_src-config-k8s-container.yaml.tmpl",
    ),
}


class PrometheusConfigGeneratorTest(unittest.TestCase):
    def test_exporter_diagnostics_are_rendered_in_keep_regexes(self) -> None:
        variables = PrometheusConfigGenerator(SOURCE_CONFIG).build_variables()

        with tempfile.TemporaryDirectory() as output_dir:
            TemplateBuilder(
                str(SOURCE_CONFIG), str(SOURCE_TEMPLATES_DIR), output_dir
            ).build()

            for function_type, diagnostic_metrics in EXPORTER_DIAGNOSTIC_METRICS.items():
                with self.subTest(function_type=function_type):
                    allow_list = variables[
                        f"{function_type}_opentelemetry_collector_metric_allow_list"
                    ].split("|")
                    first_diagnostic = allow_list.index(diagnostic_metrics[0])
                    self.assertEqual(
                        list(diagnostic_metrics),
                        allow_list[first_diagnostic:first_diagnostic + len(diagnostic_metrics)],
                    )

                    expected_regex = "|".join(diagnostic_metrics)
                    for template_name in CONFIG_TEMPLATES[function_type]:
                        rendered_template = Path(output_dir, template_name).read_text(
                            encoding="utf-8"
                        )
                        self.assertIn(
                            expected_regex,
                            rendered_template,
                            f"{template_name} does not retain exporter diagnostics",
                        )


if __name__ == "__main__":
    unittest.main()
