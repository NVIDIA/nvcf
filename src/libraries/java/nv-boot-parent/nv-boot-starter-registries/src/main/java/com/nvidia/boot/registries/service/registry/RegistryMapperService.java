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

package com.nvidia.boot.registries.service.registry;

import static com.nvidia.boot.registries.service.registry.client.acr.AzureRegistryClient.AZURE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PRIVATE_HOSTNAME_PATTERN;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.VOLCENGINE_HOSTNAME_PATTERN;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME;

import com.fasterxml.jackson.annotation.JsonInclude;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.json.JsonMapper;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.dto.AccessKeyCredentials;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.util.Base64;
import java.util.Map;
import java.util.regex.Pattern;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.util.CollectionUtils;
import org.springframework.web.util.UriComponentsBuilder;

@Slf4j
public class RegistryMapperService {

    public static final String HELM_REGISTRY_CANARY_HOSTNAME = "helm.canary.ngc.nvidia.com";
    public static final String CONTAINER_REGISTRY_CANARY_HOSTNAME = "canary.nvcr.io";
    public static final String ARTIFACT_REGISTRY_CANARY_HOSTNAME = "api.canary.ngc.nvidia.com";
    public static final JsonMapper JSON_MAPPER = JsonMapper.builder()
            .changeDefaultPropertyInclusion(v -> v.withValueInclusion(JsonInclude.Include.NON_NULL))
            .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
            .build();

    private static final Pattern LOCALHOST_REGISTRY_PATTERN =
            Pattern.compile("localhost-[^:]+:");
    private static final String ISO8601_BASIC_FORMAT = "yyyyMMdd'T'HHmmss'Z'";
    private static final String SHORT_DATE_FORMAT = "yyyyMMdd";

    private static final String MESG_FAILED_TO_SERIALIZE_OBJECT =
            "Failed serialize object to json: %s";
    private static final String MESG_SECRET_NULL_OR_EMPTY =
            "Secret cannot be null or empty";
    private static final String MESG_INVALID_SECRET_FORMAT =
            "Invalid secret format. Expected format: base64(username:passwd)";
    private static final String MESG_INVALID_BASE64_ENCODING =
            "Invalid base64 encoding in secret: %s";
    private static final String MESG_EMPTY_BASE_URL =
            "Base url cannot be null or empty";

    private String ngcArtifactRegistryHostname;
    private String ngcContainerRegistryHostname;
    private String ngcHelmRegistryHostname;

    public RegistryMapperService(String ngcContainerRegistryHostname,
                                 String ngcArtifactRegistryHostname,
                                 String ngcHelmRegistryHostname) {
        this.ngcContainerRegistryHostname = ngcContainerRegistryHostname;
        this.ngcArtifactRegistryHostname = ngcArtifactRegistryHostname;
        this.ngcHelmRegistryHostname = ngcHelmRegistryHostname;
    }

    public static boolean isCanaryHostname(String hostname) {
        // Handle NGC UI's use of canary hostnames that are just aliased to the prod hostnames.
        return CONTAINER_REGISTRY_CANARY_HOSTNAME.equals(hostname)
                || HELM_REGISTRY_CANARY_HOSTNAME.equals(hostname)
                || ARTIFACT_REGISTRY_CANARY_HOSTNAME.equals(hostname);
    }

    public String toNormalizedHostname(String hostname) {
        // Handle NGC UI's use of canary hostnames that are just aliased to the prod hostnames.
        if (CONTAINER_REGISTRY_CANARY_HOSTNAME.equals(hostname)) {
            return ngcContainerRegistryHostname;
        } else if (HELM_REGISTRY_CANARY_HOSTNAME.equals(hostname)) {
            return ngcHelmRegistryHostname;
        } else if (ARTIFACT_REGISTRY_CANARY_HOSTNAME.equals(hostname)) {
            return ngcArtifactRegistryHostname;
        }

        return hostname;
    }

    public String toNormalizedRecognizedRegistryHostname(String hostname) {

        // Handle NGC UI's use of canary hostnames that are just aliased to the prod hostnames.
        if (CONTAINER_REGISTRY_CANARY_HOSTNAME.equals(hostname)) {
            return ngcContainerRegistryHostname;
        } else if (HELM_REGISTRY_CANARY_HOSTNAME.equals(hostname)) {
            return ngcHelmRegistryHostname;
        } else if (ARTIFACT_REGISTRY_CANARY_HOSTNAME.equals(hostname)) {
            return ngcArtifactRegistryHostname;
        }

        // Map ECR private hostname containing AWS account-id and region to a generic hostname
        // used in the recognized registry configuration.
        if (StringUtils.isNotBlank(hostname)
                && ECR_PRIVATE_HOSTNAME_PATTERN.matcher(hostname).matches()) {
            return ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME;
        }

        // Map VolcEngine registry hostname containing registry id and region to a generic hostname
        // used in the recognized registry configuration.
        if (StringUtils.isNotBlank(hostname)
                && VOLCENGINE_HOSTNAME_PATTERN.matcher(hostname).matches()) {
            return VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME;
        }

        // Map dynamic azure container registry hostname to a generic hostname
        // used in the recognized registry configuration.
        if (StringUtils.isNotBlank(hostname)
                && hostname.contains(AZURE_REGISTRY_GLOBAL_HOSTNAME)) {
            return AZURE_REGISTRY_GLOBAL_HOSTNAME;
        }

        return hostname;
    }

