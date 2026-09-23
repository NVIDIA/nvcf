#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Check the real gateway path through the optional CPU fixture."""

import json
import sys
import time
import urllib.error
import urllib.request

base = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:18080"
model = sys.argv[2] if len(sys.argv) > 2 else "poc/poc-model"


def chat(model_id, stream=False):
    body = json.dumps({
        "model": model_id,
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 16,
        "stream": stream,
    }).encode()
    request = urllib.request.Request(
        base + "/v1/chat/completions", body,
        {"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return response.status, response.read().decode()
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode()


deadline = time.monotonic() + 90
while True:
    try:
        code, body = chat(model)
        if code == 200:
            break
    except (OSError, TimeoutError):
        pass
    if time.monotonic() > deadline:
        raise SystemExit("Gateway did not recover within 90 seconds")
    time.sleep(1)

response = json.loads(body)
assert response["choices"][0]["message"]["content"] == "POC routing works", response
assert response["model"] == model, response
assert response["usage"]["total_tokens"] == 6, response
print("PASS chat completion and token usage")

code, body = chat(model, True)
assert code == 200, (code, body)
events = [line[6:] for line in body.splitlines() if line.startswith("data: ")]
assert events[-1] == "[DONE]", events
chunks = [json.loads(event) for event in events[:-1]]
content = "".join(choice.get("delta", {}).get("content", "")
                  for chunk in chunks for choice in chunk.get("choices", []))
assert content == "POC routing works", content
assert any(chunk.get("usage", {}).get("total_tokens") == 6 for chunk in chunks), chunks
print("PASS SSE content, completion marker, and token usage")

code, body = chat("bare-model")
assert code == 400 and "prefix" in body, (code, body)
print("PASS current bare-model limitation is explicit (400)")

code, body = chat(model.split("/", 1)[0] + "/does-not-exist")
assert code == 404, (code, body)
print("PASS unknown model is rejected (404)")
