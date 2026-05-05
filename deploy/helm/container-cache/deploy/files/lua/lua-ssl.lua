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
-- generate and cache an (expiring) CA bundle based on the
-- intermediate CA we have from vault and our private key.
-- use it to MITM the incomming TLS connection and masquerade
-- as which ever host the client asked for. this requires the
-- client to accept the CA chain from vault.
local ssl            = require "ngx.ssl"
local openssl_bignum = require "resty.openssl.bn"
local openssl_rand   = require "resty.openssl.rand"
local openssl_pkey   = require "resty.openssl.pkey"
local x509           = require "resty.openssl.x509"
local x509_extension = require "resty.openssl.x509.extension"
local x509_name      = require "resty.openssl.x509.name"
local x509_alt_name  = require "resty.openssl.x509.altname"
local x509_crl       = require "resty.openssl.x509.crl"
local certs_crl      = ngx.shared.certs_crl
local certs_crt      = ngx.shared.certs_crt
local certs_key      = ngx.shared.certs_key
local cache_seconds  = 3600 -- 1 hour
local cert_drift = 120 -- 2 mins
local cn_length_limit = 64

local function read_file(path)
    local fd = assert(io.open(path, "rb"))
    local contents = fd:read("*all")
    fd:close()
    return contents
end

local function generate_crl()
    ngx.log(ngx.INFO, "generating new crl")

    local ca_crt_pem = read_file("/etc/certs/ssl-bundle.crt")
    local ca_crt = x509.new(ca_crt_pem, "*")
    local ca_key = openssl_pkey.new(read_file("/etc/certs/leaf-ca.key"), { format = "*", type = "*" })

    local crl = x509_crl.new()

    -- last for 2 hours
    local now = os.time()
    assert(crl:set_last_update(now))
    assert(crl:set_next_update(now + cache_seconds * 2))

    -- issue name is the subject name from the prior level intermediate cert
    assert(crl:set_issuer_name(ca_crt:get_subject_name()))

    -- 2 represents 3, per the docs : https://github.com/fffonion/lua-resty-openssl?tab=readme-ov-file#crlget_-crlset_
    assert(crl:set_version(2))

    assert(crl:sign(ca_key))

    return assert(crl:tostring("DER"))
end

local function generate_ca_signed(sni_name, sni_ip, server_ip)
    ngx.log(ngx.INFO, "generating new cert for " .. sni_name)
    -- read ca files every time to support hot reload
    local ca_crt_pem = read_file("/etc/certs/ssl-bundle.crt")
    -- ngx.log(ngx.INFO, "read leaf ca pem", ca_crt_pem)
    local ca_crt = x509.new(ca_crt_pem, "*")
    local ca_key = openssl_pkey.new(read_file("/etc/certs/leaf-ca.key"), { format = "*", type = "*" })
    local key = openssl_pkey.new(certs_key:get("STATIC_KEY"), { format = "*", type = "*" })

    local crt = x509.new()
    assert(crt:set_pubkey(key))
    assert(crt:set_version(3))
    assert(crt:set_serial_number(openssl_bignum.from_binary(openssl_rand.bytes(16))))

    -- last for 2 hours
    local now = os.time()
    assert(crt:set_not_before(now  - cert_drift))
    assert(crt:set_not_after(now + cache_seconds * 2))

    local short_sni = sni_name
    if string.len(sni_name) > cn_length_limit then
        short_sni = string.gsub(sni_name, "^[^.]+", "*")
    end

    local name = assert(x509_name.new()
        :add("C", "US")
        :add("ST", "California")
        :add("L", "Santa Clara")
        :add("O", "NVIDIA Corporation")
        :add("OU", "NVCF")
        :add("CN", short_sni))

    assert(crt:set_subject_name(name))
    local alt_name = x509_alt_name.new()
    if sni_ip then
        alt_name:add("IP", sni_name)
        if server_ip and server_ip ~= "" and server_ip ~= sni_name then
            alt_name:add("IP", server_ip)
        end
    else
        alt_name:add("DNS", sni_name)
    end
    local node_ip = os.getenv("NODE_IP")
    if node_ip and node_ip ~= "" and node_ip ~= sni_name then
        alt_name:add("IP", node_ip)
    end
    assert(crt:set_subject_alt_name(alt_name))

    assert(crt:set_issuer_name(ca_crt:get_subject_name()))

    local crl_url = "http://caching-proxy-crls.nvidia.com:19080/crl"
    ngx.log(ngx.INFO, "CRL URL " .. crl_url)
    assert(crt:add_extension(x509_extension.new("crlDistributionPoints", "URI:" .. crl_url)))

    -- Not a CA
    assert(crt:set_basic_constraints { CA = false })

    -- Only allowed to be used for TLS connections (client or server)
    assert(crt:add_extension(x509_extension.new("extendedKeyUsage", "serverAuth,clientAuth")))

    -- RFC-3280 4.2.1.2
    assert(crt:add_extension(x509_extension.new("subjectKeyIdentifier", "hash", {
        subject = crt
    })))

    -- All done; sign
    assert(crt:sign(ca_key))

    local crt_pem = assert(crt:tostring("PEM"))
    local crt_chain_pem = crt_pem .. ca_crt_pem
    local crt_chain_der = assert(ssl.cert_pem_to_der(crt_chain_pem))
    return crt_chain_der
end

-- Check if browser supports SNI
local sni_ip = false
local server_ip = nil
local sni_name, err = ssl.server_name()
if not sni_name then
    local addr, addrtyp, err1 = ssl.raw_server_addr()
    if not addr then
        ngx.log(ngx.ERR, "failed to fetch raw server addr: ", err1)
        return ngx.exit(ngx.ERROR)
    end
    if addrtyp == "inet" then  -- IPv4
        server_ip = string.format("%d.%d.%d.%d", string.byte(addr, 1), string.byte(addr, 2),
                string.byte(addr, 3), string.byte(addr, 4))
        local node_ip = os.getenv("NODE_IP")
        if node_ip and node_ip ~= "" then
            sni_name = node_ip
        else
            sni_name = server_ip
        end
        sni_ip = true
    else
        ngx.log(ngx.ERR, "addtype is not IPv4")
        return ngx.exit(ngx.ERROR)
    end
end

-- Check if CA generation needs to be skipped
if sni_name == "caching-proxy-crls.nvidia.com:19080" then
    ngx.log(ngx.INFO, "Skipping CA generation")
    return
else
    ngx.log(ngx.INFO, "Proceeding with CA generation")
end

local cache_crl_content = certs_crl:get("crl")
if not cache_crl_content then
    local crl = generate_crl()
    certs_crl:set("crl", crl, cache_seconds)
    cache_crl_content = crl
end

local cache_crt_content = certs_crt:get(sni_name)
if not cache_crt_content then
    local crt = generate_ca_signed(sni_name, sni_ip, server_ip)
    certs_crt:set(sni_name, crt, cache_seconds)
    cache_crt_content = crt
end
local cache_key_content = certs_key:get("STATIC_KEY")

local ok, err = ssl.clear_certs()
if not ok then
    ngx.log(ngx.ERR, "failed to clear existing (fallback) certificates ", err)
    return ngx.exit(ngx.ERROR)
end

local ok, err = ssl.set_der_cert(cache_crt_content)
if not ok then
    ngx.log(ngx.ERR, "failed to set DER cert: ", err)
    return ngx.exit(ngx.ERROR)
end

local ok, err = ssl.set_der_priv_key(cache_key_content)
if not ok then
    ngx.log(ngx.ERR, "failed to set DER private key: ", err)
    return ngx.exit(ngx.ERROR)
end
