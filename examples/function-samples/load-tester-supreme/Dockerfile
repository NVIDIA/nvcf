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

FROM golang:1.22 AS http-server-build

WORKDIR /go/src/http-app
COPY http-server .

RUN go mod download
RUN go vet -v
RUN go test -v

RUN CGO_ENABLED=0 go build -o /go/bin/http-app

FROM python:3.10-bookworm

ENV WORKER_COUNT=500

WORKDIR /app

COPY requirements.txt ./

RUN python -m pip install --no-cache-dir -U pip && \
    python -m pip install --no-cache-dir -r requirements.txt

COPY grpc-server/echo.proto /app/
RUN python -m grpc_tools.protoc -I /app/  --python_out=. --pyi_out=. --grpc_python_out=. /app/echo.proto

COPY grpc-server/grpc_echo_server.py /app/

ENV GIN_MODE=release
COPY --from=http-server-build /go/bin/http-app /app/

CMD python3 /app/grpc_echo_server.py & /app/http-app