FROM python:3.12-slim

ENV PYTHONPATH=/app/src

# Use the fixed OS CA bundle without merging pip's affected bundled emSign roots.
RUN python3 -m pip install --no-cache-dir --use-deprecated=legacy-certs \
        --cert /etc/ssl/certs/ca-certificates.crt 'pip>=26.2.2' && \
    python3 -m pip install --no-cache-dir 'uv>=0.13.4'

WORKDIR /app

COPY cli/src/ src/
COPY cli/uv.lock .
COPY cli/pyproject.toml .
COPY cli/validator-config.yaml .
COPY golden/ /golden
RUN uv sync

ENTRYPOINT ["uv", "run", "python3", "-m", "src.validator"]
