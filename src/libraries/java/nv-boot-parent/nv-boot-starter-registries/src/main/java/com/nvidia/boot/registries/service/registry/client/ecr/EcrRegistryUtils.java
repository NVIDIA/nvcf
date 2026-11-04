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

package com.nvidia.boot.registries.service.registry.client.ecr;

import static com.nvidia.boot.registries.service.registry.RegistryMapperService.JSON_MAPPER;
import static org.springframework.http.HttpStatus.BAD_REQUEST;
import static org.springframework.http.HttpStatus.FORBIDDEN;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.registries.service.registry.client.ecr.dto.EcrArtifactComponents;
import io.micrometer.common.util.StringUtils;
import java.time.Duration;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import java.util.stream.Collectors;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.MediaType;
import org.springframework.http.codec.json.JacksonJsonDecoder;
import org.springframework.http.codec.json.JacksonJsonEncoder;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;
import software.amazon.awssdk.regions.Region;

@Slf4j
@UtilityClass
public class EcrRegistryUtils {

    public static final String AMZ_CONTENT_TYPE = "application/x-amz-json-1.1";
    public static final String ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME = "dkr.ecr.amazonaws.com";
    public static final String ECR_PUBLIC_REGISTRY_HOSTNAME = "public.ecr.aws";
    public static final String ECR_PUBLIC_REGISTRY_BASE_URI =
            "https://api.ecr-public.us-east-1.amazonaws.com";
    // Pattern for ECR private hostname: {aws-account-id}.dkr.ecr.{region}.amazonaws.com
    public static final Pattern ECR_PRIVATE_HOSTNAME_PATTERN =
            buildEcrPrivateHostnamePattern();
    // Pattern for parsing ECR private hostname with named region group
    public static final Pattern ECR_PRIVATE_HOSTNAME_REGION_PATTERN = 
            Pattern.compile("^\\d{12}\\.dkr\\.ecr\\.(?<region>[a-z0-9-]+)\\.amazonaws\\.com$");

    private static final JacksonJsonDecoder JSON_DECODER_AMZ =
            new JacksonJsonDecoder(JSON_MAPPER,
                                   MediaType.APPLICATION_JSON,
                                   MediaType.parseMediaType(AMZ_CONTENT_TYPE));
    private static final JacksonJsonEncoder JSON_ENCODER_AMZ =
            new JacksonJsonEncoder(JSON_MAPPER,
                                   MediaType.APPLICATION_JSON,
                                   MediaType.parseMediaType(AMZ_CONTENT_TYPE));
    private static final String DEFAULT_TAG = "latest";

    // Constants for error messages.
    private static final String MESG_EMPTY_ARTIFACT_URL =
            "ECR artifact URL cannot be null or empty";
    private static final String MESG_INVALID_ECR_ARTIFACT_URL_FORMAT =
            "Invalid ECR artifact URL '%s' format";
    private static final String MESG_EMPTY_ECR_API_BASE_URL =
            "ECR registry base url cannot be null or empty";
    private static final String MESG_INVALID_ECR_API_BASE_URL =
            "Invalid ECR registry base url '%s'";
    private static final String MESG_MISSING_REGION_ECR_API_BASE_URL =
            "Missing region for ECR registry base url '%s'";
    private static final String MESG_ECR_5XX_RESPONSE =
            "ECR response with 5xx error %s";
    private static final String MESG_ECR_RESPONSE_STATUS_CODE =
            "ECR response status code '%d'";
    private static final String INVALID_ECR_PRIVATE_HOSTNAME_FORMAT =
            "Invalid ECR private hostname format";

    public static WebClient.Builder getEcrWebClientBuilder(WebClient.Builder webClientBuilder,
                                                            Duration timeout) {
        return webClientBuilder
                .codecs(configurer -> {
                    configurer.defaultCodecs().jacksonJsonDecoder(JSON_DECODER_AMZ);
                    configurer.defaultCodecs().jacksonJsonEncoder(JSON_ENCODER_AMZ);
                })
                .defaultStatusHandler(HttpStatusCode::is4xxClientError,
                                      EcrRegistryUtils::get4xxException)
                .defaultStatusHandler(HttpStatusCode::is5xxServerError,
                                      EcrRegistryUtils::get5xxException)
                .filter((request, next) -> next.exchange(request).timeout(timeout));
    }

    public static EcrArtifactComponents parsePrivateArtifactUrl(
            String artifactUrl,
            Pattern pattern) {
        var matcher = validateArtifactUrl(artifactUrl, pattern);
        var registryId = matcher.group("registryId");
        var region = matcher.group("region");
        var repository = matcher.group("repository");
        var tag = matcher.group("tag");
        var digest = matcher.group("digest");

        if (StringUtils.isEmpty(tag) && StringUtils.isEmpty(digest)) {
            tag = DEFAULT_TAG;
        }

        return new EcrArtifactComponents(registryId, region, repository, tag, digest);
    }

