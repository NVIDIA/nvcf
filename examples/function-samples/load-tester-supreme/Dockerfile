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

FROM golang:1.26.10 AS http-server-build

WORKDIR /go/src/http-app
COPY http-server .

RUN go mod download
RUN go vet -v
RUN go test -v

RUN CGO_ENABLED=0 go build -o /go/bin/http-app

FROM python:3.10-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Use the refreshed OS bundle for outbound gRPC telemetry instead of grpcio's bundled roots.
ENV GRPC_DEFAULT_SSL_ROOTS_FILE_PATH=/etc/ssl/certs/ca-certificates.crt

ENV WORKER_COUNT=500

WORKDIR /app

COPY requirements.txt ./

# Use the updated OS CA bundle to upgrade pip safely; old pip still trusts the affected emSign chains.
RUN python3 -m pip install --no-cache-dir \
        --cert /etc/ssl/certs/ca-certificates.crt 'pip>=26.2.2' && \
    python3 -m pip install --no-cache-dir -r requirements.txt

COPY grpc-server/echo.proto /app/
RUN python -m grpc_tools.protoc -I /app/  --python_out=. --pyi_out=. --grpc_python_out=. /app/echo.proto

COPY grpc-server/grpc_echo_server.py /app/

ENV GIN_MODE=release
COPY --from=http-server-build /go/bin/http-app /app/

CMD python3 /app/grpc_echo_server.py & /app/http-app
