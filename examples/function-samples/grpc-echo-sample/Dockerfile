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

FROM python:3.10.13-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt ./

# Use the updated OS CA bundle to upgrade pip safely; old pip still trusts the affected emSign chains.
RUN python3 -m pip install --no-cache-dir \
        --cert /etc/ssl/certs/ca-certificates.crt 'pip>=26.2.2' && \
    python3 -m pip install --no-cache-dir -r requirements.txt

COPY echo.proto /app/
RUN python -m grpc_tools.protoc -I /app/  --python_out=. --pyi_out=. --grpc_python_out=. /app/echo.proto

COPY grpc_echo_server.py /app/

CMD python3 /app/grpc_echo_server.py
