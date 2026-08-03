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

package com.nvidia.boot.observability.tracing.client;

import java.util.regex.Pattern;

/**
 * Shared helpers for HTTP client observation conventions (blocking + reactive).
 */
final class OtelClientObservationConventionSupport {

    private static final Pattern PATTERN_BEFORE_PATH = Pattern.compile("^https?://[^/]+");

    private OtelClientObservationConventionSupport() {}

    /**
     * Matches Spring's {@code DefaultClientRequestObservationConvention#extractPath}: strips scheme and
     * authority, normalizes to a path starting with {@code /}.
     */
    static String extractPathFromUriTemplate(String uriTemplate) {
        if (uriTemplate == null || uriTemplate.isBlank()) {
            return "";
        }
        var path = PATTERN_BEFORE_PATH.matcher(uriTemplate).replaceFirst("");
        if (path.startsWith("/")) {
            return path;
        }
        return "/" + path;
    }

    static String statusCodeFamily(int statusCode) {
        if (statusCode >= 100 && statusCode < 200) {
            return "1xx";
        }
        if (statusCode >= 200 && statusCode < 300) {
            return "2xx";
        }
        if (statusCode >= 300 && statusCode < 400) {
            return "3xx";
        }
        if (statusCode >= 400 && statusCode < 500) {
            return "4xx";
        }
        if (statusCode >= 500 && statusCode < 600) {
            return "5xx";
        }
        return "other";
    }
}
