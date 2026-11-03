#!/usr/bin/env python3
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

"""Check the sample through the public OpenAI Python client."""

import os
from urllib.parse import urlparse

import httpx
from openai import OpenAI


BASE_URL = os.environ.get("OPENAI_BASE_URL", "http://127.0.0.1:8000/v1")
API_KEY = os.environ.get("OPENAI_API_KEY", "not-needed")
if API_KEY != "not-needed" and urlparse(BASE_URL).scheme != "https":
    raise ValueError("OPENAI_BASE_URL must use HTTPS when OPENAI_API_KEY is set")

CLIENT = OpenAI(
    api_key=API_KEY,
    base_url=BASE_URL,
    http_client=httpx.Client(follow_redirects=False),
    _strict_response_validation=True,
)


def main() -> None:
    response = CLIENT.responses.create(
        model="test-model",
        input="hello",
        extra_headers={
            "X-Load-Tester-Chunk": "token",
            "X-Load-Tester-Output-Chunks": "3",
        },
    )
    if response.output_text != "tokentokentoken":
        raise RuntimeError("unexpected Responses output")

    response_events = list(
        CLIENT.responses.create(
            model="test-model",
            input="hello",
            stream=True,
            extra_headers={
                "X-Load-Tester-Chunk": "stream",
                "X-Load-Tester-Output-Chunks": "2",
            },
        )
    )
    response_text = "".join(
        event.delta
        for event in response_events
        if event.type == "response.output_text.delta"
    )
    if response_text != "streamstream":
        raise RuntimeError("unexpected streamed Responses output")

    chat = CLIENT.chat.completions.create(
        model="test-model",
        messages=[{"role": "user", "content": "hello"}],
    )
    if chat.choices[0].message.content != "xxxx":
        raise RuntimeError("unexpected chat completion output")

    chat_chunks = list(
        CLIENT.chat.completions.create(
            model="test-model",
            messages=[{"role": "user", "content": "hello"}],
            stream=True,
            extra_headers={
                "X-Load-Tester-Chunk": "chat",
                "X-Load-Tester-Output-Chunks": "2",
            },
        )
    )
    chat_text = "".join(
        choice.delta.content or ""
        for chunk in chat_chunks
        for choice in chunk.choices
    )
    if chat_text != "chatchat":
        raise RuntimeError("unexpected streamed chat completion output")

    completion = CLIENT.completions.create(model="test-model", prompt="hello")
    if completion.choices[0].text != "xxxx":
        raise RuntimeError("unexpected completion output")

    embedding = CLIENT.embeddings.create(
        model="test-model",
        input=["one", "two"],
        encoding_format="float",
    )
    if len(embedding.data) != 2:
        raise RuntimeError("unexpected embedding count")

    models = CLIENT.models.list()
    if not any(model.id == "test-model" for model in models.data):
        raise RuntimeError("test-model is missing from the model list")
    if CLIENT.models.retrieve("test-model").id != "test-model":
        raise RuntimeError("unexpected retrieved model")

    print("OpenAI client compatibility check passed")


if __name__ == "__main__":
    main()
