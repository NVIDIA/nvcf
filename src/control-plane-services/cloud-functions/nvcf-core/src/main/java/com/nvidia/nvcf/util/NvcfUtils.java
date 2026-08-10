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
package com.nvidia.nvcf.util;

import static com.nvidia.nvcf.util.NvcfConstants.HTTP_METHOD;
import static com.nvidia.nvcf.util.NvcfConstants.REMOTE_ADDRESS;
import static com.nvidia.nvcf.util.NvcfConstants.REQUEST_URI;
import static com.nvidia.nvcf.util.NvcfConstants.UNKNOWN;

import com.nvidia.boot.exceptions.BadRequestException;
import io.micrometer.tracing.Span;
import io.micrometer.tracing.Tracer;
import jakarta.annotation.Nonnull;
import jakarta.annotation.Nullable;
import jakarta.servlet.http.HttpServletRequest;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Paths;
import java.util.Collection;
import java.util.Collections;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.Data;
import lombok.NonNull;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.ProblemDetail;
import org.springframework.util.CollectionUtils;
import software.amazon.awssdk.auth.credentials.AwsSessionCredentials;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@UtilityClass
public final class NvcfUtils {

    private static final String MESG_CANNOT_READ_CREDS = "Cannot read credentials from JSON file";

    private static final String MESG_INVALID_KEY = "key should not empty/null";
    private static final String MESG_FUNCTION_NOT_IN_ACCOUNT =
            "Function '%s': Not found for account '%s'";
    private static final String MESG_FUNCTION_VERSION_NOT_IN_ACCOUNT =
            "Function id '%s', version '%s': Not found for account '%s'";
    private static final String MISSING_PROBLEM_DETAILS_RESPONSE =
            "Missing ProblemDetails response from %s";
    private static final String INVALID_PROBLEM_DETAILS_RESPONSE =
            "Invalid ProblemDetails response from {} - '{}'";
    private static final String MESG_INVALID_UUID =
            "Function id '%s', version id '%s': invalid %s '%s'";

    private static final String PATH_AWS_STS_SECRETS_JSON =
            "vault-agent/secrets/aws-sts-creds.json";
    private static final String DUMMY_LOCAL_LOCALSTATCK_CREDS =
            """
                    {
                      "aws": {
                        "accessKey": "dummy-access-key",
                        "secretKey": "dummy-secret-key",
                        "sessionToken": "dummy-session-token",
                        "arn": "arn:aws:sts::dummy-role"
                      }
                    }
            """;
    private static final JsonMapper JSON_MAPPER = new JsonMapper();

    public static AwsSessionCredentials getCredentialsForLocalStack() {
        return getAwsCredentialsUsingJson(DUMMY_LOCAL_LOCALSTATCK_CREDS);
    }

    public static AwsSessionCredentials getStsCredentialsFromVault() {
        return getAwsCredentialsUsingPath(PATH_AWS_STS_SECRETS_JSON);
    }

    // AWS Credentials used by NVCF when running locally.
    @Data
    static class AwsCredsFile {

        @Data
        static class Aws {

            private String accessKey;
            private String secretKey;
            private String sessionToken;
            private String arn;
        }

        private Aws aws;
    }

    private static AwsSessionCredentials getAwsCredentialsUsingPath(String path) {
        try (var reader = Files.newBufferedReader(Paths.get(path))) {
            var credsFile = JSON_MAPPER.readValue(reader, AwsCredsFile.class);
            return getAwsSessionCredentials(credsFile);
        } catch (IOException ex) {
            log.error("{}: {}", MESG_CANNOT_READ_CREDS, ex.getMessage());
            throw new IllegalStateException(MESG_CANNOT_READ_CREDS, ex);
        }
    }

    private static AwsSessionCredentials getAwsCredentialsUsingJson(String json) {
        try {
            var credsFile = JSON_MAPPER.readValue(json, AwsCredsFile.class);
            return getAwsSessionCredentials(credsFile);
        } catch (JacksonException ex) {
            log.error("{}: {}", MESG_CANNOT_READ_CREDS, ex.getMessage());
            throw new IllegalStateException(MESG_CANNOT_READ_CREDS, ex);
        }
    }

