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

import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toAccessKeyCredentials;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.VOLCENGINE_REGISTRY_BASE_URI;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.constructListTagsRequest;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineSignatureUtils.API_VERSION;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineSignatureUtils.LIST_TAGS_ACTION;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineSignatureUtils.signRequest;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.client.volcengine.dto.VolcengineArtifactComponents;
import java.net.URI;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.MediaType;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
public class VolcengineArtifactValidationService {

    private static final String MESG_ARTIFACT_NOT_FOUND =
            "Artifact '%s' not found in Volcengine registry";
    private static final String MESG_SUCCESSFULLY_VALIDATED_VOLCENGINE_IMAGE =
            "Successfully validated Volcengine registry image: %s";

    public static void validateArtifact(
            VolcengineArtifactRegistryStubService volcengineStubService,
            VolcengineArtifactComponents components,
            String base64ApiKey) {

        var accessKeyCredentials = toAccessKeyCredentials(base64ApiKey);
        var accessKeyId = accessKeyCredentials.accessKeyId();
        var secretAccessKey = accessKeyCredentials.secretAccessKey();

        var request = constructListTagsRequest(components);
        var requestBody = RegistryMapperService.toJson(request);
        var hostname = URI.create(VOLCENGINE_REGISTRY_BASE_URI).getHost();
        var authHeaders =
                signRequest(hostname, components.region(), accessKeyId, secretAccessKey,
                            requestBody);

        var response = volcengineStubService.listTags(
                LIST_TAGS_ACTION,
                API_VERSION,
                authHeaders.authorization(),
                MediaType.APPLICATION_JSON_VALUE,
                authHeaders.xContentSha256(),
                authHeaders.xDate(),
                request
        );
        if (response == null || response.getResult() == null
                || CollectionUtils.isEmpty(response.getResult().getItems())) {
            var msg = MESG_ARTIFACT_NOT_FOUND.formatted(
                    RegistryMapperService.toJson(components));
            log.error(msg);
            throw new NotFoundException(msg);
        }
        var msg = MESG_SUCCESSFULLY_VALIDATED_VOLCENGINE_IMAGE.formatted(
                RegistryMapperService.toJson(components));
        log.info(msg);
    }
}
