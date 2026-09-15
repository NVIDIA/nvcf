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

import com.nvidia.icms.inbound.rest.model.cluster.ClusterRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationResponse;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterUpdateRequest;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.service.extensions.api.ClusterRegistrationHandler;
import com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService;
import com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterListingService;
import com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterReconfigurationService;
import com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterTerminateService;
import java.util.List;
import java.util.Map;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class ClusterManagementService {

    private final ClusterListingService clusterListingService;

    private final ClusterCreationService clusterCreationService;

    private final ClusterTerminateService clusterTerminateService;

    private final ClusterReconfigurationService clusterReconfigurationService;

    private final ClusterRegistrationHandler clusterRegistrationHandler;

    public ClusterCreationResponse clusterCreation(
            ClusterCreationRequest clusterCreationRequest, String ncaId,
            Map<String, Object> auditProps) {
        return clusterCreationService.clusterCreation(clusterCreationRequest, ncaId, auditProps);
    }

    public List<GetClusterResponse> getClusters(String ncaId, Boolean includeAuthorizedClusters,
                                                Boolean includeNonByocInAuthorizedClusters) {
        return clusterListingService.getClustersByNcaId(ncaId,
                includeAuthorizedClusters,
                includeNonByocInAuthorizedClusters);
    }

    public GetClusterResponse getCluster(String ncaId, String clusterId) {
        return clusterListingService.getClusterByNcaIdAndClusterId(ncaId, clusterId);
    }

    public void deleteCluster(String ncaId, String clusterId, Map<String, Object> auditProps) {
        clusterTerminateService.deleteCluster(ncaId, clusterId, auditProps);
    }

    public void updateCluster(
            ClusterUpdateRequest clusterUpdateRequest, String ncaId, String clusterId,
            Map<String, Object> auditProps) {
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest, ncaId, clusterId,
                                                         auditProps);
    }

    public String getClusterVersion(String ncaId) {
        return clusterListingService.getClusterVersion(ncaId);
    }

    public void registerCluster(
            ClusterRegistrationRequest registrationRequest, String zoneName, String clientId,
            Map<String, Object> auditProps) {
        clusterRegistrationHandler.registerCluster(registrationRequest, zoneName,
                                                   clientId, auditProps);
    }
}
