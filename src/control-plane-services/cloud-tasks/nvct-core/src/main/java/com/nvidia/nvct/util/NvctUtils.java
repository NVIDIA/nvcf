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
package com.nvidia.nvct.util;

import static com.nvidia.nvct.util.NvctConstants.HTTP_METHOD;
import static com.nvidia.nvct.util.NvctConstants.REMOTE_ADDRESS;
import static com.nvidia.nvct.util.NvctConstants.REQUEST_URI;

import tools.jackson.databind.json.JsonMapper;
import io.micrometer.tracing.Tracer;
import jakarta.annotation.Nullable;
import jakarta.servlet.http.HttpServletRequest;
import java.util.Map;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.ProblemDetail;

@Slf4j
@UtilityClass
public final class NvctUtils {
    private static final String MISSING_PROBLEM_DETAILS_RESPONSE =
            "Missing ProblemDetails response from %s";
    private static final String INVALID_PROBLEM_DETAILS_RESPONSE =
            "Invalid ProblemDetails response from {} - '{}'";

    public static Map<String, String> getCustomProperties(@Nullable HttpServletRequest request) {
        if (request == null) {
            // An internal background thread is updating the state and causing the audit log to
            // be generated.
            return Map.of(REMOTE_ADDRESS, "0.0.0.0");
        }

        return Map.of(REQUEST_URI, request.getRequestURI(),
                      REMOTE_ADDRESS, request.getRemoteAddr(),
                      HTTP_METHOD, request.getMethod());
    }

    // Returns detail from ProblemDetails response.
    public static String getDetailFromProblemDetailsResponse(
            JsonMapper jsonMapper,
            String service,
            String body) {
        if (StringUtils.isBlank(body)) {
            return MISSING_PROBLEM_DETAILS_RESPONSE.formatted(service);
        }

        try {
            var pd = jsonMapper.readValue(body, ProblemDetail.class);
            return ((pd != null) && StringUtils.isNotBlank(pd.getDetail())) ? pd.getDetail() : body;
        } catch (Exception ex) {
            log.warn(INVALID_PROBLEM_DETAILS_RESPONSE, service, ex.getMessage());
            return body; // Return original response body as-is.
        }
    }

    public static void addTagsToCurrentSpan(Tracer tracer, Map<String, Object> tags) {
        var span = tracer.currentSpan();
        if (span != null) {
            tags.forEach((key, value) -> span.tag(key, String.valueOf(value)));
        }
    }

    /**
     * Creates a child span with the given name, adds the tags to it, and ends the span.
     * The child span becomes the current span for the duration of this method. A parent span
     * can have multiple child spans with the same name. Each child span is a separate span
     * with its own span ID and timestamps - the name is just a label for the span type.
     */
    public static void addTagsToChildSpan(
            Tracer tracer,
            Map<String, Object> tags,
            String childSpanName) {
        var span = tracer.nextSpan().name(childSpanName).start();
        try (var unused = tracer.withSpan(span)) { // Replace unused with _(underscore) with Java 25
            tags.forEach((key, value) -> span.tag(key, String.valueOf(value)));
        } finally {
            span.end();
        }
    }

    public static void recordExceptionUsingCurrentSpan(Tracer tracer, Throwable throwable) {
        var span = tracer.currentSpan();
        if (span != null) {
            span.error(throwable);
        }
    }

}
