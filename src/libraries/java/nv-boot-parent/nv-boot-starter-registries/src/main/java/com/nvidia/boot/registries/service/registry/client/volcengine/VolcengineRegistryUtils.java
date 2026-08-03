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

package com.nvidia.boot.registries.service.registry.client.volcengine;

import static com.nvidia.boot.registries.service.registry.RegistryMapperService.JSON_MAPPER;
import static org.springframework.http.HttpStatus.CONFLICT;
import static org.springframework.http.HttpStatus.FORBIDDEN;
import static org.springframework.http.HttpStatus.NOT_FOUND;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineArtifactRegistryStubService.ListTagsRequest;
import com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineArtifactRegistryStubService.TagFilter;
import com.nvidia.boot.registries.service.registry.client.volcengine.dto.VolcengineArtifactComponents;
import com.nvidia.boot.registries.service.registry.client.volcengine.dto.VolcengineArtifactType;
import com.nvidia.boot.registries.service.registry.client.volcengine.dto.VolcengineRegistryInfo;
import java.time.Duration;
import java.util.List;
import java.util.regex.Pattern;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.MediaType;
import org.springframework.http.codec.json.JacksonJsonDecoder;
import org.springframework.http.codec.json.JacksonJsonEncoder;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;

@Slf4j
@UtilityClass
public class VolcengineRegistryUtils {

    public static final String VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME = "cr.volces.com";
    public static final String VOLCENGINE_REGISTRY_BASE_URI = "https://open.volcengineapi.com";

    // Valid VolcEngine regions
    // Reference: https://www.volcengine.com/docs/6405/73694
    public static final List<String> VALID_VOLCENGINE_REGIONS = List.of(
            "cn-beijing",
            "cn-shanghai",
            "cn-guangzhou",
            "cn-hangzhou",
            "cn-hongkong",
            "ap-southeast-1",
            "ap-southeast-3"
    );

    // Pattern for VolcEngine hostname: {registry-name}-{region}.cr.volces.com
    public static final Pattern VOLCENGINE_HOSTNAME_PATTERN = buildVolcengineHostnamePattern();

    // Pattern for parsing registry and region from hostname
    public static final Pattern VOLCENGINE_HOSTNAME_PARSE_PATTERN = Pattern.compile(
            "^(?<registry>[\\w-]+)-(?<region>(?:cn|ap)-[\\w-]+)\\.cr\\.volces\\.com$"
    );

    private static final JacksonJsonDecoder JSON_DECODER =
            new JacksonJsonDecoder(JSON_MAPPER, MediaType.APPLICATION_JSON);
    private static final JacksonJsonEncoder JSON_ENCODER =
            new JacksonJsonEncoder(JSON_MAPPER, MediaType.APPLICATION_JSON);
    private static final String DEFAULT_TAG = "latest";

    // Constants for error messages.
    private static final String MESG_EMPTY_ARTIFACT_URL =
            "Volcengine registry artifact URL cannot be null or empty";
    private static final String INVALID_VOLCENGINE_ARTIFACT_URL_FORMAT =
            "Invalid Volcengine artifact URL '%s' format";
    private static final String MESG_EMPTY_VOLCENGINE_API_BASE_URL =
            "Volcengine registry base url cannot be null or empty";
    private static final String MESG_VOLCENGINE_5XX_RESPONSE =
            "Volcengine response with 5xx error %s";
    private static final String MESG_VOLCENGINE_RESPONSE_STATUS_CODE =
            "Volcengine response status code '%d'";
    private static final String MESG_VOLCENGINE_EMPTY_HOSTNAME =
            "Volcengine hostname cannot be null or empty";
    private static final String INVALID_VOLCENGINE_HOSTNAME_FORMAT =
            "Invalid Volcengine hostname format: %s";

    private static Pattern buildVolcengineHostnamePattern() {
        String regionsAlternation = VALID_VOLCENGINE_REGIONS.stream()
                .map(Pattern::quote)
                .collect(java.util.stream.Collectors.joining("|"));

        // Build pattern: {registry-name}-{region}.cr.volces.com
        String patternString = String.format(
                "[\\w-]+-(%s)\\.cr\\.volces\\.com",
                regionsAlternation
        );

        return Pattern.compile(patternString);
    }

    public static WebClient createVolcengineWebClient(WebClient.Builder webClientBuilder,
                                                      String baseUrl,
                                                      Duration timeout) {
        return webClientBuilder
                .baseUrl(baseUrl)
                .codecs(configurer -> {
                    configurer.defaultCodecs().jacksonJsonDecoder(JSON_DECODER);
                    configurer.defaultCodecs().jacksonJsonEncoder(JSON_ENCODER);
                })
                .defaultStatusHandler(HttpStatusCode::is4xxClientError,
                                      VolcengineRegistryUtils::get4xxException)
                .defaultStatusHandler(HttpStatusCode::is5xxServerError,
                                      VolcengineRegistryUtils::get5xxException)
                .filter((request, next)
                                -> next.exchange(request).timeout(timeout))
                .build();
    }

