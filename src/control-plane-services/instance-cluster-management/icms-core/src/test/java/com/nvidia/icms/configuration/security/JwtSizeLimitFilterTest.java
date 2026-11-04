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
package com.nvidia.icms.configuration.security;

import static org.junit.jupiter.api.Assertions.*;

import jakarta.servlet.ServletException;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpHeaders;
import org.springframework.mock.web.MockFilterChain;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.mock.web.MockHttpServletResponse;

class JwtSizeLimitFilterTest {

    private JwtSizeLimitFilter filter;
    private MockHttpServletRequest request;
    private MockHttpServletResponse response;
    private MockFilterChain filterChain;

    @BeforeEach
    void setUp() {
        filter = new JwtSizeLimitFilter();
        request = new MockHttpServletRequest();
        response = new MockHttpServletResponse();
        filterChain = new MockFilterChain();
    }

    @Test
    void rejectsOversizedJWT() throws ServletException, IOException {
        String oversizedToken = "a".repeat(JwtSizeLimitFilter.MAX_JWT_SIZE + 1);
        request.addHeader(HttpHeaders.AUTHORIZATION, "Bearer " + oversizedToken);

        filter.doFilterInternal(request, response, filterChain);

        assertEquals(431, response.getStatus());
        assertTrue(response.getContentAsString().contains("JWT exceeds maximum size"));
        assertNull(filterChain.getRequest(), "Filter chain should not have been invoked");
    }

    @Test
    void rejectsOversizedJWTWithLowercaseBearerPrefix() throws ServletException, IOException {
        String oversizedToken = "a".repeat(JwtSizeLimitFilter.MAX_JWT_SIZE + 1);
        request.addHeader(HttpHeaders.AUTHORIZATION, "bearer " + oversizedToken);

        filter.doFilterInternal(request, response, filterChain);

        assertEquals(431, response.getStatus());
        assertNull(filterChain.getRequest(), "Filter chain should not have been invoked");
    }

    @Test
    void rejectsOversizedJWTWithMixedCaseBearerPrefix() throws ServletException, IOException {
        String oversizedToken = "a".repeat(JwtSizeLimitFilter.MAX_JWT_SIZE + 1);
        request.addHeader(HttpHeaders.AUTHORIZATION, "BeArEr " + oversizedToken);

        filter.doFilterInternal(request, response, filterChain);

        assertEquals(431, response.getStatus());
        assertNull(filterChain.getRequest(), "Filter chain should not have been invoked");
    }

    @Test
    void rejectsJwtWhoseUtf8ByteLengthExceedsLimit() throws ServletException, IOException {
        String token = "a".repeat(JwtSizeLimitFilter.MAX_JWT_SIZE - 1) + "\u00e9";
        request.addHeader(HttpHeaders.AUTHORIZATION, "Bearer " + token);

        filter.doFilterInternal(request, response, filterChain);

        assertEquals(431, response.getStatus());
        assertNull(filterChain.getRequest(), "Filter chain should not have been invoked");
    }

    @Test
    void allowsNormalJWT() throws ServletException, IOException {
        String normalToken = "eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJ0ZXN0In0.signature";
        request.addHeader(HttpHeaders.AUTHORIZATION, "Bearer " + normalToken);

        filter.doFilterInternal(request, response, filterChain);

        assertEquals(200, response.getStatus());
        assertNotNull(filterChain.getRequest(), "Filter chain should have been invoked");
    }

    @Test
    void allowsNonBearerRequest() throws ServletException, IOException {
        // No Authorization header at all
        filter.doFilterInternal(request, response, filterChain);

        assertEquals(200, response.getStatus());
        assertNotNull(filterChain.getRequest(), "Filter chain should have been invoked");
    }

    @Test
    void allowsApiKeyToken() throws ServletException, IOException {
        // ApiKeys start with "nvapi-" and may be large; they should not be size-limited
        String apiKey = "nvapi-" + "x".repeat(3000);
        request.addHeader(HttpHeaders.AUTHORIZATION, "Bearer " + apiKey);

        filter.doFilterInternal(request, response, filterChain);

        assertEquals(200, response.getStatus());
        assertNotNull(filterChain.getRequest(), "Filter chain should have been invoked for api-keys");
    }

    @Test
    void allowsApiKeyWithLowercaseBearerPrefix() throws ServletException, IOException {
        String apiKey = "nvapi-" + "x".repeat(3000);
        request.addHeader(HttpHeaders.AUTHORIZATION, "bearer " + apiKey);

        filter.doFilterInternal(request, response, filterChain);

        assertEquals(200, response.getStatus());
        assertNotNull(filterChain.getRequest(), "Filter chain should have been invoked for api-keys");
    }

    @Test
    void allowsExactlyMaxSizeJWT() throws ServletException, IOException {
        String exactToken = "a".repeat(JwtSizeLimitFilter.MAX_JWT_SIZE);
        request.addHeader(HttpHeaders.AUTHORIZATION, "Bearer " + exactToken);

        filter.doFilterInternal(request, response, filterChain);

        assertEquals(200, response.getStatus());
        assertNotNull(filterChain.getRequest(), "Filter chain should have been invoked for exactly max-size token");
    }

    @Test
    void allowsBasicAuthHeader() throws ServletException, IOException {
        String basicAuthValue = Base64.getEncoder()
                .encodeToString("example-user:example-value".getBytes(StandardCharsets.UTF_8));
        request.addHeader(HttpHeaders.AUTHORIZATION, "Basic " + basicAuthValue);

        filter.doFilterInternal(request, response, filterChain);

        assertEquals(200, response.getStatus());
        assertNotNull(filterChain.getRequest(), "Filter chain should have been invoked for Basic auth");
    }
}
