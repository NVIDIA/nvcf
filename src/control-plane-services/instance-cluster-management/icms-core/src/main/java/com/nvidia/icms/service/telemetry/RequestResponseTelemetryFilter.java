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
package com.nvidia.icms.service.telemetry;

import static com.nvidia.icms.util.InstanceServiceUtil.generateRandomUUID;
import static org.apache.hc.core5.http.HttpHeaders.AUTHORIZATION;

import com.google.common.base.Stopwatch;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.JWTParser;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.AuthUtils;
import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import java.io.IOException;
import java.io.UnsupportedEncodingException;
import java.util.Arrays;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.concurrent.TimeUnit;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.apache.commons.lang3.Strings;
import org.jetbrains.annotations.NotNull;
import org.springframework.http.HttpMethod;
import org.springframework.security.oauth2.server.resource.web.BearerTokenResolver;
import org.springframework.security.oauth2.server.resource.web.DefaultBearerTokenResolver;
import org.springframework.stereotype.Component;
import org.springframework.util.AntPathMatcher;
import org.springframework.web.filter.OncePerRequestFilter;
import org.springframework.web.util.ContentCachingResponseWrapper;

/**
 * Filter to optionally send API related telemetry data to a Telemetry Server
 */
@Slf4j
@Component
public class RequestResponseTelemetryFilter extends OncePerRequestFilter {

    private static final String API_KEY_PREFIX = "Bearer nvapi-";

    private static final String CLUSTER_ID_HEADER_KEY = "X-Nv-Cluster-Id";

    private static final String[] excludedEndpoints = {"/v3/openapi", "/health", "/v1/health"};

    private static final Set<String> GET_API_QUERY_PATH =
            new HashSet<>(Arrays.asList("Action=DescribeInstances",
                                        "Action=DescribeSpotInstanceRequests"));

    private static final String GET_PATH = "/v1/si";

    private final TelemetryEventClient telemetryEventClient;

    private final BearerTokenResolver bearerTokenResolver;

    public RequestResponseTelemetryFilter(TelemetryEventClient telemetryEventClient) {
        this.telemetryEventClient = telemetryEventClient;
        this.bearerTokenResolver = new DefaultBearerTokenResolver();
    }

    @Override
    protected boolean shouldNotFilter(@NotNull HttpServletRequest request) {
        return Arrays.stream(excludedEndpoints)
                .anyMatch(e -> new AntPathMatcher().match(e, request.getRequestURI()));
    }

    @Override
    protected void doFilterInternal(
            @NotNull HttpServletRequest request, @NotNull HttpServletResponse response,
            @NotNull FilterChain filterChain)
            throws ServletException, IOException {

        // If telemetry is not enabled then we will avoid extra processing
        if (!telemetryEventClient.isEnabled()) {
            try {
                filterChain.doFilter(request, response);
            } catch (Exception exception) {
                String errMsg = String.format(
                        "Filter processing failed - while doing filter operation - %s ",
                        exception.getMessage());
                sendFilterProcessingFailedEvent(request, errMsg);
                throw exception;
            }
            return;
        }

        Stopwatch stopwatch = Stopwatch.createStarted();

        ContentCachingResponseWrapper contentCachingResponseWrapper =
                new ContentCachingResponseWrapper(response);

        try {
            filterChain.doFilter(request, contentCachingResponseWrapper);

        } catch (Exception exception) {
            String errMsg =
                    String.format("Filter processing failed - while doing filter operation - %s ",
                            exception.getMessage());
            sendFilterProcessingFailedEvent(request, errMsg);
            throw exception;

        } finally {
            try {
                sendApiResponseEvent(request, contentCachingResponseWrapper,
                                     stopwatch.elapsed(TimeUnit.MILLISECONDS));

            } catch (Exception exception) {
                String errMsg =
                        String.format("Filter processing failed - while sending api response - %s",
                                exception.getMessage());
                sendFilterProcessingFailedEvent(request, errMsg);
            } finally {
                try {
                    // Writing back the cached response to "HttpServletResponse"
                    contentCachingResponseWrapper.copyBodyToResponse();
                } catch (Exception exception) {
                    String errMsg = String.format(
                            "Filter processing failed - while writing cached response - %s",
                            exception.getMessage());
                    sendFilterProcessingFailedEvent(request, errMsg);
                }
            }
        }
    }

    private void sendFilterProcessingFailedEvent(HttpServletRequest request,
                                                 String error) {
        Map<String, Object> metaData = new HashMap<>();

        log.error(error);

        // Unique info for request
        String subId = getSubFromAuthToken(request);
        if (subId != null && !subId.isEmpty()) {
            metaData.put(TelemetryEventClient.EventMetaData.SUBJECT_ID.getName(), subId);
        }
        metaData.put(TelemetryEventClient.EventMetaData.UUID.getName(),
                generateRandomUUID());

        // Request info
        metaData.put(TelemetryEventClient.EventMetaData.REQUEST_METHOD.getName(),
                request.getMethod());
        metaData.put(TelemetryEventClient.EventMetaData.REQUEST_URI.getName(),
                request.getRequestURI());
        metaData.put(TelemetryEventClient.EventMetaData.CLIENT_ADDRESS.getName(),
                request.getRemoteAddr());

        // Sending telemetry event
        GenericMetric genericMetric = new GenericMetric()
                .withEventName(Events.FILTER_PROCESSING_FAILED_EVENT.toString())
                .withError(error)
                .withMetadata(metaData);

        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }

