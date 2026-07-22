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

package com.nvidia.boot.registries.service.registry.client.ngc;

import java.net.URI;
import java.util.List;
import lombok.Data;
import org.springframework.http.HttpHeaders;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.service.annotation.GetExchange;
import reactor.core.publisher.Mono;

/**
 * HTTP exchange stub for NGC Artifact Registry, backed by {@link
 * org.springframework.web.reactive.function.client.WebClient} via
 * {@link org.springframework.web.service.invoker.HttpServiceProxyFactory}.
 *
 * <p>Return type convention:
 * <ul>
 *   <li>{@code Mono<T>} — used where the caller needs reactive composition (pagination).
 *   <li>Synchronous types ({@code T}, {@code void}) — used where the caller is blocking;
 *       {@code HttpServiceProxyFactory} will subscribe and block the calling thread automatically.
 * </ul>
 */
public interface NgcArtifactRegistryStub {

    /**
     * expected as /v2/org/:org-name(/team/:team-name)?/models/:model-name/:version-id/files
     * OR
     * /v2/org/:org-name(/team/:team-name)?/resources/:resource-name/:version-id/files
     */
    @GetExchange
    Mono<ArtifactRegistryFilesResponse> getArtifactFiles(
            URI url,
            @RequestHeader(HttpHeaders.AUTHORIZATION) String authorization);

    @GetExchange
    ModelMetadataResponse getModelMetadata(
            URI url,
            @RequestHeader(HttpHeaders.AUTHORIZATION) String authorization);

    @GetExchange
    ResourceMetadataResponse getResourceMetadata(
            URI url,
            @RequestHeader(HttpHeaders.AUTHORIZATION) String authorization);

    @GetExchange
    void validateArtifact(
            URI url,
            @RequestHeader(HttpHeaders.AUTHORIZATION) String authorization);

    @Data
    class ArtifactRegistryFilesResponse {

        private PaginationInfo paginationInfo;
        private RequestStatus requestStatus;
        private List<String> urls;
        private List<String> filepath;
    }

    @Data
    class PaginationInfo {

        private int totalPages;
        private int index;
        private int totalResults;
        private String nextPage;
        private int size;
    }

    @Data
    class RequestStatus {

        private String serverID;
        private String statusCode;
        private String statusDescription;
        private String requestID;
    }

    @Data
    class ModelMetadataResponse {

        private ArtifactDetails modelVersion;
    }

    @Data
    class ResourceMetadataResponse {

        private ArtifactDetails recipeVersion;
    }

    @Data
    class ArtifactDetails {

        private long totalSizeInBytes;
    }
}