    public String toCanaryHostname(String hostname) {
        // Handle Downstream Spot use of canary hostnames when consuming registry secrets.
        if (ngcContainerRegistryHostname.equals(hostname)) {
            return CONTAINER_REGISTRY_CANARY_HOSTNAME;
        } else if (ngcHelmRegistryHostname.equals(hostname)) {
            return HELM_REGISTRY_CANARY_HOSTNAME;
        } else if (ngcArtifactRegistryHostname.equals(hostname)) {
            return ARTIFACT_REGISTRY_CANARY_HOSTNAME;
        }

        return hostname;
    }

    @VisibleForTesting
    public void updateNgcArtifactRegistryHostname(String newNgcArtifactRegistryHostname) {
        this.ngcArtifactRegistryHostname = newNgcArtifactRegistryHostname;
    }

    @VisibleForTesting
    public void updateNgcHelmRegistryHostname(String newNgcHelmRegistryHostname) {
        this.ngcHelmRegistryHostname = newNgcHelmRegistryHostname;
    }

    @VisibleForTesting
    public void updateNgcContainerRegistryHostname(String newNgcContainerRegistryHostname) {
        this.ngcContainerRegistryHostname = newNgcContainerRegistryHostname;
    }

    public static String toCanonicalRequest(
            String method,
            String canonicalUri,
            String canonicalQueryString,
            Map<String, String> headers,
            String hashedPayload) {
        var canonicalHeaders = new StringBuilder();
        for (Map.Entry<String, String> entry : headers.entrySet()) {
            canonicalHeaders.append(entry.getKey()).append(":").append(entry.getValue())
                    .append("\n");
        }
        var signedHeaders = String.join(";", headers.keySet());
        return method + "\n" +
                canonicalUri + "\n" +
                canonicalQueryString + "\n" +
                canonicalHeaders + "\n" +
                signedHeaders + "\n" +
                hashedPayload;
    }

    public static String toIsoBasicDateFormat(Instant instant) {
        return DateTimeFormatter
                .ofPattern(ISO8601_BASIC_FORMAT)
                .withZone(ZoneOffset.UTC)
                .format(instant);
    }

    public static String toShortDateFormat(Instant instant) {
        return DateTimeFormatter
                .ofPattern(SHORT_DATE_FORMAT)
                .withZone(ZoneOffset.UTC)
                .format(instant);
    }

    public static <T> String toJson(T obj) {
        try {
            return JSON_MAPPER.writeValueAsString(obj);
        } catch (JacksonException e) {
            var mesg = MESG_FAILED_TO_SERIALIZE_OBJECT.formatted(e.getMessage());
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }
    }

    // Expected format: base64(username:passwd)
    public static AccessKeyCredentials toAccessKeyCredentials(
            String base64Secret) {
        if (StringUtils.isEmpty(base64Secret)) {
            log.error(MESG_SECRET_NULL_OR_EMPTY);
            throw new BadRequestException(MESG_SECRET_NULL_OR_EMPTY);
        }

        try {
            var decodedBytes = Base64.getDecoder().decode(base64Secret);
            var decodedSecret = new String(decodedBytes, StandardCharsets.UTF_8);

            var parts = decodedSecret.split(":", 2);
            if (parts.length != 2) {
                log.error(MESG_INVALID_SECRET_FORMAT);
                throw new BadRequestException(MESG_INVALID_SECRET_FORMAT);
            }
            return new AccessKeyCredentials(parts[0], parts[1]);
        } catch (IllegalArgumentException e) {
            var msg = MESG_INVALID_BASE64_ENCODING.formatted(e.getMessage());
            log.error(msg);
            throw new BadRequestException(msg);
        }
    }

    public static String normalizeUrl(String hostname) {
        return hostname.startsWith("http") ? hostname : "https://" + hostname;
    }

    public static String buildUrlWithQueryParams(String baseUrl, Map<String, String> queryParams) {
        if (CollectionUtils.isEmpty(queryParams)) {
            return baseUrl;
        }
        
        var builder = UriComponentsBuilder.fromUriString(baseUrl);
        queryParams.forEach(builder::queryParam);
        return builder.build().toUriString();
    }

    public static String toBaseAuthUrl(String authBaseUrl, String registryHost) {
        return StringUtils.isNotBlank(authBaseUrl) ?
                authBaseUrl : normalizeUrl(registryHost);
    }

    public static String toRegistryBaseUrl(String registryHost, String overrideBaseUrl) {
        if (StringUtils.isBlank(overrideBaseUrl) || !overrideBaseUrl.contains("localhost")) {
            return "https://" + registryHost;
        } else {
            return toRegistryBaseUrl(overrideBaseUrl);
        }
    }

    public static String toRegistryBaseUrl(String baseUrl) {
        if (StringUtils.isEmpty(baseUrl)) {
            throw new IllegalArgumentException(MESG_EMPTY_BASE_URL);
        }

        // Design for 3rd Party Registry requires hostnames to be unique. However, when
        // integration tests involving multiple registries are being executed, this
        // becomes an issue as all the registries use "localhost" as the hostname in the
        // baseUrl. To make the hostnames unique in the application-test.yaml files of
        // apps such as NVCF API and NVCT API, we use localhost-<registry-key>:<port>
        // as the baseUrl. For example, localhost-ngc:<port>, localhost-docker:<port>,
        // etc. When using the baseUrl, we remove the `-<registry-key>` part so that
        // the client can communicate with the registry-specific mock server.
        return baseUrl.contains("localhost-")
                ? LOCALHOST_REGISTRY_PATTERN.matcher(baseUrl).replaceAll("localhost:")
                : baseUrl;
    }
}
