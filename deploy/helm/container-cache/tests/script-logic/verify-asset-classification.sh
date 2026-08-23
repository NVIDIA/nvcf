#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Behavioural tests for the per-asset metric classification.
#
# This lifts the shipped Lua out of the rendered ConfigMap and executes it, so
# it tests the code that actually runs rather than a reimplementation of it.
# The fixtures are real request URIs and hosts captured from production traffic.
#
# The property under test is grouping: one model is pulled as hundreds of
# 512MiB ranges, each with its own versionId, Signature and ssec-key query
# parameters. All of them must collapse to a single label value, or the metric
# becomes unusable and blows up cardinality.
#
# Requires a Lua 5.1-compatible interpreter (lua5.1, lua, or luajit). Skips with
# a visible notice if none is present, rather than passing silently.
#
# Run from the chart subtree:
#   bash tests/script-logic/verify-asset-classification.sh
set -euo pipefail

LUA=""
for c in luajit lua5.1 lua; do
  if command -v "$c" >/dev/null 2>&1; then LUA="$c"; break; fi
done
if [ -z "${LUA}" ]; then
  echo "SKIP: no Lua interpreter found (tried luajit, lua5.1, lua)." >&2
  echo "      Install one to run these assertions, e.g. apt-get install lua5.1." >&2
  exit 0
fi

CHART_DIR="$(cd "$(dirname "$0")/../.." && pwd)/deploy"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

helm template t "$CHART_DIR" > "$TMP/rendered.yaml" 2>/dev/null

# Lift the classification block: from the marker comment to the line that closes
# it, which is the byte counter's `end`. Taking it verbatim is the point; if the
# shipped logic changes, these tests exercise the change.
# The block contains nested `if`s, so stopping at the first `end` truncates it.
# Record the indentation of the outer guard and stop at the `end` that matches
# it, which is the only one that closes the block.
awk '/-- Per-asset accounting\./{f=1}
     f && !guard && /if request_uri ~= nil/ {
       match($0, /^ */); guard = RLENGTH; print; next
     }
     f{print}
     guard && /^ *end$/ {
       match($0, /^ */)
       if (RLENGTH == guard) exit
     }' "$TMP/rendered.yaml" > "$TMP/block.lua"

if [ ! -s "$TMP/block.lua" ]; then
  echo "FAIL: could not lift the asset classification block from the render" >&2
  exit 1
fi

# Harness: stub the two metric objects, feed one request, return what was
# recorded. The lifted block references request_uri, host, cache_status and
# body_size as locals from the enclosing log_by_lua_block, so they are declared
# here the same way.
cat > "$TMP/run.lua" <<'LUA'
local block = ...
local recorded = {}
proxy_asset_requests = { inc = function(self, n, labels)
  recorded.req = { n = n, source = labels[1], asset = labels[2], status = labels[3] }
end }
proxy_asset_bytes = { inc = function(self, n, labels)
  recorded.bytes = { n = n, source = labels[1], asset = labels[2], status = labels[3] }
end }

-- loadstring in 5.1/LuaJIT, load in 5.2+.
local compile = loadstring or load

local function classify(uri, host_in, status_in, size_in)
  recorded = {}
  local chunk = assert(compile(
    "local request_uri, host, cache_status, body_size = ...\n" .. block))
  chunk(uri, host_in, status_in, size_in)
  return recorded
end

local failures = 0
local function check(name, got, want)
  if got ~= want then
    print(string.format("FAIL: %s\n        got:  %s\n        want: %s",
      name, tostring(got), tostring(want)))
    failures = failures + 1
  end
end

-- Real NGC model range request. Query string carries versionId, ssec-key and a
-- CloudFront Signature; none of it may reach the label.
local ngc_host = "xfiles.ngc.nvidia.com"
local ngc1 = "/org/qc69jvmznzxy/team/llm_nim/modelsv2/nemotron3-ultra-genrm/blobs/sha256/34/34933feef4b44a022d56f34a0a64932b8fd939cdbcf8ae2e0cd19070b716679b/file?ssec-algo=AES256&versionId=QthslkuRtCpclJmdfB600fvoNmeWT4hF&Signature=wrF0mIIi6UptQTXT"
local r = classify(ngc1, ngc_host, "HIT", 536870912)
check("ngc source",  r.req.source, "ngc")
check("ngc asset",   r.req.asset,  "qc69jvmznzxy/llm_nim/nemotron3-ultra-genrm")
check("ngc status",  r.req.status, "HIT")
check("ngc bytes",   r.bytes.n,    536870912)

