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
-- Depth 0 is the signing intermediate that leaf certificates are minted
-- from; when Vault is enabled the sidecar replaces it before expiry and this
-- gauge is how a stalled rotation becomes visible. Higher depths are the CA
-- chain above it, whose expiry is fixed at provisioning time.
-- Run from the /cache_metrics handler, so the file is read once per scrape.
local x509 = require "resty.openssl.x509"

local bundle_path = "/etc/certs/ssl-bundle.crt"

-- Drop every series from the previous scrape first. A gauge keeps old label
-- sets until they are removed, so without this a bundle that shrank, changed
-- a CN, or failed to read would keep exporting values that look current.
tls_cert_not_after:reset()
-- Pessimistic until the bundle is parsed: an error raised below is caught by
-- the metrics handler, and the flag must not keep last scrape's 1.
tls_bundle_read_ok:set(0)

local fd, open_err = io.open(bundle_path, "rb")
if not fd then
    ngx.log(ngx.WARN, "cert expiry metrics: cannot open ", bundle_path, ": ", open_err)
    return
end
local pem = fd:read("*all")
fd:close()
if not pem then
    ngx.log(ngx.WARN, "cert expiry metrics: cannot read ", bundle_path)
    return
end

-- depth counts parsed certificates, not PEM blocks, so a malformed block
-- cannot shift the signing intermediate away from depth 0.
local depth = 0
local block_no = 0
for block in pem:gmatch("%-%-%-%-%-BEGIN CERTIFICATE%-%-%-%-%-.-%-%-%-%-%-END CERTIFICATE%-%-%-%-%-") do
    block_no = block_no + 1
    local crt, err = x509.new(block, "PEM")
    if crt then
        local cn = ""
        local name = crt:get_subject_name()
        if name then
            local entry = name:find("CN")
            if entry and entry.blob then
                cn = entry.blob
            end
        end
        tls_cert_not_after:set(crt:get_not_after(), { tostring(depth), cn })
        depth = depth + 1
    else
        ngx.log(ngx.WARN, "cert expiry metrics: cannot parse PEM block ", block_no, " of ", bundle_path, ": ", err)
    end
end
tls_bundle_read_ok:set(depth > 0 and 1 or 0)
