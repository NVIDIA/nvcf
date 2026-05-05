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
--[[
This Lua script implements access control for S3 bucket access through NGINX in a
multi-tenant environment. It handles HEAD, GET, and presigned URL requests,
balancing access control with performance optimization through caching.

Features:
    - Locking mechanism to manage concurrent requests (similar to `proxy_cache_lock`).
    - Authentication caching for HEAD requests.
    - Presigned URL verification and caching.
    - Public vs. authenticated access distinction for GET requests.

Requires the following Lua shared dictionaries to be declared in NGINX conf:
    - lock_by_request_uri_table: Required for locking mechanism.
    - head_request_cache: Stores bursty HEAD requests.
    - presigned_url_cache: Caches verified presigned URLs honoring their expiration.

Requires the following NGINX conf variables:
    - `cache_ttl`: Time to live for the head request cache.
    - `disable_cache`: Set to '1' when the access check couldn't be performed.

Usage:
This script should be included in the NGINX conf using the `access_by_lua_file` directive.

]]

local lock = require "resty.lock"
local http = require "resty.http"

local cache_ttl = tonumber(ngx.var.cache_ttl)
local MIN_CACHE_EXPIRATION_THRESHOLD = 10

local function getsslservername(s)
    if s and s ~= "" then return s end
    return ngx.req.get_headers()["Host"]
end

local function http_timestamp()
    local now = ngx.now()
    local epochSeconds = math.floor(now)
    return os.date("!%a, %d %b %Y %X GMT", epochSeconds)
end



local function logheaders(s)
    for k, v in pairs(s) do
        ngx.log(ngx.ERR, "Header: ", k, " : Value:", v)
    end
end

local function contains(s, k)
    return s[k] ~= nil
end

-- Returns true if content caching is disabled (Cache-Control: no-cache).
local function is_cache_disabled()
    return ngx.var.disable_cache ~= nil and ngx.var.disable_cache == "1"
end

-- Disabling caching forces NGINX to reach out to upstream,
-- therefore access check can be skipped.
local function disable_cache_on_error()
    ngx.var.disable_cache = "1"
end

-- Executes a function within a lock context, similar to NGINX's
-- `proxy_cache_lock` directive. This reduces the number of concurrent
-- executions for a given request URI, minimizing load on upstream
-- resources.
--
-- The lock is based on the request URI. If a lock is already held, the
-- function will still execute but log a warning. Note that this doesn't
-- guarantee exclusive execution or prevent race conditions. Concurrent
-- execution is possible if the lock cannot be acquired.
local function with_lock(func, timeout)
    timeout = timeout or 5
    local cache_key = ngx.var.request_uri
    local lock_acquired = false

    -- Try to create lock.
    local lock_obj, err = lock:new("lock_by_request_uri_table", { timeout = timeout })
    if lock_obj then
        local elapsed, err = lock_obj:lock(cache_key)
        lock_acquired = (elapsed ~= nil)
        if not lock_acquired then
            ngx.log(ngx.WARN, "Lock not acquired: ", err)
        end
    else
        ngx.log(ngx.WARN, "Failed to create lock: ", err)
    end

    -- Execute function regardless of lock status.
    local ok, result = pcall(func)

    -- Release lock if it was acquired.
    if lock_acquired then
        local unlocked, err = lock_obj:unlock()
        if not unlocked then
            ngx.log(ngx.ERR, "Failed to unlock: ", err)
        end
    end

    if not ok then
        ngx.log(ngx.ERR, "Protected function failed: ", tostring(result))
        disable_cache_on_error()
        return ngx.HTTP_INTERNAL_SERVER_ERROR
    end

    return result
end

-- Checks access to the request URI. If the resource does not exist or access is forbidden,
-- this function will return an error to the client and NGINX will not handle the request further.
-- Otherwise, this function returns the status code received from upstream, and the caller
-- can decide whether to cache it or do something else.
local function access_upstream_resource()
    local headers = ngx.req.get_headers()
    if not contains(headers, "If-Modified-Since") then
        headers["If-Modified-Since"] = http_timestamp()
    end

    local function do_request()
        local httpc = http:new()
        return httpc:request_uri(
            ngx.var.scheme .. "://" .. ngx.var.host .. ngx.var.request_uri,
            {
                method = ngx.var.request_method,
                headers = headers,
                ssl_server_name = getsslservername(ngx.var.ssl_server_name)
            }
        )
    end

    local res, err = do_request()
    if not res then
        ngx.log(ngx.WARN, "retrying auth request: ", err)
        res, err = do_request()
    end

    if not res then
        ngx.log(ngx.ERR, "auth request failed: ", err)
        disable_cache_on_error()
        return ngx.HTTP_INTERNAL_SERVER_ERROR
    end

    -- If the upstream response includes a Last-Modified header, store it in a
    -- variable for potential use in the header_filter_by_lua. This will be used
    -- to compare against the cached Last-Modified time and purge the cache if the
    -- upstream resource has been updated since it was cached.
    if ngx.var.upstream_last_modified ~= nil then
        local last_modified = res.headers["Last-Modified"]
        if last_modified then
            ngx.var.upstream_last_modified = last_modified
        end
    end

    -- Log headers when we get 412.
    -- 412 doesn't have a constant (circa 2024):
    -- https://github.com/openresty/lua-nginx-module?tab=readme-ov-file#http-status-constants
    if res.status == 412 then
        ngx.log(ngx.ERR, "***** Request headers *****")
        logheaders(ngx.req.get_headers())
        ngx.log(ngx.ERR, "***** Response headers *****")
        logheaders(res.headers)
    end

    if res.status == ngx.HTTP_PARTIAL_CONTENT then
        -- Treat partial content as OK since it indicates the resource exists
        -- and is accessible.
        return ngx.HTTP_OK
    elseif res.status == ngx.HTTP_FORBIDDEN then
        -- AWS doesn't return a body for Forbidden errors, so we can return
        -- the response immediately and close the connection.
        ngx.status = res.status
        ngx.header = res.headers
        ngx.log(ngx.WARN, "request forbidden")
        return ngx.exit(res.status)
    else
        return res.status
    end
