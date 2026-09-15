/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package com.nvidia.apikeys.web;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.apikeys.caching.CachingKeysLoader;
import com.nvidia.apikeys.caching.IntrospectionKeyOwnerValidator;
import com.nvidia.apikeys.caching.IntrospectionLocalCache;
import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.services.ServicesService;
import com.nvidia.apikeys.utils.TestUtils;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.web.context.request.RequestContextHolder;

public abstract class BaseIntegrationTest {

    @Autowired
    protected TestRestTemplate restTemplate;

    @Autowired
    protected NakProperties nakProperties;

    @Autowired
    protected ServicesService servicesService;

    @Autowired
    protected JsonMapper jsonMapper;

    @Autowired
    protected IntrospectionLocalCache introspectionLocalCache;

    @Autowired
    protected IntrospectionKeyOwnerValidator introspectionKeyOwnerValidator;

    @Autowired
    protected CachingKeysLoader cachingKeysLoader;

    @BeforeAll
    static void beforeAll() {
    }

    @AfterAll
    static void afterAll() {
    }

    @BeforeEach
    public void initBefore() {
        RequestContextHolder.resetRequestAttributes();
    }

    protected void invalidateCaches() {
        introspectionLocalCache.invalidate();
        cachingKeysLoader.invalidate();
        introspectionKeyOwnerValidator.invalidateCaches();
    }

    protected void assertJsonBodyEquals(String expectedJson, String actualBody) {
        try {
            TestUtils.assertJsonEquals(jsonMapper, expectedJson, actualBody);
        } catch (Exception e) {
            throw new AssertionError("JSON bodies are not equal", e);
        }
    }

    protected HttpHeaders addHeaderForServiceAndUser(
            String serviceId, String userId, HttpHeaders headers) {
        if (headers == null) {
            headers = new HttpHeaders();
        }
        headers.setContentType(MediaType.APPLICATION_JSON);
        if (userId != null) {
            headers.add("Key-Owner-Id", userId);
        }
        if (serviceId != null) {
            headers.add("Key-Issuer-Id", serviceId);
        }
        return headers;
    }
}
