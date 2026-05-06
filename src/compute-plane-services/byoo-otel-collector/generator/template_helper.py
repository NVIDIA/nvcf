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

from jinja2 import Environment, FileSystemLoader, Undefined, select_autoescape

from templates.processors_generator import AttributeMetricsTransformGenerator
from templates.prometheus_config_generator import PrometheusConfigGenerator

# ------------------------------
# Template generation helpers
# ------------------------------


class SilentUndefined(Undefined):
    '''
    Dont break pageloads because vars arent there!
    '''
    def _fail_with_undefined_error(self, *args, **kwargs):
        return None


class TemplateBuilder:
    def __init__(self, source_config_path: str, template_source_folder: str, output_folder: str):
        self.source_config_path = source_config_path
        self.output_folder = output_folder

        self.attribute_generator = AttributeMetricsTransformGenerator(source_config_path)
        self.metrics_generator = PrometheusConfigGenerator(source_config_path)

        self.env = Environment(
            loader=FileSystemLoader(template_source_folder),
            autoescape=select_autoescape([]),
            undefined=SilentUndefined,
            variable_start_string="[[",
            variable_end_string="]]",
            trim_blocks=True,
            lstrip_blocks=True,
        )

    def build(self) -> str:

        gfn_attributes, gfn_metricstransform = self.attribute_generator.generate_snippet("gfn")
        non_gfn_attributes, non_gfn_metricstransform = self.attribute_generator.generate_snippet("non-gfn")

        metrics_variables = self.metrics_generator.build_variables()

        for source_template in ["src-config-vm-helm.yaml.tmpl", "src-config-vm-container.yaml.tmpl", "src-config-k8s-helm.yaml.tmpl", "src-config-k8s-container.yaml.tmpl"]:
            tpl = self.env.get_template(source_template)

            rendered_yaml = tpl.render(
                gfn_attributes=gfn_attributes,
                gfn_metricstransform=gfn_metricstransform,
                non_gfn_attributes=non_gfn_attributes,
                non_gfn_metricstransform=non_gfn_metricstransform,
                helm_nvcf_worker_metric_allow_list=metrics_variables.get("helm_nvcf_worker_metric_allow_list"),
                helm_nvcf_worker_attr_allow_list=metrics_variables.get("helm_nvcf_worker_attr_allow_list"),
                helm_nvca_metric_allow_list=metrics_variables.get("helm_nvca_metric_allow_list"),
                helm_nvca_attr_allow_list=metrics_variables.get("helm_nvca_attr_allow_list"),
                helm_kubernetes_cadvisor_metric_allow_list=metrics_variables.get("helm_kubernetes_cadvisor_metric_allow_list"),
                helm_kubernetes_cadvisor_attr_allow_list=metrics_variables.get("helm_kubernetes_cadvisor_attr_allow_list"),
                helm_vm_kubernetes_cadvisor_metrics_drop_label_patterns=metrics_variables.get("helm_vm_kubernetes_cadvisor_metrics_drop_label_patterns"),
                helm_k8s_kubernetes_cadvisor_metrics_drop_label_patterns=metrics_variables.get("helm_k8s_kubernetes_cadvisor_metrics_drop_label_patterns"),
                helm_kube_state_metrics_metric_allow_list=metrics_variables.get("helm_kube_state_metrics_metric_allow_list"),
                helm_kube_state_metrics_attr_allow_list=metrics_variables.get("helm_kube_state_metrics_attr_allow_list"),
                helm_vm_kube_state_metrics_metrics_drop_label_patterns=metrics_variables.get("helm_vm_kube_state_metrics_metrics_drop_label_patterns"),
                helm_k8s_kube_state_metrics_metrics_drop_label_patterns=metrics_variables.get("helm_k8s_kube_state_metrics_metrics_drop_label_patterns"),
                helm_opentelemetry_collector_metric_allow_list=metrics_variables.get("helm_opentelemetry_collector_metric_allow_list"),
                helm_opentelemetry_collector_attr_allow_list=metrics_variables.get("helm_opentelemetry_collector_attr_allow_list"),
                helm_nvidia_dcgm_exporter_metric_allow_list=metrics_variables.get("helm_nvidia_dcgm_exporter_metric_allow_list"),
                helm_nvidia_dcgm_exporter_attr_allow_list=metrics_variables.get("helm_nvidia_dcgm_exporter_attr_allow_list"),

                container_nvcf_worker_metric_allow_list=metrics_variables.get("container_nvcf_worker_metric_allow_list"),
                container_nvcf_worker_attr_allow_list=metrics_variables.get("container_nvcf_worker_attr_allow_list"),
                container_nvca_metric_allow_list=metrics_variables.get("container_nvca_metric_allow_list"),
                container_nvca_attr_allow_list=metrics_variables.get("container_nvca_attr_allow_list"),
                container_nvidia_dcgm_exporter_metric_allow_list=metrics_variables.get("container_nvidia_dcgm_exporter_metric_allow_list"),
                container_nvidia_dcgm_exporter_attr_allow_list=metrics_variables.get("container_nvidia_dcgm_exporter_attr_allow_list"),
                container_opentelemetry_collector_metric_allow_list=metrics_variables.get("container_opentelemetry_collector_metric_allow_list"),
                container_metrics_utils_metric_allow_list=metrics_variables.get("container_metrics_utils_metric_allow_list"),
                container_kubernetes_cadvisor_metric_allow_list=metrics_variables.get("container_kubernetes_cadvisor_metric_allow_list"),
                container_kubernetes_cadvisor_attr_allow_list=metrics_variables.get("container_kubernetes_cadvisor_attr_allow_list"),
                container_kubernetes_cadvisor_metrics_drop_label_patterns=metrics_variables.get("container_kubernetes_cadvisor_metrics_drop_label_patterns"),
                container_kube_state_metrics_metric_allow_list=metrics_variables.get("container_kube_state_metrics_metric_allow_list"),
                container_kube_state_metrics_attr_allow_list=metrics_variables.get("container_kube_state_metrics_attr_allow_list"),
                container_kube_state_metrics_metrics_drop_label_patterns=metrics_variables.get("container_kube_state_metrics_metrics_drop_label_patterns"),
                container_vm_kubernetes_cadvisor_metrics_drop_label_patterns=metrics_variables.get("container_vm_kubernetes_cadvisor_metrics_drop_label_patterns"),
            )

            with open(f"{self.output_folder}/generated_{source_template}", "w", encoding="utf-8") as f:
                f.write(rendered_yaml)

            print(f"Config templates gererated at {self.output_folder}/generated_{source_template}")
