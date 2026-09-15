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

package com.nvidia.boot.exceptions.handlers;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;

import com.nvidia.boot.exceptions.NotFoundException;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpStatus;
import org.springframework.http.ProblemDetail;
import org.springframework.security.access.AccessDeniedException;
import org.springframework.security.core.AuthenticationException;
import org.springframework.web.context.request.WebRequest;

class BootMvcExceptionHandlerTest {

    private TestBootExceptionHandler handler;
    private WebRequest webRequest;

    @BeforeEach
    void setUp() {
        handler = new TestBootExceptionHandler();
        webRequest = mock(WebRequest.class);
    }

    @Test
    void handleAccessDeniedReturnsForbiddenWithProblemDetail() throws Exception {
        var ex = new AccessDeniedException("denied");
        var response = handler.handleAccessDenied(ex, webRequest);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
        assertThat(response.getBody()).isInstanceOf(ProblemDetail.class);
        var body = (ProblemDetail) response.getBody();
        assertThat(body.getDetail()).isEqualTo("denied");
        assertThat(body.getType()).hasToString("urn:nv-boot:problem-details:forbidden");
    }

    @Test
    void handleAccessDeniedNullMessageUsesDefault() throws Exception {
        var ex = new AccessDeniedException(null);
        var response = handler.handleAccessDenied(ex, webRequest);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
        var body = (ProblemDetail) response.getBody();
        assertThat(body.getDetail()).isEqualTo("Access denied");
    }

    @Test
    void handleAuthenticationReturnsUnauthorizedWithProblemDetail() throws Exception {
        var ex = new TestAuthenticationException("bad credentials");
        var response = handler.handleAuthentication(ex, webRequest);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.UNAUTHORIZED);
        assertThat(response.getBody()).isInstanceOf(ProblemDetail.class);
        var body = (ProblemDetail) response.getBody();
        assertThat(body.getDetail()).isEqualTo("bad credentials");
        assertThat(body.getType()).hasToString("urn:nv-boot:problem-details:unauthorized");
    }

    @Test
    void handleAuthenticationNullMessageUsesDefault() throws Exception {
        var ex = new TestAuthenticationException(null);
        var response = handler.handleAuthentication(ex, webRequest);
        var body = (ProblemDetail) response.getBody();
        assertThat(body.getDetail()).isEqualTo("Unauthorized");
    }

    @Test
    void handleUnmappedReturnsInternalServerErrorAndRecordsException() throws Exception {
        var ex = new IllegalStateException("unexpected");
        var response = handler.handleUnmapped(ex, webRequest);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.INTERNAL_SERVER_ERROR);
        assertThat(response.getBody()).isInstanceOf(ProblemDetail.class);
        var body = (ProblemDetail) response.getBody();
        assertThat(body.getDetail()).isEqualTo("unexpected");
        // recordException is invoked from handleExceptionInternal with the wrapped exception
        assertThat(handler.recordedException).isInstanceOf(org.springframework.web.ErrorResponseException.class);
        assertThat(handler.recordedException.getCause()).isSameAs(ex);
    }

    @Test
    void handleUnmappedNullMessageUsesDefault() throws Exception {
        var ex = new Exception();
        var response = handler.handleUnmapped(ex, webRequest);
        var body = (ProblemDetail) response.getBody();
        assertThat(body.getDetail()).isEqualTo("Internal server error");
    }

    @Test
    void handleExceptionInternalCallsRecordException() {
        var ex = new NotFoundException("missing");
        handler.handleExceptionInternal(ex, ex.getBody(), null, ex.getStatusCode(), webRequest);
        assertThat(handler.recordedException).isSameAs(ex);
    }

    private static final class TestBootExceptionHandler extends BootMvcExceptionHandler {
        Exception recordedException;

        @Override
        protected void recordException(Exception ex) {
            this.recordedException = ex;
        }
    }

    @SuppressWarnings("serial")
    private static final class TestAuthenticationException extends AuthenticationException {
        TestAuthenticationException(String message) {
            super(message);
        }
    }
}
