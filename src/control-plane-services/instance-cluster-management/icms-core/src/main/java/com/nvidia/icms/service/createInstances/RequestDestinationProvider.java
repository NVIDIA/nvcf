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
package com.nvidia.icms.service.createInstances;

import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.service.extensions.api.InstanceDestinationProvider;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.service.account.GpuUsageFilter;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.uec.IcmsHttpUnifiedErrorException;
import com.nvidia.icms.uec.IcmsUnifiedError;
import com.nvidia.icms.uec.UnifiedErrorException;
import com.nvidia.icms.uec.UnifiedErrorReporter;
import jakarta.validation.constraints.NotNull;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.HttpStatus;
import org.springframework.stereotype.Service;

import java.util.HashSet;
import java.util.Map;
import java.util.Set;

import static com.nvidia.icms.inbound.rest.converters.ErrorDataConverter.toUnifiedErrorData;
import static com.nvidia.icms.service.account.GpuUsageFilter.getNullOrSet;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_GPU;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_INSTANCE_TYPE;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_READY_CLUSTER;
import static com.nvidia.icms.uec.IcmsUnifiedError.ICMS_UNDEFINED;

/**
 * Orchestrates destination resolution for instance creation requests.
 *
 * <p>For <em>targeted</em> requests the flow is:
 * <ol>
 *   <li>Fetch all ready clusters via the targeting system.</li>
 *   <li>Validate and filter clusters ({@link ByocRequestDestinationProvider}).</li>
 *   <li>Apply non-BYOC zone-authorization and long-wait-task filtering
 *       ({@link InstanceDestinationProvider}).</li>
 * </ol>
 *
 * <p>For <em>non-targeted</em> requests the flow routes to:
 * <ul>
 *   <li>{@link InstanceDestinationProvider} — for non-BYOC requests</li>
 *   <li>{@link ByocRequestDestinationProvider} — for all other backends</li>
 * </ul>
 */
@Service
@Slf4j
@AllArgsConstructor
public class RequestDestinationProvider {

    private final InstanceDestinationProvider instanceDestinationProvider;
    private final ByocRequestDestinationProvider byocRequestDestinationProvider;
    private final ClusterTargetingHelper clusterTargetingHelper;
    private final UnifiedErrorReporter unifiedErrorReporter;
    private final ComputePlatformService computePlatformService;

    public @NotNull Set<RequestInstanceDestination> getAllTargetedDestinations(
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Map<String, CloudHealthEntity> cloudHealthByClusterId) {

        // Fetch ready clusters from the targeting system (NCA-specific + wildcard)
        Set<ClusterByGroupIdAndIdEntity> readyClustersForRequestedNcaId = new HashSet<>(
                clusterTargetingHelper.getReadyClusterEntitiesForNcaId(instanceRequest.getNcaId()));
        Set<ClusterByGroupIdAndIdEntity> readyClustersForWildcardNcaId =
                clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD);

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>(readyClustersForRequestedNcaId);
        readyClusterEntities.addAll(readyClustersForWildcardNcaId);

        log.info("InstanceRequest: {}: Total clusters available is {}.",
                 instanceRequest.getLoggingId(), readyClusterEntities.size());

        GpuUsageFilter filter = getGpuUsageFilter(instanceRequest);

        readyClusterEntities.removeIf(
                r -> !r.getAllowClusterTargeting()
                        || !byocRequestDestinationProvider.isClusterAllowed(r, filter));

        Set<RequestInstanceDestination> destinations =
                byocRequestDestinationProvider.generateTargetedDestinationList(
                        readyClusterEntities, filter, instanceRequest);

        // Apply non-BYOC-specific filtering: zone authorization and task long-wait exclusion
        Set<String> nonByocAuthorizedZones =
                instanceDestinationProvider.getAuthorizedZones(readyClustersForRequestedNcaId, instanceRequest.getNcaId());
        destinations = instanceDestinationProvider.filterForAuthorizedZones(destinations, instanceRequest, nonByocAuthorizedZones);
        destinations = instanceDestinationProvider.removeForTaskWithLongWait(instanceRequest, destinations);

