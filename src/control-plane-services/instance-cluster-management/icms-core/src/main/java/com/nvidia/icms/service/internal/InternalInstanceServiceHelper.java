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
package com.nvidia.icms.service.internal;

import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.ByocService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import jakarta.validation.constraints.NotNull;
import java.util.Optional;

import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class InternalInstanceServiceHelper {

    private final ByocService byocService;
    private final ComputePlatformService computePlatformService;

    @Data
    @Builder
    public static class InstancePlacementValidationResponse {

        SpotInstanceStatusUpdateRequest.InstancePlacement instancePlacement;
        ResourceProvider resourceProvider;
        CloudProvider cloudProvider;
        String clusterName;
    }

    @Observed
    public InstancePlacementValidationResponse validateInstancePlacement(
            @Nullable SpotInstanceStatusUpdateRequest.InstancePlacement instancePlacement,
            @NotNull String clientId,
            String requestId) {

        if ((null == instancePlacement) || (null == instancePlacement.getAvailabilityZone()) ||
                (StringUtils.isEmpty(instancePlacement.getAvailabilityZone()))) {

            // Request is for BYOC flow
            Optional<ClusterEntity> optionalClusterEntity =
                    byocService.getClusterEntityFromByocClusterId(clientId);

            if (optionalClusterEntity.isPresent()) {
                ClusterEntity clusterEntity = optionalClusterEntity.get();

                if (null == instancePlacement) {
                    return InstancePlacementValidationResponse.builder()
                            .instancePlacement(
                                    new SpotInstanceStatusUpdateRequest.InstancePlacement(
                                            clusterEntity.getClusterId()))
                            .resourceProvider(ResourceProvider.BYOC)
                            .cloudProvider(CloudProvider.getCloudProviderFromClusterProvider(
                                    clusterEntity.getClusterProvider()))
                            .clusterName(clusterEntity.getClusterName())
                            .build();

                } else {
                    instancePlacement.setAvailabilityZone(clusterEntity.getClusterId());
                    return InstancePlacementValidationResponse.builder()
                            .instancePlacement(instancePlacement)
                            .clusterName(clusterEntity.getClusterName())
                            .resourceProvider(ResourceProvider.BYOC)
                            .cloudProvider(CloudProvider.getCloudProviderFromClusterProvider(
                                    clusterEntity.getClusterProvider()))
                            .build();
                }
            } else {

                // Zone is not provided and the request is not for registered NVCA
                String errMsg = String.format("Cloud not find any cluster with %s clusterId",
                                              clientId);
                log.error(errMsg);
                throw new IcmsNotFoundException(errMsg);
            }
        }

        // If InstancePlacement is provided then request is for non-BYOC cloud provider,
        // stamped with the configured compute platform's provider identity.
        // Fail fast if no compute platform is configured: this placement-bearing request is
        // non-BYOC by definition
        ResourceProvider resourceProvider = computePlatformService.primaryComputePlatformResourceProvider()
                .orElseThrow(() -> {
                    String errMsg = String.format(
                            "Cannot resolve non-BYOC resource provider for placement-bearing request %s: "
                                    + "no compute platform is configured", requestId);
                    log.error(errMsg);
                    return new IcmsInternalServerException(errMsg);
                });
        return InstancePlacementValidationResponse.builder()
                .instancePlacement(instancePlacement)
                .cloudProvider(computePlatformService.primaryComputePlatformCloudProvider()
                        .orElse(CloudProvider.UNKNOWN))
                .resourceProvider(resourceProvider)
                // For non-BYOC, zone = clusterName
                .clusterName(instancePlacement.getAvailabilityZone())
                .build();
    }
}
