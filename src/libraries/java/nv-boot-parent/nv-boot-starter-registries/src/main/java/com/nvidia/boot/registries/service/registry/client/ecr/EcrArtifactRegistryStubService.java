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

import java.net.URI;
import java.util.List;
import lombok.Builder;
import lombok.Value;
import lombok.extern.jackson.Jacksonized;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.service.annotation.PostExchange;

public interface EcrArtifactRegistryStubService {

    @PostExchange(value = "/")
    DescribeImagesResponse describePrivateArtifacts(
            URI baseUrl,
            @RequestHeader("Authorization") String authorization,
            @RequestHeader("Content-Type") String contentType,
            @RequestHeader("X-Amz-Target") String xAmzTarget,
            @RequestHeader("X-Amz-Date") String xAmzDate,
            @RequestHeader("X-Amz-Content-Sha256") String xAmzContentSha256,
            @RequestBody DescribeImagesRequest request
    );

    @PostExchange(value = "/")
    DescribeImagesResponse describePublicArtifacts(
            @RequestHeader("Authorization") String authorization,
            @RequestHeader("Content-Type") String contentType,
            @RequestHeader("X-Amz-Target") String xAmzTarget,
            @RequestHeader("X-Amz-Date") String xAmzDate,
            @RequestHeader("X-Amz-Content-Sha256") String xAmzContentSha256,
            @RequestBody DescribeImagesRequest request
    );

    @PostExchange(value = "/")
    PrivateAuthorizationTokenResponse getEcrPrivateAuthorizationToken(
            URI baseUrl,
            @RequestHeader("Authorization") String authorization,
            @RequestHeader("Content-Type") String contentType,
            @RequestHeader("X-Amz-Target") String xAmzTarget,
            @RequestHeader("X-Amz-Date") String xAmzDate,
            @RequestHeader("X-Amz-Content-Sha256") String xAmzContentSha256,
            @RequestBody String request
    );

    @PostExchange(value = "/")
    PublicAuthorizationTokenResponse getEcrPublicAuthorizationToken(
            @RequestHeader("Authorization") String authorization,
            @RequestHeader("Content-Type") String contentType,
            @RequestHeader("X-Amz-Target") String xAmzTarget,
            @RequestHeader("X-Amz-Date") String xAmzDate,
            @RequestHeader("X-Amz-Content-Sha256") String xAmzContentSha256,
            @RequestBody String request
    );

    @Value
    @Jacksonized
    @Builder
    class DescribeImagesRequest {

        String repositoryName;
        String registryId;
        List<ImageId> imageIds;

        @Value
        @Jacksonized
        @Builder
        public static class ImageId {

            String imageTag;
            String imageDigest;
        }
    }

    @Value
    @Jacksonized
    @Builder
    class DescribeImagesResponse {

        List<ImageDetail> imageDetails;

        @Value
        @Jacksonized
        @Builder
        public static class ImageDetail {

            String imageDigest;
            String imageManifestMediaType;
            List<String> imageTags;
            String registryId;
            String repositoryName;
        }
    }

    @Value
    @Jacksonized
    @Builder
    class PrivateAuthorizationTokenResponse {

        List<AuthorizationData> authorizationData;

        @Value
        @Jacksonized
        @Builder
        public static class AuthorizationData {

            String authorizationToken;
            Double expiresAt;
        }
    }

    @Value
    @Jacksonized
    @Builder
    class PublicAuthorizationTokenResponse {

        AuthorizationData authorizationData;

        @Value
        @Jacksonized
        @Builder
        public static class AuthorizationData {

            String authorizationToken;
            Double expiresAt;
        }
    }
}
