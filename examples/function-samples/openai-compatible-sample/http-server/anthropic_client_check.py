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

"""Check the sample Messages route through the public Anthropic Python client."""

import os
from urllib.parse import urlparse

import anthropic


BASE_URL = os.environ.get("ANTHROPIC_BASE_URL", "http://127.0.0.1:8000")
API_KEY = os.environ.get("ANTHROPIC_API_KEY", "not-needed")
if API_KEY != "not-needed" and urlparse(BASE_URL).scheme != "https":
    raise ValueError("ANTHROPIC_BASE_URL must use HTTPS when ANTHROPIC_API_KEY is set")

CLIENT = anthropic.Anthropic(
    api_key=API_KEY,
    base_url=BASE_URL,
    max_retries=0,
    _strict_response_validation=True,
)
MESSAGES = [{"role": "user", "content": "hello"}]


def main() -> None:
    message = CLIENT.messages.create(
        model="test-model",
        max_tokens=1024,
        messages=MESSAGES,
        extra_headers={
            "X-Load-Tester-Chunk": "token",
            "X-Load-Tester-Output-Chunks": "3",
        },
    )
    if message.content[0].text != "tokentokentoken":
        raise RuntimeError("unexpected Messages output")
    if message.stop_reason != "end_turn" or message.usage.output_tokens != 3:
        raise RuntimeError("unexpected Messages stop reason or usage")

    with CLIENT.messages.stream(
        model="test-model",
        max_tokens=2,
        messages=MESSAGES,
        extra_headers={
            "X-Load-Tester-Chunk": "stream",
            "X-Load-Tester-Output-Chunks": "3",
        },
    ) as stream:
        streamed_text = "".join(stream.text_stream)
        final = stream.get_final_message()
    if streamed_text != "streamstream" or final.content[0].text != streamed_text:
        raise RuntimeError("unexpected streamed Messages output")
    if final.stop_reason != "max_tokens" or final.usage.output_tokens != 2:
        raise RuntimeError("unexpected streamed Messages stop reason or usage")

    try:
        CLIENT.messages.create(
            model="test-model",
            max_tokens=1024,
            messages=MESSAGES,
            extra_headers={"X-Load-Tester-Status-Code": "529"},
        )
    except anthropic.APIStatusError as error:
        if error.status_code != 529 or error.body["error"]["type"] != "overloaded_error":
            raise RuntimeError("unexpected injected Messages error") from error
    else:
        raise RuntimeError("injected Messages error was not raised")

    try:
        with CLIENT.messages.stream(
            model="test-model",
            max_tokens=1024,
            messages=MESSAGES,
            extra_headers={
                "X-Load-Tester-Output-Chunks": "2",
                "X-Load-Tester-Stream-Error-After-Chunks": "1",
            },
        ) as stream:
            stream.until_done()
    except anthropic.APIStatusError as error:
        if "injected streaming error" not in str(error):
            raise RuntimeError("unexpected injected Messages stream error") from error
    else:
        raise RuntimeError("injected Messages stream error was not raised")

    print("Anthropic client compatibility check passed")


if __name__ == "__main__":
    main()
