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
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toJson;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.VOLCENGINE_REGISTRY_BASE_URI;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineSignatureUtils.API_VERSION;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineSignatureUtils.GET_AUTHORIZATION_TOKEN_ACTION;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineSignatureUtils.signRequest;

import com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineArtifactRegistryStubService.AuthorizationTokenRequest;
import com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineArtifactRegistryStubService.AuthorizationTokenResponse;
import java.net.URI;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.MediaType;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class VolcengineCredentialValidationService {

    public static AuthorizationTokenResponse getAuthorizationToken(
            VolcengineArtifactRegistryStubService volcengineStubService,
            String registry,
            String region,
            String base64EncodedSecret) {

        var accessKeyCredentials = toAccessKeyCredentials(base64EncodedSecret);
        var accessKeyId = accessKeyCredentials.accessKeyId();
        var secretAccessKey = accessKeyCredentials.secretAccessKey();

        var request = AuthorizationTokenRequest.builder()
                .registry(registry)
                .build();
        var requestBody = toJson(request);
        var hostname = URI.create(VOLCENGINE_REGISTRY_BASE_URI).getHost();
        var authHeaders = signRequest(hostname, region, accessKeyId, secretAccessKey,
                                     requestBody, GET_AUTHORIZATION_TOKEN_ACTION);

        return volcengineStubService.getAuthorizationToken(
                GET_AUTHORIZATION_TOKEN_ACTION,
                API_VERSION,
                authHeaders.authorization(),
                MediaType.APPLICATION_JSON_VALUE,
                authHeaders.xContentSha256(),
                authHeaders.xDate(),
                request
        );
    }
}
