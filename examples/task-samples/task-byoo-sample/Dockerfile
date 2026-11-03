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
FROM python:3.12-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY requirements.txt ./

# Use the fixed OS CA bundle without merging pip's affected bundled emSign roots.
RUN python3 -m pip install --no-cache-dir --use-deprecated=legacy-certs \
        --cert /etc/ssl/certs/ca-certificates.crt 'pip>=26.2.2' && \
    python3 -m pip install --no-cache-dir -r requirements.txt

COPY main.py .
COPY byoo.py .

CMD ["python3", "main.py"]