end

local function check_head_auth_cache()
    local head_cache = ngx.shared.head_request_cache
    if not head_cache then return nil end
    return head_cache:get(ngx.var.request_uri)
end

local function check_and_cache_head_auth()
    if check_head_auth_cache() then return end

    -- Cache only 200 (OK) and 404 (Not Found) responses. These statuses are safe
    -- to cache for HEAD requests because they indicate either that the request
    -- has access to the resource, or that access is not needed because
    -- the resource doesn't exist.
    local status = access_upstream_resource()
    if status == ngx.HTTP_OK or status == ngx.HTTP_NOT_FOUND then
        local head_cache = ngx.shared.head_request_cache
        if not head_cache then return end
        head_cache:set(ngx.var.request_uri, status, cache_ttl)
    end
end

-- Determines whether the current request is for a presigned URL. If so,
-- it calculates the expiration time. Otherwise, it returns nil.
local function try_get_presigned_url_expiration()
    local args = ngx.req.get_uri_args()
    local x_aws_date, x_aws_expires, x_aws_signature
    for k, v in pairs(args) do
        if k == "X-Amz-Date" then
            x_aws_date = v
        elseif k == "X-Amz-Expires" then
            x_aws_expires = v
        elseif k == "X-Amz-Signature" then
            -- The signature value is not used, but its presence indicates
            -- this is a presigned URL.
            x_aws_signature = v
        end
        if x_aws_date and x_aws_expires and x_aws_signature then break end
    end
    if x_aws_date and x_aws_expires and x_aws_signature then
        -- Parse ISO-8601 date.
        local year, month, day, hour, min, sec = x_aws_date:match("(%d%d%d%d)(%d%d)(%d%d)T(%d%d)(%d%d)(%d%d)Z")
        if not (year and month and day and hour and min and sec) then
            return nil, nil
        end
        local aws_date = os.time({ year = year, month = month, day = day, hour = hour, min = min, sec = sec })
        if aws_date > os.time() then
            -- Do not cache if X-Aws-Date is in the future.
            return nil, nil
        end
        local expires = tonumber(x_aws_expires)
        if not expires then return nil, nil end
        local expiration_time = aws_date + tonumber(expires)
        return ngx.var.request_uri, expiration_time
    end
    -- Not a presigned URL or missing required parameters.
    return nil, nil
end

local function check_verified_presigned_url(presigned_url_cache, presigned_url)
    if not presigned_url_cache then return nil end
    local cached_status = presigned_url_cache:get(presigned_url)
    return cached_status
end

-- Caches a verified presigned URL if it's not expiring soon.
local function cache_verified_presigned_url(presigned_url_cache, presigned_url, expiration)
    if not presigned_url_cache then return end
    local current_time = os.time()
    local time_until_expiration_in_seconds = expiration - current_time
    if time_until_expiration_in_seconds >= MIN_CACHE_EXPIRATION_THRESHOLD then
        assert(MIN_CACHE_EXPIRATION_THRESHOLD > 2)
        local cache_ttl = time_until_expiration_in_seconds - 2
        presigned_url_cache:set(presigned_url, true, cache_ttl)
    end
end

local function handle_presigned_url_request(presigned_url, expiration)
    local presigned_url_cache = ngx.shared.presigned_url_cache
    if check_verified_presigned_url(presigned_url_cache, presigned_url) then
        -- Let NGINX proceed, the presigned URL has not expired.
        return
    end

    with_lock(function()
        -- Checks the cache again under lock. It may be already populated.
        if check_verified_presigned_url(presigned_url_cache, presigned_url) then
            return
        end
        local status = access_upstream_resource()
        if status == ngx.HTTP_OK then
            cache_verified_presigned_url(presigned_url_cache, presigned_url, expiration)
        end
    end)
end

local function is_public_url()
    -- Check if the request doesn't contain an Authorization header:
    -- https://docs.aws.amazon.com/en_us/AmazonS3/latest/API/sigv4-auth-using-authorization-header.html
    if ngx.req.get_headers()["Authorization"] then return false end

    -- Double check if this may be a presigned URL.
    if ngx.req.get_uri_args()["X-Amz-Signature"] then return false end

    return true
end

local function handle_normal_get_request()
    if is_public_url() then
        -- For public URLs, let NGINX core handle the request (e.g., serve from cache).
        return
    else
        access_upstream_resource()
    end
end

local function handle_request()
    -- Bail out as Access Checks are only relevant when content is cached.
    if is_cache_disabled() then return false end

    --  Determines the request type and handles it accordingly.
    if ngx.req.get_method() == "HEAD" then
        local status = check_head_auth_cache()
        if not status then
            with_lock(check_and_cache_head_auth)
        end
    elseif ngx.req.get_method() == "GET" then
        local presigned_url, expiration = try_get_presigned_url_expiration()
        if presigned_url then
            handle_presigned_url_request(presigned_url, expiration)
        else
            handle_normal_get_request()
        end
    end
    return true
end

-- Main execution.
if _G._UNIT_TEST == true then
    -- Provide testable functions.
    return {
        handle_request = handle_request,
        is_cache_disabled = is_cache_disabled
    }
else
    -- Execute immediately, as this module is not loaded within
    -- a unit testing context.
    handle_request()
end
