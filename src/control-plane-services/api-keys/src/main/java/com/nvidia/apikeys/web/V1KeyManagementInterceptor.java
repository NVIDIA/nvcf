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

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.config.exceptions.GoneException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Component;
import org.springframework.web.method.HandlerMethod;
import org.springframework.web.servlet.HandlerInterceptor;

/**
 * Rejects every request to a {@link V1KeyManagement} controller with 410 when
 * apikeys.v1-key-management-enabled is false. Introspection, authorization evaluation, and
 * service lookups are not marked and stay available.
 */
@Slf4j
@Component
@RequiredArgsConstructor
public class V1KeyManagementInterceptor implements HandlerInterceptor {

    static final String MESG_V1_KEY_MANAGEMENT_DISABLED =
            "v1 key management is disabled, use the /v2/api-keys endpoints";

    private final NakProperties nakProperties;

    @Override
    public boolean preHandle(
            HttpServletRequest request, HttpServletResponse response, Object handler) {
        if (nakProperties.isV1KeyManagementEnabled()
                || !(handler instanceof HandlerMethod method)
                || !method.getBeanType().isAnnotationPresent(V1KeyManagement.class)) {
            return true;
        }
        log.info("Rejected v1 key management request, handler={}.{}",
                 method.getBeanType().getSimpleName(), method.getMethod().getName());
        throw new GoneException(MESG_V1_KEY_MANAGEMENT_DISABLED);
    }
}
