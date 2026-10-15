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

SELF_SCRAPE_METRIC_MATCHER = "otelcol_.*"
CADVISOR_POD_SELECTOR = 'container=~"inference|task|POD|"'

CONFIG_TEMPLATES = {
    "helm": ("generated_src-config-vm-helm.yaml.tmpl", "generated_src-config-k8s-helm.yaml.tmpl"),
    "container": (
        "generated_src-config-vm-container.yaml.tmpl",
        "generated_src-config-k8s-container.yaml.tmpl",
    ),
}


class PrometheusConfigGeneratorTest(unittest.TestCase):
    def test_self_scrape_metric_family_is_rendered_in_keep_regexes(self) -> None:
        variables = PrometheusConfigGenerator(SOURCE_CONFIG).build_variables()

        with tempfile.TemporaryDirectory() as output_dir:
            TemplateBuilder(
                str(SOURCE_CONFIG), str(SOURCE_TEMPLATES_DIR), output_dir
            ).build()

            for function_type, template_names in CONFIG_TEMPLATES.items():
                with self.subTest(function_type=function_type):
                    allow_list = variables[
                        f"{function_type}_opentelemetry_collector_metric_allow_list"
                    ].split("|")
                    self.assertEqual([SELF_SCRAPE_METRIC_MATCHER], allow_list)

                    for template_name in template_names:
                        rendered_template = Path(output_dir, template_name).read_text(
                            encoding="utf-8"
                        )
                        self.assertIn(
                            f'regex: "({SELF_SCRAPE_METRIC_MATCHER})"',
                            rendered_template,
                        )

    def test_k8s_container_cadvisor_keeps_pod_sandbox_metrics(self) -> None:
        with tempfile.TemporaryDirectory() as output_dir:
            TemplateBuilder(
                str(SOURCE_CONFIG), str(SOURCE_TEMPLATES_DIR), output_dir
            ).build()

            rendered_template = Path(
                output_dir, "generated_src-config-k8s-container.yaml.tmpl"
            ).read_text(encoding="utf-8")
            cadvisor_start = rendered_template.index(
                '        - job_name: "kubernetes-cadvisor"'
            )
            cadvisor_end = rendered_template.index(
                '        - job_name: "kube-state-metrics"', cadvisor_start
            )
            cadvisor_config = rendered_template[cadvisor_start:cadvisor_end]
            self.assertIn(CADVISOR_POD_SELECTOR, cadvisor_config)


if __name__ == "__main__":
    unittest.main()
