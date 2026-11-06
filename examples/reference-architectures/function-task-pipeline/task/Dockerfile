# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

FROM python:3.12-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Use the fixed OS CA bundle without merging pip's affected bundled emSign roots.
RUN python3 -m pip install --no-cache-dir --use-deprecated=legacy-certs \
        --cert /etc/ssl/certs/ca-certificates.crt 'pip>=26.2.2'

WORKDIR /app

COPY inventory_artifacts.py /app/

RUN useradd --no-create-home --uid 1000 taskrunner
USER taskrunner

CMD ["python3", "/app/inventory_artifacts.py"]
