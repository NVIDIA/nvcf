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

from openai import OpenAI


BASE_URL = os.environ.get("OPENAI_BASE_URL", "http://127.0.0.1:8000/v1")
CLIENT = OpenAI(
    api_key=os.environ.get("OPENAI_API_KEY", "not-needed"),
    base_url=BASE_URL,
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
    assert response.output_text == "tokentokentoken"

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
    assert response_text == "streamstream"

    chat = CLIENT.chat.completions.create(
        model="test-model",
        messages=[{"role": "user", "content": "hello"}],
    )
    assert chat.choices[0].message.content == "xxxx"

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
    assert chat_text == "chatchat"

    completion = CLIENT.completions.create(model="test-model", prompt="hello")
    assert completion.choices[0].text == "xxxx"

    embedding = CLIENT.embeddings.create(
        model="test-model",
        input=["one", "two"],
        encoding_format="float",
    )
    assert len(embedding.data) == 2

    models = CLIENT.models.list()
    assert any(model.id == "test-model" for model in models.data)
    assert CLIENT.models.retrieve("test-model").id == "test-model"

    print("OpenAI client compatibility check passed")


if __name__ == "__main__":
    main()
