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
package com.nvidia.icms.service;

import com.nvidia.icms.inbound.rest.model.nvca.NvcaAccessCreds;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationResponse;
import com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class NvcaService {

    private final NvcaClusterRegistrationService nvcaClusterRegistrationService;

    public NvcaRegistrationResponse nvcaClusterRegistration(
            @NotNull NvcaRegistrationRequest nvcaRegistrationRequest,
            @NotNull String clusterId,
            @NotNull Map<String, Object> auditProperties) {
        return nvcaClusterRegistrationService.nvcaClusterRegistration(nvcaRegistrationRequest,
                                                                      clusterId, auditProperties);
    }

    public NvcaAccessCreds renewAccessCredentials(@NotNull String clusterId) {
        return nvcaClusterRegistrationService.renewAccessCredentials(clusterId);
    }
}
