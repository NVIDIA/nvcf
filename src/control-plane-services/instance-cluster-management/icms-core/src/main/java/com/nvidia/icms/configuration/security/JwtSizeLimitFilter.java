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

import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import org.springframework.http.HttpHeaders;
import org.springframework.web.filter.OncePerRequestFilter;

/**
 * Rejects JWTs larger than MAX_JWT_SIZE bytes with HTTP 431.
 * Applied before Spring Security's BearerTokenAuthenticationFilter
 * to prevent memory/CPU abuse from oversized tokens.
 */
public class JwtSizeLimitFilter extends OncePerRequestFilter {

    /** Maximum bearer-token / JWT size in bytes, enforced at the authorization
     *  boundary and re-referenced by downstream defense-in-depth checks. */
    public static final int MAX_JWT_SIZE = 2048;
    private static final String BEARER_PREFIX = "Bearer ";
    // ApiKeys tokens are exempt from JWT size limits because they are
    // validated by the existing/corresponding auth resolver which handles its own size constraints.
    private static final String API_KEY_PREFIX = "Bearer nvapi-";

    @Override
    protected void doFilterInternal(HttpServletRequest request, HttpServletResponse response, FilterChain filterChain)
            throws ServletException, IOException {
        String authHeader = request.getHeader(HttpHeaders.AUTHORIZATION);
        if (hasPrefix(authHeader, BEARER_PREFIX) && !hasPrefix(authHeader, API_KEY_PREFIX)) {
            String token = authHeader.substring(BEARER_PREFIX.length());
            int tokenLength = token.getBytes(StandardCharsets.UTF_8).length;
            if (tokenLength > MAX_JWT_SIZE) {
                response.setStatus(431);
                response.setContentType("application/json");
                response.getWriter().write("{\"error\":\"JWT exceeds maximum size of " + MAX_JWT_SIZE + " bytes\"}");
                return;
            }
        }
        filterChain.doFilter(request, response);
    }

    private static boolean hasPrefix(String value, String prefix) {
        return value != null && value.regionMatches(true, 0, prefix, 0, prefix.length());
    }
}
