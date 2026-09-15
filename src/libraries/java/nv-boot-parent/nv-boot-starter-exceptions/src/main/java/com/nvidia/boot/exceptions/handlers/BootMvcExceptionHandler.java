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

import static org.springframework.http.HttpStatus.INTERNAL_SERVER_ERROR;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import org.jspecify.annotations.Nullable;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.ProblemDetail;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.AccessDeniedException;
import org.springframework.security.core.AuthenticationException;
import org.springframework.web.ErrorResponseException;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.context.request.WebRequest;
import org.springframework.web.servlet.mvc.method.annotation.ResponseEntityExceptionHandler;

/**
 * Base exception handler provides security mapping + optional OpenTelemetry span recording;
 * apps may extend and override {@link #recordException(Exception)} when needed.
 *
 * <p>
 */
public abstract class BootMvcExceptionHandler extends ResponseEntityExceptionHandler {

    /**
     * Override to record the exception (e.g. on the current OpenTelemetry span).
     * Default is no-op.
     *
     * @param ex the exception that was handled
     */
    protected void recordException(Exception ex) {
        // no-op
    }

    @Override
    protected @Nullable ResponseEntity<Object> handleExceptionInternal(
            Exception ex, @Nullable Object body, HttpHeaders headers,
            HttpStatusCode statusCode, WebRequest request)  {
        recordException(ex);
        return super.handleExceptionInternal(ex, body, headers, statusCode, request);
    }

    @ExceptionHandler(AccessDeniedException.class)
    protected ResponseEntity<Object> handleAccessDenied(
            AccessDeniedException ex,
            WebRequest request) throws Exception {
        var message = ex.getMessage() != null ? ex.getMessage() : "Access denied";
        return super.handleException(new ForbiddenException(message, ex), request);
    }

    @ExceptionHandler(AuthenticationException.class)
    protected ResponseEntity<Object> handleAuthentication(
            AuthenticationException ex,
            WebRequest request) throws Exception {
        var message = ex.getMessage() != null ? ex.getMessage() : "Unauthorized";
        return super.handleException(new UnauthorizedException(message, ex), request);
    }

    @ExceptionHandler(Exception.class)
    protected ResponseEntity<Object> handleUnmapped(
            Exception ex,
            WebRequest request) throws Exception {
        recordException(ex);
        var wrapped =
                new ErrorResponseException(INTERNAL_SERVER_ERROR,
                                           ProblemDetail.forStatusAndDetail(
                                                   INTERNAL_SERVER_ERROR,
                                                   ex.getMessage() != null ? ex.getMessage() :
                                                           "Internal server error"),
                                           ex);
        return super.handleException(wrapped, request);
    }
}