    public static EcrArtifactComponents parsePublicArtifactUrl(
            String artifactUrl,
            Pattern pattern) {
        var matcher = validateArtifactUrl(artifactUrl, pattern);
        var repository = matcher.group("repository");
        var tag = matcher.group("tag");
        var digest = matcher.group("digest");

        if (StringUtils.isEmpty(tag) && StringUtils.isEmpty(digest)) {
            tag = DEFAULT_TAG;
        }

        return new EcrArtifactComponents(null, null, repository, tag, digest);
    }

    private static Matcher validateArtifactUrl(
            String artifactUrl,
            Pattern pattern) {

        if (StringUtils.isEmpty(artifactUrl)) {
            log.error(MESG_EMPTY_ARTIFACT_URL);
            throw new BadRequestException(MESG_EMPTY_ARTIFACT_URL);
        }

        var matcher = pattern.matcher(artifactUrl);
        if (!matcher.matches()) {
            var mesg = MESG_INVALID_ECR_ARTIFACT_URL_FORMAT.formatted(artifactUrl);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        return matcher;
    }

    // expected final format: https://ecr.{region}.amazonaws.com
    public static String toBaseUrlForEcrPrivateRegistry(String baseUrl, String region) {
        if (StringUtils.isEmpty(baseUrl)) {
            log.error(MESG_EMPTY_ECR_API_BASE_URL);
            throw new IllegalArgumentException(MESG_EMPTY_ECR_API_BASE_URL);
        }

        if (StringUtils.isEmpty(region)) {
            var mesg = MESG_MISSING_REGION_ECR_API_BASE_URL.formatted(baseUrl);
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
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
            return baseUrl.replace("-ecr", "");
        }

        if (baseUrl.contains(ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME)) {
            return "https://ecr.%s.amazonaws.com".formatted(region);
        } else {
            var mesg = MESG_INVALID_ECR_API_BASE_URL.formatted(baseUrl);
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }
    }

    public static String toBaseUrlForEcrPublicRegistry(String baseUrl) {
        if (StringUtils.isEmpty(baseUrl)) {
            log.error(MESG_EMPTY_ECR_API_BASE_URL);
            throw new IllegalArgumentException(MESG_EMPTY_ECR_API_BASE_URL);
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
            return baseUrl.replace("-ecr-public", "");
        }

        if (baseUrl.contains(ECR_PUBLIC_REGISTRY_HOSTNAME)) {
            return ECR_PUBLIC_REGISTRY_BASE_URI;
        } else {
            var mesg = MESG_INVALID_ECR_API_BASE_URL.formatted(baseUrl);
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }
    }

    private static Mono<BootResponseException> get4xxException(ClientResponse response) {
        var status = response.statusCode();
        var msg = MESG_ECR_RESPONSE_STATUS_CODE.formatted(response.statusCode().value());
        log.error(msg);

        if (status.isSameCodeAs(FORBIDDEN)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Forbidden")
                    .flatMap(body -> Mono.error(new ForbiddenException(body)));
        }

        if (status.isSameCodeAs(BAD_REQUEST)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Bad Request")
                    .flatMap(body -> Mono.error(new BadRequestException(body)));
        }

        return response.bodyToMono(String.class)
                .defaultIfEmpty("Bad Request")
                .flatMap(body -> Mono.error(new BadRequestException(body)));
    }

    private static Mono<BootResponseException> get5xxException(ClientResponse response) {
        var errorMsg = MESG_ECR_5XX_RESPONSE.formatted(response.statusCode());
        log.error(errorMsg);

        return response.bodyToMono(String.class)
                .switchIfEmpty(Mono.error(new UpstreamException(errorMsg)))
                .flatMap(body -> {
                    var mesg = errorMsg + " - " + body;
                    log.error(mesg);
                    return Mono.error(new UpstreamException(mesg));
                });
    }

    private static Pattern buildEcrPrivateHostnamePattern() {
        String regionsAlternation = Region.regions().stream()
                .map(Region::id)
                .map(Pattern::quote)  // Escape special regex characters in region names
                .collect(Collectors.joining("|"));

        // Build pattern: {aws-account-id}.dkr.ecr.{region}.amazonaws.com
        String patternString = String.format(
                "\\d{12}\\.dkr\\.ecr\\.(%s)\\.amazonaws\\.com",
                regionsAlternation
        );

        return Pattern.compile(patternString);
    }

    /**
     * Parses the AWS region from an ECR private hostname.
     *
     * @param hostname The ECR private hostname (format: {account-id}.dkr.ecr.{region}.amazonaws.com)
     * @return The AWS region extracted from the hostname
     * @throws BadRequestException if the hostname format is invalid
     */
    public static String parseRegionFromEcrPrivateHostname(String hostname) {
        var matcher = ECR_PRIVATE_HOSTNAME_REGION_PATTERN.matcher(hostname);
        
        if (!matcher.matches()) {
            throw new BadRequestException(INVALID_ECR_PRIVATE_HOSTNAME_FORMAT);
        }
        
        return matcher.group("region");
    }
}
