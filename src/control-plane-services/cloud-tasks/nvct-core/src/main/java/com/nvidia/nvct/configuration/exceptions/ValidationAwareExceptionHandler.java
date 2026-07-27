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
package com.nvidia.nvct.configuration.exceptions;

import tools.jackson.databind.JsonNode;
import com.nvidia.boot.exceptions.BootResponseException;
import jakarta.annotation.Nonnull;
import java.util.List;
import org.springframework.context.MessageSourceResolvable;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.ProblemDetail;
import org.springframework.http.ResponseEntity;
import org.jspecify.annotations.Nullable;
import org.springframework.util.ObjectUtils;
import org.springframework.validation.FieldError;
import org.springframework.web.bind.MethodArgumentNotValidException;
import org.springframework.web.bind.annotation.ControllerAdvice;
import org.springframework.web.context.request.WebRequest;
import org.springframework.web.method.annotation.HandlerMethodValidationException;

@Configuration
@ControllerAdvice
public class ValidationAwareExceptionHandler extends ExceptionTracingExceptionHandler {

    private static final int MAX_REJECTED_VALUE_LOG_CHARS = 256;

    private static String toCleanMessage(HandlerMethodValidationException ex) {
        StringBuilder sb = new StringBuilder("Validation failed with ");
        appendError(sb, ex.getAllErrors());
        return sb.toString();
    }

    private static void appendError(
            StringBuilder sb, List<? extends MessageSourceResolvable> errors) {
        for (MessageSourceResolvable error : errors) {
            sb.append('[');
            if (error instanceof FieldError fieldError) {
                sb.append("Field error in object '")
                        .append(fieldError.getObjectName())
                        .append("' on field '")
                        .append(fieldError.getField())
                        .append("': rejected value [")
                        .append(truncateRejectedValueForLog(fieldError.getRejectedValue()))
                        .append("]; ")
                        .append(fieldError.getDefaultMessage());
            } else {
                sb.append(error);
            }
            sb.append("] ");
        }
        sb.deleteCharAt(sb.length() - 1);
    }

    /**
     * Keeps validation problem responses and framework DEBUG logs bounded when rejected values are
     * very large (e.g. secret value length integration tests).
     */
    @Override
    protected ResponseEntity<Object> handleMethodArgumentNotValid(
            MethodArgumentNotValidException ex,
            HttpHeaders headers,
            HttpStatusCode status,
            WebRequest request) {
        var problemDetail =
                ProblemDetail.forStatusAndDetail(status, buildMethodArgumentNotValidDetail(ex));
        return handleExceptionInternal(ex, problemDetail, headers, status, request);
    }

    private static String buildMethodArgumentNotValidDetail(MethodArgumentNotValidException ex) {
        var errors = ex.getBindingResult().getFieldErrors();
        if (errors.isEmpty()) {
            return "Validation failed";
        }
        StringBuilder sb = new StringBuilder("Validation failed: ");
        for (FieldError fe : errors) {
            sb.append("[object=")
                    .append(fe.getObjectName())
                    .append(", field=")
                    .append(fe.getField())
                    .append(", rejected=")
                    .append(truncateRejectedValueForLog(fe.getRejectedValue()))
                    .append(", message=")
                    .append(fe.getDefaultMessage())
                    .append("] ");
        }
        return sb.toString().trim();
    }

    private static String truncateRejectedValueForLog(@Nullable Object rejected) {
        if (rejected == null) {
            return "null";
        }
        String raw;
        if (rejected instanceof JsonNode node) {
            if (node.isString()) {
                raw = node.asString();
            } else {
                raw = node.toString();
            }
        } else if (rejected instanceof CharSequence seq) {
            raw = seq.toString();
        } else {
            raw = ObjectUtils.nullSafeConciseToString(rejected);
        }
        if (raw.length() <= MAX_REJECTED_VALUE_LOG_CHARS) {
            return raw;
        }
        return raw.substring(0, MAX_REJECTED_VALUE_LOG_CHARS)
                + "... (truncated for log/response, total length "
                + raw.length()
                + " chars)";
    }

    @Override
    protected ResponseEntity<Object> handleHandlerMethodValidationException(
            @Nonnull HandlerMethodValidationException ex, @Nonnull HttpHeaders headers,
            @Nonnull HttpStatusCode status, @Nonnull WebRequest request) {
        var problemDetail = ProblemDetail.forStatusAndDetail(status, toCleanMessage(ex));
        return super.handleExceptionInternal(ex, problemDetail, headers, status, request);
    }

    @Nonnull
    @Override
    protected ResponseEntity<Object> handleExceptionInternal(
            @Nonnull Exception ex,
            Object body,
            HttpHeaders headers,
            @Nonnull HttpStatusCode status,
            @Nonnull WebRequest request) {
        Object problemBody = body instanceof ProblemDetail ? body : resolveBody(ex, status);
        return super.handleExceptionInternal(ex, problemBody, headers, status, request);
    }

    private static ProblemDetail resolveBody(Exception ex, HttpStatusCode status) {
        if (ex instanceof BootResponseException bre) {
            return bre.getBody();
        }
        return ProblemDetail.forStatusAndDetail(status, ex.getMessage());
    }
}