        log.info("InstanceRequest: {}: {} total destinations available after applying {} filters",
                 instanceRequest.getLoggingId(), destinations.size(), filter.toString());

        return destinations;
    }

    public @NotNull Set<RequestInstanceDestination> getAllNonTargetedDestinations(
            @NotNull SpotInstanceRequestSchema instanceRequest) {

        Set<RequestInstanceDestination> destinations =
                computePlatformService.isComputePlatformBackend(instanceRequest.getBackend())
                        ? instanceDestinationProvider.getNonTargetedDestinations(instanceRequest)
                        : byocRequestDestinationProvider.getNonTargetedDestinations(instanceRequest);

        if (destinations.isEmpty()) {
            //TODO Yury it should not throw an exception on this level
            throwUnifiedErrorException(NVCF_CUSTOMER_NO_ACCESS_TO_READY_CLUSTER, instanceRequest);
        }

        return destinations;
    }

    protected String getClusterIdsFromFilteredRequestInfo(Set<RequestInstanceDestination> filteredRequestInfo) {
        return filteredRequestInfo.stream()
                .map(RequestInstanceDestination::getClusterId)
                .filter(id -> !StringUtils.isBlank(id))
                .collect(Collectors.collectingAndThen(
                        Collectors.joining(", "),
                        result -> result.isEmpty() ? null : result));
    }

    private GpuUsageFilter getGpuUsageFilter(@NotNull SpotInstanceRequestSchema instanceRequest) {
        return GpuUsageFilter.builder()
                .regionNames(instanceRequest.getRegions())
                .clusterNames(instanceRequest.getClusters())
                .attributes(instanceRequest.getAttributes())
                .gpuNames(getNullOrSet(instanceRequest.getGpu()))
                .instanceTypes(getNullOrSet(instanceRequest.getInstanceType()))
                .build();
    }

    private void throwUnifiedErrorException(@NotNull IcmsUnifiedError icmsUnifiedError,
                                            @NotNull SpotInstanceRequestSchema instanceRequest) {
        UnifiedErrorException exception = switch (icmsUnifiedError) {
            case NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS -> new IcmsHttpUnifiedErrorException(
                    NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS,
                    HttpStatus.CONFLICT,
                    icmsUnifiedError.defaultMessageFormat(),
                    toUnifiedErrorData(instanceRequest));
            case NVCF_CUSTOMER_NO_ACCESS_TO_GPU -> new IcmsHttpUnifiedErrorException(
                    NVCF_CUSTOMER_NO_ACCESS_TO_GPU,
                    HttpStatus.CONFLICT,
                    String.format(icmsUnifiedError.defaultMessageFormat(), instanceRequest.getGpu()),
                    toUnifiedErrorData(instanceRequest));
            case NVCF_CUSTOMER_NO_ACCESS_TO_INSTANCE_TYPE -> new IcmsHttpUnifiedErrorException(
                    NVCF_CUSTOMER_NO_ACCESS_TO_INSTANCE_TYPE,
                    HttpStatus.CONFLICT,
                    String.format(icmsUnifiedError.defaultMessageFormat(),
                                  instanceRequest.getInstanceType(), instanceRequest.getGpu()),
                    toUnifiedErrorData(instanceRequest));
            case NVCF_CUSTOMER_NO_ACCESS_TO_READY_CLUSTER -> new IcmsHttpUnifiedErrorException(
                    NVCF_CUSTOMER_NO_ACCESS_TO_READY_CLUSTER,
                    HttpStatus.CONFLICT,
                    String.format(icmsUnifiedError.defaultMessageFormat(),
                                  instanceRequest.getGpu(), instanceRequest.getInstanceType()),
                    toUnifiedErrorData(instanceRequest));
            default -> new IcmsHttpUnifiedErrorException(
                    ICMS_UNDEFINED,
                    HttpStatus.INTERNAL_SERVER_ERROR,
                    ICMS_UNDEFINED.defaultMessageFormat(),
                    toUnifiedErrorData(instanceRequest));
        };
        unifiedErrorReporter.reportAndThrow(exception);
    }
}