    private static AwsSessionCredentials getAwsSessionCredentials(AwsCredsFile awsCredsFile) {
        var aws = awsCredsFile.getAws();
        var accessKey = aws.getAccessKey();
        var secretKey = aws.getSecretKey();
        var sessionToken = aws.getSessionToken();
        return AwsSessionCredentials.create(accessKey, secretKey, sessionToken);
    }

    // Used only for logging redacted keys when creating pre-signed URLs for upload.
    // When creating pre-signed upload URL, the key contains the nca id from the ID
    // token. The passed in key MUST have the following format -- <nca-id>/<uuid>.
    public static String redact(@NonNull String key) {
        if (StringUtils.isBlank(key)) {
            throw new IllegalArgumentException(MESG_INVALID_KEY);
        }

        int indexOfSlash = key.indexOf('/');
        var prefix = key.substring(indexOfSlash);
        return "X".repeat(prefix.length()) + key.substring(indexOfSlash);
    }

    public static Map<String, String> getCustomProperties(@Nullable HttpServletRequest request) {
        if (request == null) {
            // An internal task is updating the state and causing the audit log to
            // be generated.
            return Map.of(REMOTE_ADDRESS, "0.0.0.0");
        }

        var remoteAddress = StringUtils.isNotBlank(request.getRemoteAddr()) ?
                request.getRemoteAddr() : UNKNOWN;

        return Map.of(REQUEST_URI, request.getRequestURI(),
                      REMOTE_ADDRESS, remoteAddress,
                      HTTP_METHOD, request.getMethod());
    }

    public static String functionNotFoundMessage(
            String ncaId,
            UUID functionId,
            @Nullable UUID versionId) {
        if (versionId == null) {
            return MESG_FUNCTION_NOT_IN_ACCOUNT.formatted(functionId, ncaId);
        }
        return MESG_FUNCTION_VERSION_NOT_IN_ACCOUNT.formatted(functionId, versionId, ncaId);
    }

    @Nonnull
    public static Set<String> filterBlankStrings(Collection<String> strings) {
        if (CollectionUtils.isEmpty(strings)) {
            return Collections.emptySet();
        }

        return strings.stream()
                .filter(StringUtils::isNotBlank)
                .collect(Collectors.toSet());
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
        try (var _ = tracer.withSpan(span)) {
            tags.forEach((key, value) -> span.tag(key, String.valueOf(value)));
        } finally {
            span.end();
        }
    }

    public static void addTagsToSpan(Span span, Map<String, Object> tags) {
        if (span != null) {
            tags.forEach((key, value) -> span.tag(key, String.valueOf(value)));
        }
    }

    @Nullable
    public static <T> T inSpan(
            Tracer tracer,
            String spanName,
            Map<String, Object> tags,
            SpanCallback<T> action) {
        var span = tracer.nextSpan();
        span.name(spanName);
        span.start();
        addTagsToSpan(span, tags);

        try (var ignored = tracer.withSpan(span)) {
            return action.apply(span);
        } catch (RuntimeException ex) {
            span.error(ex);
            throw ex;
        } finally {
            span.end();
        }
    }

    @FunctionalInterface
    public interface SpanCallback<T> {

        @Nullable
        T apply(@Nonnull Span span);
    }

    public static void recordExceptionUsingCurrentSpan(Tracer tracer, Throwable throwable) {
        var span = tracer.currentSpan();
        if (span != null) {
            span.error(throwable);
        }
    }

    public static UUID parseUuid(
            String fieldName,
            String value,
            Object functionId,
            Object versionId) {
        try {
            return UUID.fromString(value);
        } catch (IllegalArgumentException ex) {
            var mesg = MESG_INVALID_UUID.formatted(functionId, versionId, fieldName, value);
            log.error(mesg);
            throw new BadRequestException(mesg, ex);
        }
    }
}
