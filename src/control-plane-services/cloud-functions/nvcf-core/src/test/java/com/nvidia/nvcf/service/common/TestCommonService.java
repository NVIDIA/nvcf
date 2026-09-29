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
package com.nvidia.nvcf.service.common;

import com.nvidia.boot.audit.AuditService;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.persistence.function.FunctionsDeploymentRepository;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.GpuSpecificationsRepository;
import com.nvidia.nvcf.service.apikeys.ApiKeysService;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.registry.RegistryArtifactService;
import jakarta.annotation.Nonnull;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

@Service
@Slf4j
public class TestCommonService {
    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private FunctionsDeploymentRepository deploymentRepository;

    @Autowired
    private GpuSpecificationsRepository gpuSpecificationRepository;

    @Autowired
    private AuthorizedPartiesService authorizedPartiesService;

    @Autowired
    private ApiKeysService apiKeysService;

    @Autowired
    private IcmsClient icmsClient;

    @Autowired
    private RegistryArtifactService artifactService;

    @Autowired
    private AuditService auditService;


    // Invalidate caches, clear repositories, etc. after each test to start the next one with
    // a clean slate and avoid interference.
    public void reset() {
        // use of MockApiKeysServer with different scopes causes the apikeys cache to dirty
        apiKeysService.invalidateCache();

        authorizedPartiesService.clearPublicFunctionCache();
        functionsRepository.deleteAll();
        deploymentRepository.deleteAll();
        gpuSpecificationRepository.deleteAll();
        icmsClient.clearClusterGroupCache();
        icmsClient.clearInstanceTypesCache();
        artifactService.clearArtifactCache();
    }

    @Nonnull
    public AuditEventPayload.Builder getAuditEventPayloadBuilder() {
        return auditService.auditEventPayloadBuilder()
                .groupType("NVCF-TESTS")
                .actorId("unknown")
                .actorLocation("nowhere")
                .subjectId("unknown")
                .subjectLocation("nowhere")
                .objectLocation("NVCF-TESTS")
                .custom("test-context", "TestAccountService"); // Avoid exception from

    }

}
