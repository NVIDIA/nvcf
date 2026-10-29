-- SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
--
-- Consistent-hash owner routing for the container-cache proxy tier, shared by
-- every server block that opts in. Route each request to a single owner pod so
-- a blob is cached once across the fleet instead of once per pod.
--
-- The including location sets, before rewrite_by_lua_file:
--   $cc_hash_key  this block's proxy_cache_key (the exact cache identity)
--   $cc_replicas  replicaCount (N)
-- and $cc_owner is declared at server scope (proxy-common.conf), where the
-- shared @cc_relay location also lives.

-- A request already relayed is served locally: relaying is at most one hop.
-- The marker is not authenticated; this is an internal, access-restricted
-- service (see proxy-common.conf @cc_relay).
if ngx.req.get_headers()["X-NVCF-CC-Relayed"] then
  return
end

local n = tonumber(ngx.var.cc_replicas)
if not n or n < 2 then
  return  -- single pod (or unset): nothing to distribute
end

-- Owner = md5(cache_key) mod N. Same key nginx caches on, so each byte-range
-- chunk maps to its own owner and a large blob spreads across the tier.
local owner = tonumber(string.sub(ngx.md5(ngx.var.cc_hash_key), 1, 8), 16) % n

-- Self ordinal from the pod hostname (statefulset pod name).
local self = tonumber(string.match(ngx.var.hostname, "(%d+)$"))
if self ~= nil and owner ~= self then
  ngx.var.cc_owner = "cc_owner_" .. owner
  return ngx.exec("@cc_relay")
end
-- Unparseable hostname falls through to local serving, which is safe: it
-- degrades to non-routed behavior for this pod only.
