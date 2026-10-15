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

package com.nvidia.boot.exceptions;

import java.net.URI;
import org.springframework.http.HttpStatus;
import org.springframework.http.ProblemDetail;
import org.springframework.web.ErrorResponseException;

/**
 * Base exception for all nv-boot-exceptions. Extends ErrorResponseException
 * to support RFC 7807 Problem Details with auto-generated type URNs.
 */
public abstract class BootResponseException extends ErrorResponseException {

    private static final String URN_PREFIX = "urn:nv-boot:problem-details:";

    protected BootResponseException(HttpStatus status, String message,
            Class<? extends BootResponseException> type) {
        super(status, createProblemDetail(status, message, type), null);
    }

    protected BootResponseException(HttpStatus status, String message, Throwable cause,
            Class<? extends BootResponseException> type) {
        super(status, createProblemDetail(status, message, type), cause);
    }

    /**
     * Creates a ProblemDetail with status, detail, type URN, and title.
     * The type URN is derived from the exception class name (e.g., NotFoundException → not-found).
     * If message is null or blank, the status reason phrase is used as the detail.
     */
    private static ProblemDetail createProblemDetail(HttpStatus status, String message,
            Class<? extends BootResponseException> type) {
        var detail = (message != null && !message.isBlank()) ? message : status.getReasonPhrase();
        var problemDetail = ProblemDetail.forStatusAndDetail(status, detail);
        var typeSuffix = classToKebabCase(type.getSimpleName());
        problemDetail.setType(URI.create(URN_PREFIX + typeSuffix));
        problemDetail.setTitle(status.getReasonPhrase());
        return problemDetail;
    }

    private static String classToKebabCase(String className) {
        var withoutException = className.endsWith("Exception")
                ? className.substring(0, className.length() - "Exception".length())
                : className;
        return camelToKebab(withoutException).toLowerCase();
    }

    private static String camelToKebab(String input) {
        var out = new StringBuilder(input.length());
        input.codePoints().forEach(c -> {
            if (Character.isUpperCase(c) && !out.isEmpty()) {
                out.append('-');
            }
            out.appendCodePoint(Character.toLowerCase(c));
        });
        return out.toString();
    }
}
