FROM python:3.12-slim

ENV PYTHONPATH=/app/src

RUN pip install uv

WORKDIR /app

COPY cli/src/ src/
COPY cli/uv.lock .
COPY cli/pyproject.toml .
COPY cli/validator-config.yaml .
COPY golden/ /golden
RUN uv sync

ENTRYPOINT ["uv", "run", "python3", "-m", "src.validator"]
