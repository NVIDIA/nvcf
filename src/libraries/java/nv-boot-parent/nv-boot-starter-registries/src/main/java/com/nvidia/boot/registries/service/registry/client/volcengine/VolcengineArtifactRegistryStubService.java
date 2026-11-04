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

import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineSignatureUtils.ACTION_PARAM;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineSignatureUtils.VERSION_PARAM;

import tools.jackson.databind.PropertyNamingStrategies;
import tools.jackson.databind.annotation.JsonNaming;
import java.util.List;
import lombok.Builder;
import lombok.Data;
import lombok.Value;
import lombok.extern.jackson.Jacksonized;
import org.springframework.http.HttpHeaders;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.service.annotation.PostExchange;

public interface VolcengineArtifactRegistryStubService {

    @PostExchange(value = "/")
    ListTagsResponse listTags(
            @RequestParam(ACTION_PARAM) String action,
            @RequestParam(VERSION_PARAM) String version,
            @RequestHeader(HttpHeaders.AUTHORIZATION) String authorization,
            @RequestHeader(HttpHeaders.CONTENT_TYPE) String contentType,
            @RequestHeader("X-Content-Sha256") String xContentSha256,
            @RequestHeader("X-Date") String xDate,
            @RequestBody ListTagsRequest request
    );

    @PostExchange(value = "/")
    AuthorizationTokenResponse getAuthorizationToken(
            @RequestParam(ACTION_PARAM) String action,
            @RequestParam(VERSION_PARAM) String version,
            @RequestHeader(HttpHeaders.AUTHORIZATION) String authorization,
            @RequestHeader(HttpHeaders.CONTENT_TYPE) String contentType,
            @RequestHeader("X-Content-Sha256") String xContentSha256,
            @RequestHeader("X-Date") String xDate,
            @RequestBody AuthorizationTokenRequest request
    );

    @Builder
    @Data
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class ListTagsRequest {

        private String registry;
        private String namespace;
        private String repository;
        private TagFilter filter;
    }

    @Builder
    @Data
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class TagFilter {

        private List<String> names;
        private List<String> types;
    }

    @Data
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class ListTagsResponse {

        private ResponseMetadata responseMetadata;
        private Result result;
    }

    @Data
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class ResponseMetadata {

        private String requestId;
        private String action;
        private String version;
        private String service;
        private String region;
    }

    @Data
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class Result {

        private String registry;
        private String namespace;
        private String repository;
        private List<Tag> items;
    }

    @Value
    @Jacksonized
    @Builder
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class Tag {

        String name;
        String type;
        String digest;
        List<ImageAttribute> imageAttributes;
        ChartAttribute chartAttribute;
    }

    @Value
    @Jacksonized
    @Builder
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class ImageAttribute {

        String architecture;
        String os;
        String digest;
    }

    @Value
    @Jacksonized
    @Builder
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class ChartAttribute {

        String apiVersion;
        String name;
        String version;
    }

    @Builder
    @Data
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class AuthorizationTokenRequest {

        private String registry;
    }

    @Data
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class AuthorizationTokenResponse {

        private ResponseMetadata responseMetadata;
        private AuthTokenResult result;
    }

    @Data
    @JsonNaming(PropertyNamingStrategies.UpperCamelCaseStrategy.class)
    class AuthTokenResult {

        private String token;
        private String username;
        private String expireTime;
    }
}
