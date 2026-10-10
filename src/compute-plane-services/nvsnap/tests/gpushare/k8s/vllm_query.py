# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Deterministic completion against a local vLLM server; prints {"secs", "text"}.
import json, sys, time, urllib.request
prompt = sys.argv[1] if len(sys.argv) > 1 else "List the first 10 prime numbers and explain why 1 is not prime."
model = json.load(urllib.request.urlopen("http://localhost:8000/v1/models", timeout=60))["data"][0]["id"]
body = json.dumps({"model": model, "prompt": prompt, "max_tokens": 128, "temperature": 0, "seed": 0}).encode()
t = time.time()
r = urllib.request.urlopen(urllib.request.Request("http://localhost:8000/v1/completions", body, {"Content-Type": "application/json"}), timeout=600)
out = json.load(r)["choices"][0]["text"]
print(json.dumps({"secs": round(time.time() - t, 2), "text": out}))