-- GROUPING: a different file, a different byte range and a different versionId
-- of the same model must produce the identical label value.
local ngc2 = "/org/qc69jvmznzxy/team/llm_nim/modelsv2/nemotron3-ultra-genrm/blobs/sha256/24/24e6f4c84eec7c10872d4f18c5ea031ab41d25ebd575228e29eb03ca95b2f73c/file?versionId=y4mfHRXUsYBsiVSmmtbqaURE8Tlhg_ss"
local r2 = classify(ngc2, ngc_host, "MISS", 536870912)
check("grouping: same asset across files/ranges", r2.req.asset, r.req.asset)
check("miss still recorded", r2.req.status, "MISS")

-- The query string must be stripped before matching. A query value that
-- contains a slash otherwise satisfies the `([^/]+)/` segment matcher and
-- smuggles request-unique text into the label, which is unbounded cardinality.
local tricky = "/org/o/team/t/modelsv2/mymodel?redirect=/a/b/c"
local rq = classify(tricky, ngc_host, "HIT", 1)
check("query string cannot leak into the asset label", rq.req.asset, "o/t/mymodel")

-- Regression: the team-less fallback used to match a team path and label the
-- TEAM as the model, so /org/o/team/t/... came out as "o/no-team/t".
local rteam = classify("/org/o/team/myteam/modelsv2/mymodel/blobs/x", ngc_host, "HIT", 1)
check("team is not mistaken for the model", rteam.req.asset, "o/myteam/mymodel")

-- A second model must NOT collapse into the first.
local r3 = classify("/org/qc69jvmznzxy/team/llm_nim/modelsv2/kimi-k3/blobs/sha256/aa/aa/file?x=1", ngc_host, "HIT", 1)
check("distinct models stay distinct", r3.req.asset, "qc69jvmznzxy/llm_nim/kimi-k3")

-- Empty cache_status is the case the older metric block dropped entirely:
-- 45.6 percent of requests and 786 GiB on a live sample.
local r4 = classify(ngc1, ngc_host, "", 536870912)
check("empty status becomes NONE", r4.req.status, "NONE")
check("empty status still counts bytes", r4.bytes.n, 536870912)
local r5 = classify(ngc1, ngc_host, nil, 100)
check("nil status becomes NONE", r5.req.status, "NONE")

-- S3. The bucket is the useful grouping; the object key is unbounded and must
-- not appear. This is a real upload path seen in production.
local r6 = classify("/bulk-upload-temp-folder/f5e47a34-b184-4322-83c3-d73f29671db0/part-0.p",
  "nim-payload-metrics-byoo-kratos-xpor2.s3.us-west-2.amazonaws.com", "", 0)
check("s3 source", r6.req.source, "s3")
check("s3 asset is the bucket", r6.req.asset, "nim-payload-metrics-byoo-kratos-xpor2")
-- A zero-byte response must not record a byte sample.
check("zero-byte response records no bytes", r6.bytes, nil)

-- The same bucket in another region is the same asset.
local r7 = classify("/x/y", "nim-payload-metrics-byoo-kratos-xpor2.s3.us-west-1.amazonaws.com", "", 1)
check("s3 asset is region independent", r7.req.asset, r6.req.asset)

-- HuggingFace.
local r8 = classify("/meta-llama/Llama-3-8B/resolve/main/model.safetensors?download=true",
  "huggingface.co", "HIT", 10)
check("hf source", r8.req.source, "hf")
check("hf asset",  r8.req.asset,  "meta-llama/Llama-3-8B")

-- NGC without a team segment.
local r9 = classify("/org/someorg/modelsv2/somemodel/blobs/sha256/aa/bb/file", ngc_host, "HIT", 1)
check("ngc team-less asset", r9.req.asset, "someorg/no-team/somemodel")

-- Unknown host falls back rather than inventing a label.
local r10 = classify("/whatever/path", "example.invalid", "HIT", 1)
check("unknown host source", r10.req.source, "other")
check("unknown host asset",  r10.req.asset,  "other")

-- An NGC path that does not match the expected shape must not produce a
-- half-parsed label.
local r11 = classify("/health", ngc_host, "HIT", 1)
check("unparseable ngc path", r11.req.asset, "other")

-- Manifest traffic is excluded, matching the other metrics.
local r12 = classify("/v2/foo/manifests/latest", ngc_host, "HIT", 1)
check("manifest excluded", r12.req, nil)

-- Label values are bounded even for a pathological path.
local long = "/org/" .. string.rep("a", 300) .. "/team/b/modelsv2/c/blobs/x"
local r13 = classify(long, ngc_host, "HIT", 1)
if r13.req and #r13.req.asset > 120 then
  print(string.format("FAIL: asset label not truncated: %d chars", #r13.req.asset))
  failures = failures + 1
end

if failures > 0 then
  print(string.format("\n%d assertion(s) failed", failures))
  os.exit(1)
end
print("PASS: asset classification behaves correctly on real production URIs")
LUA

"${LUA}" -e "
local f = assert(io.open('$TMP/block.lua')); local block = f:read('*a'); f:close()
local run = assert(loadfile('$TMP/run.lua')); run(block)
"
