# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
import logging
import os
import sys
import importlib
from opentelemetry import metrics,trace
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import (
    PeriodicExportingMetricReader,
)
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.sdk.trace import TracerProvider

task_results_counter = None
task_progress_gauge = None


def otel_metric_exporter_configured():
    value = os.environ.get("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
    return True if value else False


def otel_log_exporter_configured():
    value = os.environ.get("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
    return True if value else False

def otel_trace_exporter_configured():
    value = os.environ.get("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
    return True if value else False

def is_valid_protocol(protocol):
    if not protocol:
        return False
    return protocol in ["http", "grpc"]


def get_otlp_exporter(protocol, exporter_type, exporter_class):
    if not is_valid_protocol(protocol):
        raise ValueError(f"Invalid OTLP protocol '{protocol}'")

    module_name = f"opentelemetry.exporter.otlp.proto.{protocol}.{exporter_type}"
    module = importlib.import_module(module_name)
    exporter = getattr(module, exporter_class)

    return exporter()


def get_otlp_log_exporter():
    """
    :return: One of the following otlp log exporter
             opentelemetry.exporter.otlp.proto.http._log_exporter.OTLPLogExporter
             opentelemetry.exporter.otlp.proto.grpc._log_exporter.OTLPLogExporter
    """
    protocol = os.getenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "grpc")
    return get_otlp_exporter(protocol, "_log_exporter", "OTLPLogExporter")


def get_otlp_metrics_exporter():
    """
    :return: One of the following otlp metrics exporter
             opentelemetry.exporter.otlp.proto.http.metric_exporter.OTLPMetricExporter
             opentelemetry.exporter.otlp.proto.grpc.metric_exporter.OTLPMetricExporter
    """
    protocol = os.getenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "grpc")
    return get_otlp_exporter(protocol, "metric_exporter", "OTLPMetricExporter")


def get_otlp_trace_exporter():
    """
    :return: One of the following otlp trace exporter
             opentelemetry.exporter.otlp.proto.http.trace_exporter.OTLPSpanExporter
             opentelemetry.exporter.otlp.proto.grpc.trace_exporter.OTLPSpanExporter
    """
    protocol = os.getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "grpc").lower()
    return get_otlp_exporter(protocol, "trace_exporter", "OTLPSpanExporter")

resource = Resource.create({"service.name": "byoo-task-test"})

# Set up OpenTelemetry traces
otlp_trace_exporter = None
tracer = None

# Set up OpenTelemetry traces
if otel_trace_exporter_configured():
    otlp_trace_exporter = get_otlp_trace_exporter()


if otlp_trace_exporter is not None:
    tracer_provider = TracerProvider(resource=resource)
    tracer_provider.add_span_processor(BatchSpanProcessor(otlp_trace_exporter))
    trace.set_tracer_provider(tracer_provider)
    tracer = trace.get_tracer(__name__)

# Set up OpenTelemetry metrics
otlp_metric_exporter = None
meter = None
if otel_metric_exporter_configured():
    otlp_metric_exporter = get_otlp_metrics_exporter()


if otlp_metric_exporter is not None:
    metric_reader = PeriodicExportingMetricReader(otlp_metric_exporter)
    metrics.set_meter_provider(
        MeterProvider(resource=resource, metric_readers=[metric_reader])
    )
    meter = metrics.get_meter(__name__)

# Set up OpenTelemetry logs
otlp_log_exporter = None
logging_handler = None
if otel_log_exporter_configured():
    otlp_log_exporter = get_otlp_log_exporter()


if otlp_log_exporter is not None:
    logger_provider = LoggerProvider(resource=resource)
    logger_provider.add_log_record_processor(BatchLogRecordProcessor(otlp_log_exporter))
    logging_handler = LoggingHandler(
        level=logging.NOTSET, logger_provider=logger_provider
    )
else:
    logging_handler = logging.StreamHandler(sys.stdout)
