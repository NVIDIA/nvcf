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

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import jakarta.annotation.Nonnull;
import jakarta.annotation.Nullable;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.ProblemDetail;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.AccessDeniedException;
import org.springframework.security.core.AuthenticationException;
import org.springframework.web.ErrorResponseException;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.reactive.result.method.annotation.ResponseEntityExceptionHandler;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;

@Slf4j
public abstract class BootReactiveExceptionHandler extends ResponseEntityExceptionHandler {

    /**
     * Override to record the exception (e.g. on the current OpenTelemetry span).
     * Default is no-op.
     *
     * @param ex the exception that was handled
     */
    protected void recordException(Exception ex) {
        // no-op
    }

    @Nonnull
    @Override
    protected Mono<ResponseEntity<Object>> handleExceptionInternal(
            @Nonnull Exception ex, @Nullable Object body, @Nullable HttpHeaders headers,
            @Nonnull HttpStatusCode statusCode, @Nonnull ServerWebExchange exchange) {
        recordException(ex);
        return super.handleExceptionInternal(ex, body, headers, statusCode, exchange);
    }

    @ExceptionHandler(AccessDeniedException.class)
    protected Mono<ResponseEntity<Object>> handleException(
            AccessDeniedException ex, ServerWebExchange exchange) {
        return super.handleException(
                new ForbiddenException(ex.getMessage(), ex.getCause()), exchange);
    }

    @ExceptionHandler(AuthenticationException.class)
    protected Mono<ResponseEntity<Object>> handleException(
            AuthenticationException ex, ServerWebExchange exchange) {
        return super.handleException(
                new UnauthorizedException(ex.getMessage(), ex.getCause()), exchange);
    }

    @ExceptionHandler(Exception.class)
    protected Mono<ResponseEntity<Object>> handleExceptionCatchAll(
            Exception ex, ServerWebExchange exchange) {
        return super.handleException(
                new ErrorResponseException(HttpStatus.INTERNAL_SERVER_ERROR,
                                           ProblemDetail.forStatusAndDetail(
                                                   HttpStatus.INTERNAL_SERVER_ERROR,
                                                   ex.getMessage()), ex), exchange);
    }
}
