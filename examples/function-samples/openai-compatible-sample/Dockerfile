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

FROM golang:1.23 AS build

WORKDIR /src
COPY http-server/go.mod ./
RUN go mod download
COPY http-server/ ./
RUN CGO_ENABLED=0 go build -o /openai-compatible-sample .

FROM alpine:3.20

COPY --from=build /openai-compatible-sample /app/openai-compatible-sample
RUN adduser -D -H app && chown app:app /app/openai-compatible-sample

EXPOSE 8000

USER app

CMD ["/app/openai-compatible-sample"]