    public static VolcengineArtifactComponents parseArtifactUrl(
            String artifactUrl,
            Pattern pattern,
            VolcengineArtifactType artifactType) {
        if (StringUtils.isEmpty(artifactUrl)) {
            log.error(MESG_EMPTY_ARTIFACT_URL);
            throw new BadRequestException(MESG_EMPTY_ARTIFACT_URL);
        }

        var matcher = pattern.matcher(artifactUrl);
        if (!matcher.matches()) {
            var mesg = INVALID_VOLCENGINE_ARTIFACT_URL_FORMAT.formatted(artifactUrl);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var registry = matcher.group("registry");
        var region = matcher.group("region");
        var namespace = matcher.group("namespace");
        var repository = matcher.group("repository");
        var tag = matcher.group("tag");

        if (StringUtils.isEmpty(tag)) {
            tag = DEFAULT_TAG;
        }

        return new VolcengineArtifactComponents(registry, region, namespace, repository,
                                                tag, null, artifactType);
    }

    public static String toBaseUrl(String baseUrl) {
        if (StringUtils.isEmpty(baseUrl)) {
            log.error(MESG_EMPTY_VOLCENGINE_API_BASE_URL);
            throw new IllegalArgumentException(MESG_EMPTY_VOLCENGINE_API_BASE_URL);
        }

        // Design for 3rd Party Registry requires hostnames to be unique. However, when
        // integration tests involving multiple registries are being executed, this
        // becomes an issue as all the registries use "localhost" as the hostname in the
        // baseUrl. To make the hostnames unique in the application-test.yaml files of
        // apps such as NVCF API and NVCT API, we use localhost-<registry-key>:<port>
        // as the baseUrl. For example, localhost-ngc:<port>, localhost-docker:<port>,
        // etc. When using the baseUrl, we remove the `-<registry-key>` part so that
        // the client can communicate with the registry-specific mock server.
        if (baseUrl.contains("localhost")) {
            return baseUrl.replace("-volcengine", "");
        }

        if (baseUrl.contains(VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME)) {
            return VOLCENGINE_REGISTRY_BASE_URI;
        } else {
            var mesg = INVALID_VOLCENGINE_ARTIFACT_URL_FORMAT.formatted(baseUrl);
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }
    }

    public static ListTagsRequest constructListTagsRequest(
            VolcengineArtifactComponents components) {

        var filterBuilder = TagFilter.builder()
                .types(List.of(components.type().toString()))
                .names(List.of(components.tag()));

        return ListTagsRequest.builder()
                .registry(components.registry())
                .namespace(components.namespace())
                .repository(components.repository())
                .filter(filterBuilder.build())
                .build();
    }

    public static VolcengineRegistryInfo parseHostname(String hostname) {
        if (StringUtils.isEmpty(hostname)) {
            log.error(MESG_VOLCENGINE_EMPTY_HOSTNAME);
            throw new BadRequestException(MESG_VOLCENGINE_EMPTY_HOSTNAME);
        }

        var matcher = VOLCENGINE_HOSTNAME_PARSE_PATTERN.matcher(hostname);
        if (!matcher.matches()) {
            var mesg = String.format(INVALID_VOLCENGINE_HOSTNAME_FORMAT, hostname);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        return new VolcengineRegistryInfo(
                matcher.group("registry"),
                matcher.group("region")
        );
    }

    private static Mono<BootResponseException> get4xxException(ClientResponse response) {
        var status = response.statusCode();
        var msg = MESG_VOLCENGINE_RESPONSE_STATUS_CODE.formatted(response.statusCode().value());
        log.error(msg);

        if (status.isSameCodeAs(FORBIDDEN)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Forbidden")
                    .flatMap(body -> Mono.error(new ForbiddenException(body)));
        }

        if (status.isSameCodeAs(NOT_FOUND)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Not Found")
                    .flatMap(body -> Mono.error(new NotFoundException(body)));
        }

        if (status.isSameCodeAs(CONFLICT)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Conflict")
                    .flatMap(body -> Mono.error(new ConflictException(body)));
        }

        return response.bodyToMono(String.class)
                .defaultIfEmpty("Bad Request")
                .flatMap(body -> Mono.error(new BadRequestException(body)));
    }

    private static Mono<BootResponseException> get5xxException(ClientResponse response) {
        var errorMsg = MESG_VOLCENGINE_5XX_RESPONSE.formatted(response.statusCode());
        log.error(errorMsg);

        return response.bodyToMono(String.class)
                .switchIfEmpty(Mono.error(new UpstreamException(errorMsg)))
                .flatMap(body -> {
                    var mesg = errorMsg + " - " + body;
                    log.error(mesg);
                    return Mono.error(new UpstreamException(mesg));
                });
    }
}
