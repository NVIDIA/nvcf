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

import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toAccessKeyCredentials;
import static com.nvidia.boot.registries.service.registry.client.ecr.AwsSignatureUtils.signRequest;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.AMZ_CONTENT_TYPE;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PUBLIC_REGISTRY_BASE_URI;

import com.nvidia.boot.registries.service.registry.client.ecr.EcrArtifactRegistryStubService.PrivateAuthorizationTokenResponse;
import com.nvidia.boot.registries.service.registry.client.ecr.EcrArtifactRegistryStubService.PublicAuthorizationTokenResponse;
import java.net.URI;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class EcrCredentialValidationService {

    private static final String ECR_SERVICE = "ecr";
    private static final String AMZ_ECR_GET_AUTH_TOKEN_TARGET =
            "AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken";
    private static final String ECR_PUBLIC_SERVICE = "ecr-public";
    private static final String AMZ_ECR_PUBLIC_GET_AUTH_TOKEN_TARGET =
            "SpencerFrontendService.GetAuthorizationToken";
    private static final String ECR_PUBLIC_REGION = "us-east-1";

    public static PrivateAuthorizationTokenResponse getEcrPrivateAuthorizationToken(
            EcrArtifactRegistryStubService ecrStubService,
            String ecrEndpointUrl,
            String region,
            String base64EncodedSecret) {

        var accessKeyCredentials = toAccessKeyCredentials(base64EncodedSecret);
        var accessKeyId = accessKeyCredentials.accessKeyId();
        var secretAccessKey = accessKeyCredentials.secretAccessKey();

        var requestBody = "{}";
        var awsAuthHeaders = signRequest(
                region,
                ECR_SERVICE,
                accessKeyId,
                secretAccessKey,
                requestBody,
                AMZ_ECR_GET_AUTH_TOKEN_TARGET,
                "ecr." + region + ".amazonaws.com"
        );

        return ecrStubService.getEcrPrivateAuthorizationToken(
                URI.create(ecrEndpointUrl),
                awsAuthHeaders.authorization(),
                AMZ_CONTENT_TYPE,
                AMZ_ECR_GET_AUTH_TOKEN_TARGET,
                awsAuthHeaders.xAmzDate(),
                awsAuthHeaders.xAmzContentSha256(),
                requestBody);
    }

    public static PublicAuthorizationTokenResponse getEcrPublicAuthorizationToken(
            EcrArtifactRegistryStubService ecrStubService,
            String base64EncodedSecret) {

        var accessKeyCredentials = toAccessKeyCredentials(base64EncodedSecret);
        var accessKeyId = accessKeyCredentials.accessKeyId();
        var secretAccessKey = accessKeyCredentials.secretAccessKey();

        var requestBody = "{}";
        var awsAuthHeaders = signRequest(
                ECR_PUBLIC_REGION,
                ECR_PUBLIC_SERVICE,
                accessKeyId,
                secretAccessKey,
                requestBody,
                AMZ_ECR_PUBLIC_GET_AUTH_TOKEN_TARGET,
                URI.create(ECR_PUBLIC_REGISTRY_BASE_URI).getHost()
        );

        return ecrStubService.getEcrPublicAuthorizationToken(
                awsAuthHeaders.authorization(),
                AMZ_CONTENT_TYPE,
                AMZ_ECR_PUBLIC_GET_AUTH_TOKEN_TARGET,
                awsAuthHeaders.xAmzDate(),
                awsAuthHeaders.xAmzContentSha256(),
                requestBody);
    }
}
