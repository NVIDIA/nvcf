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

import com.nvidia.boot.exceptions.NotFoundException;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpStatus;
import org.springframework.http.ProblemDetail;
import org.springframework.mock.http.server.reactive.MockServerHttpRequest;
import org.springframework.mock.web.server.MockServerWebExchange;
import org.springframework.security.access.AccessDeniedException;
import org.springframework.security.core.AuthenticationException;
import org.springframework.web.server.ServerWebExchange;

class BootReactiveExceptionHandlerTest {

    private TestBootReactiveExceptionHandler handler;
    private ServerWebExchange exchange;

    @BeforeEach
    void setUp() {
        handler = new TestBootReactiveExceptionHandler();
        exchange = MockServerWebExchange.from(MockServerHttpRequest.get("/").build());
    }

    @Test
    void handleAccessDeniedReturnsForbiddenWithProblemDetail() {
        var ex = new AccessDeniedException("denied");
        var response = handler.handleException(ex, exchange).block();
        assertThat(response).isNotNull();
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
        assertThat(response.getBody()).isInstanceOf(ProblemDetail.class);
        var body = (ProblemDetail) response.getBody();
        assertThat(body.getDetail()).isEqualTo("denied");
        assertThat(body.getType()).hasToString("urn:nv-boot:problem-details:forbidden");
    }

    @Test
    void handleAuthenticationReturnsUnauthorizedWithProblemDetail() {
        var ex = new TestAuthenticationException("bad credentials");
        var response = handler.handleException(ex, exchange).block();
        assertThat(response).isNotNull();
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.UNAUTHORIZED);
        assertThat(response.getBody()).isInstanceOf(ProblemDetail.class);
        var body = (ProblemDetail) response.getBody();
        assertThat(body.getDetail()).isEqualTo("bad credentials");
        assertThat(body.getType()).hasToString("urn:nv-boot:problem-details:unauthorized");
    }

    @Test
    void handleExceptionCatchAllReturnsInternalServerError() {
        var ex = new IllegalStateException("unexpected");
        var response = handler.handleExceptionCatchAll(ex, exchange).block();
        assertThat(response).isNotNull();
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.INTERNAL_SERVER_ERROR);
        assertThat(response.getBody()).isInstanceOf(ProblemDetail.class);
        var body = (ProblemDetail) response.getBody();
        assertThat(body.getDetail()).isEqualTo("unexpected");
    }

    @Test
    void handleExceptionInternalCallsRecordException() {
        var ex = new NotFoundException("missing");
        assertThat(handler.handleExceptionInternal(
                ex, ex.getBody(), null, ex.getStatusCode(), exchange).block()).isNotNull();
        assertThat(handler.recordedException).isSameAs(ex);
    }

    private static final class TestBootReactiveExceptionHandler
            extends BootReactiveExceptionHandler {
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
