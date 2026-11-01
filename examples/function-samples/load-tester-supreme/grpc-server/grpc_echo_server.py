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

import os
import time
from concurrent import futures
import logging
import threading
import grpc
from grpc_health.v1 import health
from grpc_health.v1 import health_pb2
from grpc_health.v1 import health_pb2_grpc
from grpc_reflection.v1alpha import reflection
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.instrumentation.grpc import GrpcInstrumentorClient

import echo_pb2
import echo_pb2_grpc


def _toggle_health(health_servicer: health.HealthServicer, service: str):
    next_status = health_pb2.HealthCheckResponse.SERVING
    while True:
        if next_status == health_pb2.HealthCheckResponse.SERVING:
            next_status = health_pb2.HealthCheckResponse.NOT_SERVING
        else:
            next_status = health_pb2.HealthCheckResponse.SERVING

        health_servicer.set(service, next_status)
        time.sleep(5)


def _configure_health_server(server: grpc.Server):
    health_servicer = health.HealthServicer(
            experimental_non_blocking=True,
            experimental_thread_pool=futures.ThreadPoolExecutor(max_workers=10),
    )
    health_pb2_grpc.add_HealthServicer_to_server(health_servicer, server)

    # Use a daemon thread to toggle health status
    toggle_health_status_thread = threading.Thread(
            target=_toggle_health,
            args=(health_servicer, "helloworld.Greeter"),
            daemon=True,
    )
    toggle_health_status_thread.start()


class Echo(echo_pb2_grpc.EchoServicer):
    def EchoMessage(self, request, context):
        delay = float(0.000001 if not request.HasField("delay") else request.delay)
        repeats = int(1 if not request.HasField("repeats") else request.repeats)

        time.sleep(delay)
        return echo_pb2.EchoReply(message=f"{request.message * repeats}")


    def EchoMessageStreaming(self, request_iterator, context):
        for request in request_iterator:
            delay = float(0.000001 if not request.HasField("delay") else request.delay)
            repeats = int(1 if not request.HasField("repeats") else request.repeats)

            for _ in range(repeats):
                time.sleep(delay)
                yield echo_pb2.EchoReply(message=f"{request.message}")


def serve():
    # Set the tracer provider
    trace.set_tracer_provider(TracerProvider())

    # Create an OTLP exporter and specify the endpoint
    otlp_exporter = OTLPSpanExporter(endpoint=os.getenv('NVCF_TRACING_ENDPOINT_HTTP', 'http://localhost:4138/v1/traces'))

    # Add the exporter to the BatchSpanProcessor
    trace.get_tracer_provider().add_span_processor(
        BatchSpanProcessor(otlp_exporter)
    )

    grpc_client_instrumentor = GrpcInstrumentorClient()
    grpc_client_instrumentor.instrument()


    port = "8001"
    thread_count = int(os.getenv('WORKER_COUNT', 500))
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=thread_count))
    echo_pb2_grpc.add_EchoServicer_to_server(Echo(), server)
    server.add_insecure_port("[::]:" + port)
    _configure_health_server(server)
    reflection.enable_server_reflection((
        echo_pb2.DESCRIPTOR.services_by_name['Echo'].full_name,
        reflection.SERVICE_NAME,
    ), server)
    server.start()
    print("Server started, listening on " + port)

    server.wait_for_termination()


if __name__ == "__main__":
    logging.basicConfig()
    serve()
