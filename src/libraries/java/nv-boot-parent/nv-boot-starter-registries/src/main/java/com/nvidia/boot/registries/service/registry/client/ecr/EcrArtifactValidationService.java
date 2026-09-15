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

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.client.ecr.EcrArtifactRegistryStubService.DescribeImagesRequest;
import com.nvidia.boot.registries.service.registry.client.ecr.EcrArtifactRegistryStubService.DescribeImagesRequest.ImageId;
import com.nvidia.boot.registries.service.registry.client.ecr.EcrArtifactRegistryStubService.DescribeImagesResponse;
import com.nvidia.boot.registries.service.registry.client.ecr.dto.EcrArtifactComponents;
import io.micrometer.common.util.StringUtils;
import java.net.URI;
import java.util.List;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
public class EcrArtifactValidationService {

    private static final String MESG_ARTIFACT_NOT_FOUND =
            "Artifact '%s' not found in ECR registry";
    private static final String MESG_SUCCESSFULLY_VALIDATED_ECR_IMAGE =
            "Successfully validated ECR image: %s";

    private static final String ECR_SERVICE = "ecr";
    private static final String AMZ_ECR_TARGET =
            "AmazonEC2ContainerRegistry_V20150921.DescribeImages";
    private static final String ECR_PUBLIC_SERVICE = "ecr-public";
    private static final String AMZ_ECR_PUBLIC_TARGET =
            "SpencerFrontendService.DescribeImages";
    private static final String ECR_PUBLIC_REGION = "us-east-1";

    public static void validatePrivateArtifact(
            EcrArtifactRegistryStubService ecrStubService,
            String ecrEndpointUrl,
            EcrArtifactComponents components,
            String base64ApiKey) {

        var accessKeyCredentials = toAccessKeyCredentials(base64ApiKey);
        var accessKeyId = accessKeyCredentials.accessKeyId();
        var secretAccessKey = accessKeyCredentials.secretAccessKey();

        var request = DescribeImagesRequest.builder()
                .registryId(components.registryId())
                .repositoryName(components.repositoryName())
                .imageIds(List.of(getArtifactId(components)))
                .build();
        var requestBody = RegistryMapperService.toJson(request);
        var awsAuthHeaders = signRequest(
                components.region(),
                ECR_SERVICE,
                accessKeyId,
                secretAccessKey,
                requestBody,
                AMZ_ECR_TARGET,
                "ecr." + components.region() + ".amazonaws.com"
        );

        var response = ecrStubService.describePrivateArtifacts(
                URI.create(ecrEndpointUrl),
                awsAuthHeaders.authorization(),
                AMZ_CONTENT_TYPE,
                AMZ_ECR_TARGET,
                awsAuthHeaders.xAmzDate(),
                awsAuthHeaders.xAmzContentSha256(),
                request);
        handleResponse(response, components);
    }

    public static void validatePublicArtifact(
            EcrArtifactRegistryStubService ecrStubService,
            EcrArtifactComponents components,
            String base64ApiKey) {
        var accessKeyCredentials = toAccessKeyCredentials(base64ApiKey);
        var accessKeyId = accessKeyCredentials.accessKeyId();
        var secretAccessKey = accessKeyCredentials.secretAccessKey();

        var request = DescribeImagesRequest.builder()
                .repositoryName(components.repositoryName())
                .imageIds(List.of(getArtifactId(components)))
                .build();
        var requestBody = RegistryMapperService.toJson(request);
        var awsAuthHeaders = signRequest(
                ECR_PUBLIC_REGION,
                ECR_PUBLIC_SERVICE,
                accessKeyId,
                secretAccessKey,
                requestBody,
                AMZ_ECR_PUBLIC_TARGET,
                URI.create(ECR_PUBLIC_REGISTRY_BASE_URI).getHost()
        );

        var response = ecrStubService.describePublicArtifacts(
                awsAuthHeaders.authorization(),
                AMZ_CONTENT_TYPE,
                AMZ_ECR_PUBLIC_TARGET,
                awsAuthHeaders.xAmzDate(),
                awsAuthHeaders.xAmzContentSha256(),
                request);
        handleResponse(response, components);
    }

    private static ImageId getArtifactId(EcrArtifactComponents components) {
        if (StringUtils.isNotEmpty(components.digest())) {
            return ImageId.builder().imageDigest(components.digest()).build();
        } else {
            return ImageId.builder().imageTag(components.tag()).build();
        }
    }

    private static void handleResponse(
            DescribeImagesResponse response,
            EcrArtifactComponents components) {
        if (response == null || CollectionUtils.isEmpty(response.getImageDetails())) {
            var msg = MESG_ARTIFACT_NOT_FOUND.formatted(
                    RegistryMapperService.toJson(components));
            log.error(msg);
            throw new BadRequestException(msg);
        }
        var msg = MESG_SUCCESSFULLY_VALIDATED_ECR_IMAGE
                .formatted(RegistryMapperService.toJson(components));
        log.info(msg);
    }
}
