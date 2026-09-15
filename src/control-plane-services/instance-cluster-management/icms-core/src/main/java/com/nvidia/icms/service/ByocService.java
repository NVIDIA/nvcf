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

import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationCredentialsResponse;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterHeartbeatRequest;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.byoc.ByocTerminateService;
import com.nvidia.icms.service.byoc.ClusterHealthService;
import com.nvidia.icms.service.byoc.ClusterQueueAccessCredsService;
import com.nvidia.icms.service.byoc.ClusterRegistrationService;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class ByocService {

    private final ClusterQueueAccessCredsService clusterQueueAccessCredsService;
    private final ClusterRegistrationService clusterRegistrationService;
    private final ClusterHealthService clusterHeartbeatService;
    private final ClusterRepository clusterRepository;

    private final ByocTerminateService byocTerminateService;

    public BartRegistrationResponse registerCluster(
            BartRegistrationRequest bartRegistrationRequest,
            String clusterId,
            Map<String, Object> auditProps) {
        return clusterRegistrationService.registerCluster(bartRegistrationRequest, clusterId,
                                                          auditProps);
    }

    public void deleteCluster(String clusterId, Map<String, Object> auditProps) {
        clusterRegistrationService.deleteCluster(clusterId, auditProps);
    }

    public BartRegistrationCredentialsResponse getClusterQueuesInfo(String clusterId) {
        return clusterQueueAccessCredsService.getClusterQueuesInfo(clusterId);
    }

    public void registerClusterHeartbeat(
            ClusterHeartbeatRequest heartbeatRequest,
            String clusterId) {
        clusterHeartbeatService.registerClusterHeartbeat(heartbeatRequest, clusterId);
    }

    public CloudProvider getCloudProviderForByocCluster(String clusterId) {

        Optional<ClusterEntity> optionalClusterEntity =
                getClusterEntityFromByocClusterId(clusterId);

        if (optionalClusterEntity.isEmpty()) {
            String errMsg = String.format("Cloud not find any cluster with %s clusterId",
                                          clusterId);
            log.error(errMsg);
            throw new IcmsNotFoundException(errMsg);
        }

        ClusterEntity clusterEntity = optionalClusterEntity.get();
        return CloudProvider.getCloudProviderFromClusterProvider(
                clusterEntity.getClusterProvider());
    }

    public ClusterEntity validateAndGetClusterEntityFromByocClusterId(String clusterId) {

        Optional<ClusterEntity> optionalClusterEntity =
                getClusterEntityFromByocClusterId(clusterId);

        if (optionalClusterEntity.isEmpty()) {
            String errMsg = String.format("Cloud not find any cluster with %s clusterId",
                    clusterId);
            log.error(errMsg);
            throw new IcmsNotFoundException(errMsg);
        }
        return optionalClusterEntity.get();
    }

    public Optional<ClusterEntity> getClusterEntityFromByocClusterId(@NotNull String clusterId) {

        // Setting checkForHashedClusterId: true
        // Reason: Request could be for BART or NVCA cluster
        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, true);
        if (optionalClusterEntity.isEmpty()) {
            log.debug("Couldn't find clusterInfo for {} clusterId", clusterId);
            return Optional.empty();
        }
        ClusterEntity clusterEntity = optionalClusterEntity.get();
        return Optional.of(clusterEntity);
    }

    public TerminateInstancesResponse terminateInstances(
            Set<InstanceV2Entity> instanceEntities,
            Map<String, Object> auditProps) {
        return byocTerminateService.terminateInstances(instanceEntities,
                                                            auditProps);
    }

    public TerminateInstancesResponse terminateInstanceRequests(
            Map<String, InstanceV2Entity> runningInstancesEntityMap,
            Map<String, Object> auditProps) {
        return byocTerminateService.terminateInstanceRequests(runningInstancesEntityMap,
                                                           auditProps);
    }
}
