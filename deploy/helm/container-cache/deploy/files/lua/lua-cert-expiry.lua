-- SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
-- Publish the notAfter of every certificate in the served TLS bundle.
-- Depth is the certificate's position in ssl-bundle.crt. The producer writes
-- the signing intermediate first, so depth 0 is the intermediate by
-- construction; when Vault is enabled the sidecar replaces it before expiry
-- and this gauge is how a stalled rotation becomes visible. Higher depths are
-- the CA chain above it, whose expiry is fixed at provisioning time.
-- Run from the /cache_metrics handler, so the file is read once per scrape.
--
-- Series are updated in place and deleted only when the bundle shrinks or
-- fails to parse. nginx-lua-prometheus never reuses a key-index slot once a
-- series is removed, so a reset() per scrape would grow the index for the
-- life of the pod and make every worker rescan it after each scrape.
local x509 = require "resty.openssl.x509"

local bundle_path = "/etc/certs/ssl-bundle.crt"
local state = ngx.shared.cert_metrics_state
local PUBLISHED = "published_depths"

-- Pessimistic until the bundle is parsed: an error raised below is caught by
-- the metrics handler, and the flag must not keep last scrape's 1.
tls_bundle_read_ok:set(0)

local function unpublish(from, to)
    for d = from, to - 1 do
        tls_cert_not_after:del({ tostring(d) })
    end
end

-- A bundle we cannot trust publishes nothing: drop whatever the last good
-- scrape left behind rather than show a partial or shifted view.
local function fail(msg)
    ngx.log(ngx.WARN, "cert expiry metrics: ", msg)
    local prev = tonumber(state:get(PUBLISHED)) or 0
    if prev > 0 then
        unpublish(0, prev)
        state:set(PUBLISHED, 0)
    end
end

local fd, open_err = io.open(bundle_path, "rb")
if not fd then
    return fail("cannot open " .. bundle_path .. ": " .. tostring(open_err))
end
local pem = fd:read("*all")
fd:close()
if not pem then
    return fail("cannot read " .. bundle_path)
end

local not_afters = {}
for block in pem:gmatch("%-%-%-%-%-BEGIN CERTIFICATE%-%-%-%-%-.-%-%-%-%-%-END CERTIFICATE%-%-%-%-%-") do
    local crt, err = x509.new(block, "PEM")
    if not crt then
        return fail("cannot parse certificate at position " .. #not_afters .. " of " .. bundle_path .. ": " .. tostring(err))
    end
    not_afters[#not_afters + 1] = crt:get_not_after()
end

-- The matcher silently skips a block with no END marker, so a truncated
-- bundle would otherwise look complete. Every BEGIN must have parsed.
local begins = select(2, pem:gsub("%-%-%-%-%-BEGIN CERTIFICATE%-%-%-%-%-", ""))
if begins ~= #not_afters then
    return fail(bundle_path .. " has " .. begins .. " BEGIN markers but " .. #not_afters .. " complete certificates")
end
if #not_afters == 0 then
    return fail(bundle_path .. " holds no certificate")
end

for i, not_after in ipairs(not_afters) do
    tls_cert_not_after:set(not_after, { tostring(i - 1) })
end
local prev = tonumber(state:get(PUBLISHED)) or 0
if prev > #not_afters then
    unpublish(#not_afters, prev)
end
state:set(PUBLISHED, #not_afters)
tls_bundle_read_ok:set(1)