    private void sendApiResponseEvent(
            HttpServletRequest request,
            ContentCachingResponseWrapper contentCachingResponseWrapper,
            long elapsed)
            throws UnsupportedEncodingException {
        Map<String, Object> metaData = new HashMap<>();

        // Unique info for request
        String subId = getSubFromAuthToken(request);
        if (subId != null && !subId.isEmpty()) {
            metaData.put(TelemetryEventClient.EventMetaData.SUBJECT_ID.getName(), subId);
        }
        metaData.put(TelemetryEventClient.EventMetaData.UUID.getName(),
                     generateRandomUUID());

        // Request info
        metaData.put(TelemetryEventClient.EventMetaData.REQUEST_METHOD.getName(),
                     request.getMethod());
        metaData.put(TelemetryEventClient.EventMetaData.REQUEST_URI.getName(),
                     request.getRequestURI());
        metaData.put(TelemetryEventClient.EventMetaData.CLIENT_ADDRESS.getName(),
                     request.getRemoteAddr());

        // Response info
        metaData.put(TelemetryEventClient.EventMetaData.API_EXECUTION_TIME.getName(), elapsed);
        metaData.put(TelemetryEventClient.EventMetaData.RESPONSE_STATUS.getName(),
                     contentCachingResponseWrapper.getStatus());

        // Adding response payload for error
        if (!(contentCachingResponseWrapper.getStatus() >= 200 &&
                contentCachingResponseWrapper.getStatus() <= 299)) {
            metaData.put(TelemetryEventClient.EventMetaData.ERROR_RESPONSE.getName(),
                         new String(
                                 contentCachingResponseWrapper.getContentAsByteArray(),
                                 contentCachingResponseWrapper.getCharacterEncoding()
                         ));
        }

        // Sending GET /v1/si?Action=DescribeSpotInstanceRequests and /v1/si?Action=DescribeInstances query params
        if (request.getMethod().equals(HttpMethod.GET.name()) &&
                GET_PATH.equals(request.getRequestURI()) &&
                isGetApiPathParam(request.getQueryString())) {
            metaData.put(TelemetryEventClient.EventMetaData.QUERY_PATH_PARAM.getName(),
                         request.getQueryString());
        }

        GenericMetric genericMetric = new GenericMetric()
                .withEventName(Events.API_RESPONSE_EVENT.toString())
                .withMetadata(metaData);

        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }

    private String getSubFromAuthToken(
            HttpServletRequest request) {
        var authenticationToken = request.getHeader(AUTHORIZATION);

        if (StringUtils.isBlank(authenticationToken)) {
            return "";
        }

        // Cluster ID fetching for ApiKeys
        if (Strings.CI.startsWith(authenticationToken, API_KEY_PREFIX)) {
            return getClusterIdFromApiKey(request);
        } else {
            // Sub fetching for JWT token
            return getSubFromSsaToken(request);
        }
    }

    private boolean isGetApiPathParam(String pathParam) {
        if (!StringUtils.isEmpty(pathParam)) {
            for (String path : GET_API_QUERY_PATH) {
                if (pathParam.contains(path)) {
                    return true;
                }
            }
        }
        return false;
    }

    private String getClusterIdFromApiKey(HttpServletRequest request) {
        try {
            return AuthUtils.getClusterIdFromApiKey();
        } catch (Exception exception) {
            log.warn("Failed to get clusterId from ApiKey, fetching from header, exception {}",
                    exception.getMessage());

            // Fetching from header
            var clusterIdKey = request.getHeader(CLUSTER_ID_HEADER_KEY);
            if (StringUtils.isBlank(clusterIdKey)) {
                log.warn("{} is not present in request having ApiKey. clientIP: {}",
                        CLUSTER_ID_HEADER_KEY, request.getRemoteAddr());
                return "";
            }
            return clusterIdKey;
        }
    }

    private String getSubFromSsaToken(HttpServletRequest request) {
        try {
            String token = bearerTokenResolver.resolve(request);
            JWTClaimsSet jwtClaimsSet = JWTParser
                    .parse(Objects.requireNonNull(token, "Bearer token is empty"))
                    .getJWTClaimsSet();
            return (String) jwtClaimsSet.getClaim("sub");
        } catch (Exception exception) {
            log.warn("Failed to get token claims for Api Response Event - {}",
                    exception.getMessage());
            return "";
        }
    }
}


