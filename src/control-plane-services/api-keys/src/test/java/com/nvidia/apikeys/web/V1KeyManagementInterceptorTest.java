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

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.config.exceptions.GoneException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.http.HttpStatus;
import org.springframework.web.method.HandlerMethod;

class V1KeyManagementInterceptorTest {

    private final HttpServletRequest request = mock(HttpServletRequest.class);
    private final HttpServletResponse response = mock(HttpServletResponse.class);

    @ParameterizedTest
    @ValueSource(classes = {
            UserOwnedKeysController.class, UserAdminController.class, AdminController.class})
    void v1KeyManagementControllersAreRejectedWhenDisabled(Class<?> controller) {
        var interceptor = interceptor(false);

        assertThatThrownBy(() -> interceptor.preHandle(request, response, handler(controller)))
                .isInstanceOfSatisfying(GoneException.class, e -> {
                    assertThat(e.getStatusCode()).isEqualTo(HttpStatus.GONE);
                    assertThat(e.getMessage()).contains(
                            V1KeyManagementInterceptor.MESG_V1_KEY_MANAGEMENT_DISABLED);
                });
    }

    @ParameterizedTest
    @ValueSource(classes = {
            IntrospectionController.class, AuthzController.class, ServicesController.class,
            KeyOwnerKeysController.class})
    void otherControllersStayAvailableWhenDisabled(Class<?> controller) {
        assertThat(interceptor(false).preHandle(request, response, handler(controller))).isTrue();
    }

    @Test
    void v1KeyManagementControllersAreAllowedWhenEnabled() {
        assertThat(interceptor(true).preHandle(
                request, response, handler(UserOwnedKeysController.class))).isTrue();
    }

    @Test
    void nonControllerHandlersAreAllowedWhenDisabled() {
        assertThat(interceptor(false).preHandle(request, response, new Object())).isTrue();
    }

    private static V1KeyManagementInterceptor interceptor(boolean v1KeyManagementEnabled) {
        return new V1KeyManagementInterceptor(
                NakProperties.builder().v1KeyManagementEnabled(v1KeyManagementEnabled).build());
    }

    @SuppressWarnings({"unchecked", "rawtypes"})
    private static HandlerMethod handler(Class<?> controller) {
        var handler = mock(HandlerMethod.class);
        when(handler.getBeanType()).thenReturn((Class) controller);
        when(handler.getMethod()).thenReturn(controller.getDeclaredMethods()[0]);
        return handler;
    }
}
