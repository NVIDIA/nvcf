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
package com.nvidia.icms.service.gating;

import static com.nvidia.icms.inbound.rest.converters.ErrorDataConverter.toUnifiedErrorData;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_GPU;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_INSTANCE_TYPE;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.GpuResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.InstanceTypeResponse;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import com.nvidia.icms.uec.IcmsHttpUnifiedErrorException;
import com.nvidia.icms.uec.IcmsUnifiedError;
import com.nvidia.icms.uec.UnifiedErrorReporter;
import jakarta.validation.constraints.NotNull;
import java.util.HashSet;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatus;
import org.springframework.stereotype.Service;

/**
 * Applies {@code icms.gpu-gating} to everything a gated NCA ID can get or allocate
 */
@Service
@AllArgsConstructor
@Slf4j
public class GpuGatingService {

    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final UnifiedErrorReporter unifiedErrorReporter;

    /**
     * Removes GPUs and instance types withheld from this NCA ID from an account-info response.
     * A GPU left with no instance types is dropped entirely.
     */
    public void removeGatedGpusFromAccountInfo(@NotNull InstanceTypeAvailabilityResponse result,
                                               @NotNull String ncaId) {
        if (!icmsConfigurationProperties.hasGpuGating(ncaId) || result.getGpus() == null) {
            return;
        }

        result.getGpus().removeIf(gpu -> {
            if (!icmsConfigurationProperties.isGpuAllowedForNca(ncaId, gpu.getGpuName())) {
                return true;
            }

            if (gpu.getInstanceTypes() != null) {
                gpu.getInstanceTypes().removeIf(
                        instanceType -> !icmsConfigurationProperties.isInstanceTypeAllowedForNca(
                                ncaId, gpu.getGpuName(), instanceType.getInstanceName()));
            }

            boolean noInstanceTypesLeft =
                    gpu.getInstanceTypes() == null || gpu.getInstanceTypes().isEmpty();
            if (noInstanceTypesLeft) {
                log.info("NcaId {}: GPU {} removed from account info response because gpu gating "
                                 + "left it with no instance types", ncaId, gpu.getGpuName());
            }
            return noInstanceTypesLeft;
        });
    }

    /**
     * Removes GPUs and instance types withheld from this NCA ID from the cluster-group response,
     * dropping cluster groups left with no GPUs. Applies to BYOC groups too, which is what keeps
     * a gated org out of publicly accessible BYOC clusters.
     */
    public void removeGatedClusterGroups(@NotNull Set<ClusterGroups> clusterGroupsSet,
                                         @NotNull String ncaId) {
        if (!icmsConfigurationProperties.hasGpuGating(ncaId)) {
            return;
        }

        clusterGroupsSet.removeIf(clusterGroup -> {
            clusterGroup.setGpus(retainAllowedGpus(clusterGroup.getGpus(), ncaId));

            boolean noGpusLeft = isSetEmptyOrNull(clusterGroup.getGpus());
            if (noGpusLeft) {
                log.info("NcaId {}: clusterGroup {} removed from response by gpu gating",
                         ncaId, clusterGroup.getName());
            }
            return noGpusLeft;
        });
    }

    /**
     * Rebuilds the GPU set keeping only the allowed GPUs and, within them, the allowed instance
     * types.
     */
    private Set<GpuResponse> retainAllowedGpus(Set<GpuResponse> gpus, String ncaId) {
        if (isSetEmptyOrNull(gpus)) {
            return new HashSet<>();
        }

        Set<GpuResponse> allowedGpus = new HashSet<>();
        for (GpuResponse gpu : gpus) {
            if (!icmsConfigurationProperties.isGpuAllowedForNca(ncaId, gpu.getName())) {
                continue;
            }

            Set<InstanceTypeResponse> allowedInstanceTypes = Optional
                    .ofNullable(gpu.getInstanceTypes())
                    .orElseGet(HashSet::new)
                    .stream()
                    .filter(instanceType -> icmsConfigurationProperties.isInstanceTypeAllowedForNca(
                            ncaId, gpu.getName(), instanceType.getName()))
                    .collect(Collectors.toSet());

            if (allowedInstanceTypes.isEmpty()) {
                continue;
            }

            allowedGpus.add(GpuResponse.builder()
                                    .name(gpu.getName())
                                    .instanceTypes(allowedInstanceTypes)
                                    .build());
        }
        return allowedGpus;
    }

    /**
     * Removes destinations withheld from this NCA ID from an instance request. Unlike the read
     * paths, an empty result here is an error: the caller asked for capacity it may not use, so
     * this throws rather than silently launching nothing.
     */
    public void removeGatedDestinations(@NotNull Set<RequestInstanceDestination> destinations,
                                        @NotNull SpotInstanceRequestSchema instanceRequest) {
        String ncaId = instanceRequest.getNcaId();
        if (!icmsConfigurationProperties.hasGpuGating(ncaId)) {
            return;
        }

        int destinationCountBeforeGating = destinations.size();
        Set<RequestInstanceDestination> gatedOut = new HashSet<>();
        boolean anyGpuAllowed = false;

        for (RequestInstanceDestination destination : destinations) {
            if (!icmsConfigurationProperties.isGpuAllowedForNca(ncaId, destination.getGpuName())) {
                gatedOut.add(destination);
                continue;
            }

            anyGpuAllowed = true;
            String instanceTypeName = destination.getInstanceType() != null
                    ? destination.getInstanceType().getName() : null;
            if (!icmsConfigurationProperties.isInstanceTypeAllowedForNca(ncaId,
                                                                         destination.getGpuName(),
                                                                         instanceTypeName)) {
                gatedOut.add(destination);
            }
        }

        destinations.removeAll(gatedOut);

        log.info("InstanceRequest: {}: NcaId {}: gpu gating kept {} of {} destinations for GPU {} "
                         + "and instance type {}, removed clusters {}",
                 instanceRequest.getLoggingId(), ncaId, destinations.size(),
                 destinationCountBeforeGating, instanceRequest.getGpu(),
                 instanceRequest.getInstanceType(),
                 gatedOut.stream().map(RequestInstanceDestination::getClusterId).distinct().toList());

        if (!destinations.isEmpty()) {
            return;
        }

        // No GPU survived means the org has no access to the GPU at all; otherwise the GPU was
        // allowed and the instance type is what gating withheld.
        throwGatingError(anyGpuAllowed
                                 ? NVCF_CUSTOMER_NO_ACCESS_TO_INSTANCE_TYPE
                                 : NVCF_CUSTOMER_NO_ACCESS_TO_GPU, instanceRequest);
    }

    private void throwGatingError(@NotNull IcmsUnifiedError icmsUnifiedError,
                                  @NotNull SpotInstanceRequestSchema instanceRequest) {
        String message = icmsUnifiedError == NVCF_CUSTOMER_NO_ACCESS_TO_GPU
                ? String.format(icmsUnifiedError.defaultMessageFormat(), instanceRequest.getGpu())
                : String.format(icmsUnifiedError.defaultMessageFormat(),
                                instanceRequest.getInstanceType(), instanceRequest.getGpu());

        unifiedErrorReporter.reportAndThrow(new IcmsHttpUnifiedErrorException(
                icmsUnifiedError, HttpStatus.CONFLICT, message,
                toUnifiedErrorData(instanceRequest)));
    }
}
