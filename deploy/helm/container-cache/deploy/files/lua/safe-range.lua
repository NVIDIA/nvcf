-- SPDX-FileCopyrightText: Copyright (c) 2023-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
-- SPDX-License-Identifier: Apache-2.0
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.
--[[
Neutralises a Range header that names the "bytes" unit but does not parse.

Why this exists. The NGC CLI computes a range of first-byte-pos 0 through
last-byte-pos size-1. For a zero-length file that is 0 through -1, so it sends:

    Range: bytes=0--1

RFC 7233 section 2.1 defines last-byte-pos as 1*DIGIT, which admits no sign, so
the byte-range-set does not parse. nginx rejects it at parse time and answers
416. That is conformant: section 3.1 says a server SHOULD send 416 when ranges
are invalid. The NGC origin instead ignores the bad header and returns 200 with
the full representation, which is lenient but permitted.

The result is that a client which downloads successfully from the origin fails
behind this cache. Since we are a transparent intermediary in front of a client
we do not control, we match the origin rather than being stricter than it.

Scope, deliberately narrow:
  - Only a header naming the "bytes" unit is considered. nginx already ignores
    units it does not understand, so those are left untouched.
  - Only a header that fails to PARSE is cleared. A header that parses but is
    unsatisfiable or semantically invalid, such as "bytes=100-50", keeps
    nginx's 416. That behaviour is correct and the origin rejects it too.

Used via set_by_lua_file so it runs in configuration order alongside the
surrounding `set` directives. That ordering matters: $http_range is cached by
nginx once evaluated, so any `set` that reads it before this runs would pin the
malformed value into the cache key. Callers must use the returned $safe_range,
not $http_range, in proxy_cache_key, proxy_set_header Range and $cc_hash_key.

Returns the original value when it is valid or absent, otherwise an empty
string, having also removed the header so nginx's range filter does not see it.
]]

local range = ngx.var.http_range
if not range or range == "" then
  return ""
end

local range_set = range:match("^[Bb][Yy][Tt][Ee][Ss]=(.*)$")
if not range_set then
  return range
end

local valid = range_set ~= ""
if valid then
  -- byte-range-spec = first-byte-pos "-" [ last-byte-pos ]
  -- suffix-byte-range-spec = "-" suffix-length
  for spec in (range_set .. ","):gmatch("([^,]*),") do
    spec = spec:gsub("^%s*(.-)%s*$", "%1")
    if not (spec:match("^%d+%-%d*$") or spec:match("^%-%d+$")) then
      valid = false
      break
    end
  end
end

if valid then
  return range
end

ngx.req.clear_header("Range")
return ""
