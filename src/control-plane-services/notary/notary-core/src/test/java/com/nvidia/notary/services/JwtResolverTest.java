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
package com.nvidia.notary.services;

import static com.nvidia.notary.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.ForbiddenException;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.jwt.Jwt;

@ExtendWith(MockitoExtension.class)
class JwtResolverTest {

    @InjectMocks
    private JwtResolver jwtResolver;

    @Mock
    private Authentication authenticationMock;

    @Mock
    private Jwt jwtMock;

    @AfterEach
    void cleanup() {
        SecurityContextHolder.clearContext();
    }

    @Test
    void getCallerToken_throwsIfNoAuthentication() {
        SecurityContextHolder.clearContext();
        assertThrowsExceptionWithDetails(
                ForbiddenException.class,
                () -> jwtResolver.getCallerToken(),
                "authentication is not JWT");
    }

    @Test
    void getCallerToken_throwsIfPrincipalIsNull() {
        SecurityContextHolder.getContext().setAuthentication(authenticationMock);
        assertThrowsExceptionWithDetails(
                ForbiddenException.class,
                () -> jwtResolver.getCallerToken(),
                "authentication is not JWT");
    }


    @Test
    void getCallerToken_throwsIfPrincipalIsNotJwt() {
        SecurityContextHolder.getContext().setAuthentication(authenticationMock);
        when(authenticationMock.getPrincipal()).thenReturn("client-id");
        assertThrowsExceptionWithDetails(
                ForbiddenException.class,
                () -> jwtResolver.getCallerToken(),
                "authentication is not JWT");
    }

    @Test
    void getCallerToken_returnsJwt() {
        SecurityContextHolder.getContext().setAuthentication(authenticationMock);
        when(authenticationMock.getPrincipal()).thenReturn(jwtMock);
        assertThat(jwtResolver.getCallerToken()).isEqualTo(jwtMock);
    }
}
