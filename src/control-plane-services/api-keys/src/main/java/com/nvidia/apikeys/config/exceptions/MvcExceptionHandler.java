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

package com.nvidia.apikeys.config.exceptions;

import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.handlers.BootMvcExceptionHandler;
import io.opentelemetry.api.trace.Span;
import jakarta.annotation.Nonnull;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.ProblemDetail;
import org.springframework.http.ResponseEntity;
import org.springframework.http.converter.HttpMessageNotReadableException;
import org.springframework.web.bind.annotation.ControllerAdvice;
import org.springframework.web.context.request.WebRequest;

@Configuration
@ControllerAdvice
public class MvcExceptionHandler extends BootMvcExceptionHandler {

    @Override
    protected void recordException(Exception ex) {
        Span.current().recordException(ex);
    }

    @Nonnull
    @Override
    protected ResponseEntity<Object> handleExceptionInternal(
            @Nonnull Exception ex,
            Object body,
            HttpHeaders headers,
            @Nonnull HttpStatusCode status,
            @Nonnull WebRequest request) {
        var problemDetail = resolveBody(ex, status);
        return super.handleExceptionInternal(ex, problemDetail, headers, status, request);
    }

    @Override
    protected ResponseEntity<Object> handleHttpMessageNotReadable(
            HttpMessageNotReadableException ex, HttpHeaders headers,
            HttpStatusCode status, WebRequest request) {
        ProblemDetail body = this.createProblemDetail(ex, status,
                "Failed to read request:" + ex.getMessage(), null, null, request);
        return this.handleExceptionInternal(ex, body, headers, status, request);
    }

    private static ProblemDetail resolveBody(Exception ex, HttpStatusCode status) {
        if (ex instanceof BootResponseException bre) {
            return bre.getBody();
        }
        return ProblemDetail.forStatusAndDetail(status, ex.getMessage());
    }
}
