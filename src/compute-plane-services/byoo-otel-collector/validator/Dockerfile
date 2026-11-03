FROM python:3.12-slim

ENV PYTHONPATH=/app/src

ADD --checksum=sha256:fd7f4dfa8190e014013b9f08f3e973f4e4c26ffd179a204895533a7254813a70 \
    https://files.pythonhosted.org/packages/3f/06/3e4420302363fbb71be3c7f35ce042f54bfca330e3e66b3a2e3c4cca579f/pip-26.2.2-py3-none-any.whl /tmp/pip-26.2.2-py3-none-any.whl
RUN python3 -m pip install --no-cache-dir --no-index --no-deps /tmp/pip-26.2.2-py3-none-any.whl && \
    rm /tmp/pip-26.2.2-py3-none-any.whl && \
    python3 -m pip install --no-cache-dir uv

WORKDIR /app

COPY cli/src/ src/
COPY cli/uv.lock .
COPY cli/pyproject.toml .
COPY cli/validator-config.yaml .
COPY golden/ /golden
RUN uv sync

ENTRYPOINT ["uv", "run", "python3", "-m", "src.validator"]
