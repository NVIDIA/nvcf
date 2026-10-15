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
package com.nvidia.icms.outbound.cassandra.byoc;

import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAccountKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV4Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV3Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import java.util.Collections;
import java.util.HashSet;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.NonNull;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;

@UtilityClass
@Slf4j
public class NvcaConverter {

    public static ClusterByGroupIdAndIdEntity toClusterByGroupIdAndIdEntity(ClusterEntity entity) {
        return ClusterByGroupIdAndIdEntity.builder()
                .clusterName(entity.getClusterName())
                .key(ClusterByGroupIdAndIdKey.builder()
                             .clusterId(entity.getClusterId())
                             .clusterGroupId(entity.getClusterGroupId())
                             .build())
                .ncaId(entity.getNcaId())
                .terminationQueueUrl(entity.getTerminationQueueUrl())
                .terminationQueueType(entity.getTerminationQueueType())
                .clusterDescription(entity.getClusterDescription())
                .clusterProvider(entity.getClusterProvider())
                .clusterStatus(entity.getClusterStatus())
                .clusterSource(entity.getClusterSource())
                .k8sVersion(entity.getK8sVersion())
                .clusterGroupName(entity.getClusterGroupName())
                .creationQueueUrl(entity.getCreationQueueUrl())
                .creationQueueType(entity.getCreationQueueType())
                .gpus(entity.getGpus())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .requestDump(entity.getRequestDump())
                .gpusV4(entity.getGpusV4())
                .gpusV5(getGpusV5(entity))
                .capabilities(entity.getCapabilities())
                .attributes(entity.getAttributes())
                .nvcaVersion(entity.getNvcaVersion())
                .authClientId(entity.getAuthClientId())
                .region(entity.getRegion())
                .nvcaLastConnected(entity.getNvcaLastConnected())
                .creationQueues(entity.getCreationQueues())
                .customAttributes(entity.getCustomAttributes())
                .allowClusterTargeting(entity.getAllowClusterTargeting())
                .clusterCreationQueues(entity.getClusterCreationQueues())
                .clusterCreationQueuesForTasks(entity.getClusterCreationQueueForTasks())
                .allowTaskClusterCreationQueues(entity.getAllowTaskClusterCreationQueues())
                .clusterKeyId(entity.getClusterKeyId())
                .build();
    }

    public static ClustersByAccountEntity toClustersByAccountEntity(ClusterEntity entity) {
        return ClustersByAccountEntity.builder()
                .key(ClustersByAccountKey.builder()
                             .clusterName(entity.getClusterName())
                             .ncaId(entity.getNcaId())
                             .build())
                .registrationTime(entity.getRegistrationTime())
                .clusterId(entity.getClusterId())
                .terminationQueueUrl(entity.getTerminationQueueUrl())
                .terminationQueueType(entity.getTerminationQueueType())
                .clusterDescription(entity.getClusterDescription())
                .clusterProvider(entity.getClusterProvider())
                .clusterStatus(entity.getClusterStatus())
                .clusterSource(entity.getClusterSource())
                .k8sVersion(entity.getK8sVersion())
                .clusterGroupName(entity.getClusterGroupName())
                .clusterGroupId(entity.getClusterGroupId())
                .creationQueueUrl(entity.getCreationQueueUrl())
                .creationQueueType(entity.getCreationQueueType())
                .gpus(entity.getGpus())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .requestDump(entity.getRequestDump())
                .gpusV4(entity.getGpusV4())
                .gpusV5(getGpusV5(entity))
                .capabilities(entity.getCapabilities())
                .attributes(entity.getAttributes())
                .nvcaVersion(entity.getNvcaVersion())
                .authClientId(entity.getAuthClientId())
                .region(entity.getRegion())
                .nvcaLastConnected(entity.getNvcaLastConnected())
                .creationQueues(entity.getCreationQueues())
                .customAttributes(entity.getCustomAttributes())
                .allowClusterTargeting(entity.getAllowClusterTargeting())
                .clusterCreationQueues(entity.getClusterCreationQueues())
                .clusterCreationQueuesForTasks(entity.getClusterCreationQueueForTasks())
                .allowTaskClusterCreationQueues(entity.getAllowTaskClusterCreationQueues())
                .clusterKeyId(entity.getClusterKeyId())
                .build();
    }

    public static ClusterGroupsByAccountEntity toClusterGroupsByAccountsEntity(
            ClusterEntity entity) {
        return ClusterGroupsByAccountEntity.builder()
                .key(ClusterGroupsByAccountKey.builder()
                             .clusterGroupName(entity.getClusterGroupName())
                             .ncaId(entity.getNcaId())
                             .build())
                .clusterGroupId(entity.getClusterGroupId())
                .creationQueueUrl(entity.getCreationQueueUrl())
                .creationQueueType(entity.getCreationQueueType())
                .gpus(entity.getGpus())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .build();
    }

    public static ClusterGroupsByAuthorizedAccountsEntity toClusterGroupsByAuthorizedAccountsEntity(
            ClusterEntity entity, String ncaId) {
        return ClusterGroupsByAuthorizedAccountsEntity.builder()
                .key(ClusterGroupsByAuthorizedAccountsKey.builder()
                             .clusterGroupName(entity.getClusterGroupName())
                             .clusterGroupId(entity.getClusterGroupId())
                             .ncaIdKey(entity.getNcaId())
                             .build())
                .creationQueueUrl(entity.getCreationQueueUrl())
                .creationQueueType(entity.getCreationQueueType())
                .gpus(entity.getGpus())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .ncaId(ncaId)
                .build();
    }

    public static ClustersByAuthorizedAccountsEntity toClustersByAuthorizedAccountsEntity(
            ClusterEntity entity, String ncaId) {
        return ClustersByAuthorizedAccountsEntity.builder()
                .key(ClustersByAuthorizedAccountsKey.builder()
                             .clusterId(entity.getClusterId())
                             .ncaIdKey(ncaId)
                             .build())
                .clusterName(entity.getClusterName())
                .clusterGroupName(entity.getClusterGroupName())
                .clusterGroupId(entity.getClusterGroupId())
                .creationQueues(entity.getCreationQueues())
                .gpusV4(entity.getGpusV4())
                .gpusV5(getGpusV5(entity))
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .ncaId(ncaId)
                .nvcaLastConnected(entity.getNvcaLastConnected())
                .clusterCreationQueues(entity.getClusterCreationQueues())
                .clusterCreationQueuesForTasks(entity.getClusterCreationQueueForTasks())
                .clusterKeyId(entity.getClusterKeyId())
                .build();
    }

    public static ClusterGroupByGroupIdEntity toClusterGroupByGroupIdEntity(ClusterEntity entity) {
        return ClusterGroupByGroupIdEntity.builder()
                .clusterGroupName(entity.getClusterGroupName())
                .clusterGroupId(entity.getClusterGroupId())
                .creationQueueUrl(entity.getCreationQueueUrl())
                .creationQueueType(entity.getCreationQueueType())
                .gpus(entity.getGpus())
                .ncaId(entity.getNcaId())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .build();
    }

    public static ClusterEntity toClusterEntity(ClusterByGroupIdAndIdEntity entity) {
        return ClusterEntity.builder()
                .clusterName(entity.getClusterName())
                .clusterId(entity.getKey().getClusterId())
                .ncaId(entity.getNcaId())
                .terminationQueueUrl(entity.getTerminationQueueUrl())
                .terminationQueueType(entity.getTerminationQueueType())
                .clusterDescription(entity.getClusterDescription())
                .clusterProvider(entity.getClusterProvider())
                .clusterStatus(entity.getClusterStatus())
                .clusterSource(entity.getClusterSource())
                .k8sVersion(entity.getK8sVersion())
                .clusterGroupName(entity.getClusterGroupName())
                .clusterGroupId(entity.getKey().getClusterGroupId())
                .creationQueueUrl(entity.getCreationQueueUrl())
                .creationQueueType(entity.getCreationQueueType())
                .gpus(entity.getGpus())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .requestDump(entity.getRequestDump())
                .capabilities(entity.getCapabilities())
                .attributes(entity.getAttributes())
                .gpusV4(entity.getGpusV4())
                .gpusV5(getGpusV5(entity))
                .nvcaVersion(entity.getNvcaVersion())
                .authClientId(entity.getAuthClientId())
                .region(entity.getRegion())
                .creationQueues(entity.getCreationQueues())
                .nvcaLastConnected(entity.getNvcaLastConnected())
                .customAttributes(entity.getCustomAttributes())
                .allowClusterTargeting(entity.getAllowClusterTargeting())
                .clusterCreationQueues(entity.getClusterCreationQueues())
                .clusterCreationQueuesForTasks(entity.getClusterCreationQueueForTasks())
                .allowTaskClusterCreationQueues(entity.getAllowTaskClusterCreationQueues())
                .clusterKeyId(entity.getClusterKeyId())
                .build();
    }

    public static ClusterEntity toClusterEntity(ClustersByAccountEntity entity) {
        return ClusterEntity.builder()
                .clusterName(entity.getKey().getClusterName())
                .clusterId(entity.getClusterId())
                .ncaId(entity.getKey().getNcaId())
                .terminationQueueUrl(entity.getTerminationQueueUrl())
                .terminationQueueType(entity.getTerminationQueueType())
                .clusterDescription(entity.getClusterDescription())
                .clusterProvider(entity.getClusterProvider())
                .clusterStatus(entity.getClusterStatus())
                .clusterSource(entity.getClusterSource())
                .k8sVersion(entity.getK8sVersion())
                .clusterGroupName(entity.getClusterGroupName())
                .clusterGroupId(entity.getClusterGroupId())
                .creationQueueUrl(entity.getCreationQueueUrl())
                .creationQueueType(entity.getCreationQueueType())
                .gpus(entity.getGpus())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .requestDump(entity.getRequestDump())
                .capabilities(entity.getCapabilities())
                .attributes(entity.getAttributes())
                .gpusV4(entity.getGpusV4())
                .gpusV5(getGpusV5(entity))
                .nvcaVersion(entity.getNvcaVersion())
                .authClientId(entity.getAuthClientId())
                .region(entity.getRegion())
                .creationQueues(entity.getCreationQueues())
                .nvcaLastConnected(entity.getNvcaLastConnected())
                .customAttributes(entity.getCustomAttributes())
                .allowClusterTargeting(entity.getAllowClusterTargeting())
                .clusterCreationQueues(entity.getClusterCreationQueues())
                .clusterCreationQueuesForTasks(entity.getClusterCreationQueueForTasks())
                .allowTaskClusterCreationQueues(entity.getAllowTaskClusterCreationQueues())
                .clusterKeyId(entity.getClusterKeyId())
                .build();
    }


    public static Set<GpuV5Udt> getGpusV5(ClustersByAccountEntity entity) {
        return getGpusV5(entity.getGpusV5(), entity.getGpusV4());
    }

    public static Set<GpuV5Udt> getGpusV5(ClusterEntity entity) {
        return getGpusV5(entity.getGpusV5(), entity.getGpusV4());
    }

    public static Set<GpuV5Udt> getGpusV5(ClusterByGroupIdAndIdEntity entity) {
        return getGpusV5(entity.getGpusV5(), entity.getGpusV4());
    }

    public static Set<GpuV5Udt> getGpusV5(ClustersByAuthorizedAccountsEntity entity) {
        return getGpusV5(entity.getGpusV5(), entity.getGpusV4());
    }

    public static Set<GpuV5Udt> getGpusV5(Set<GpuV5Udt> gpuV5s, Set<GpuV4Udt> gpuV4s) {
        if (gpuV5s != null && !gpuV5s.isEmpty()) {
            return gpuV5s;
        }

        return gpuV4s != null ?
                gpuV4s.stream().map(NvcaConverter::toGpuV5).collect(Collectors.toSet()) :
                Collections.emptySet();
    }

    public static Set<GpuV5Udt> getGpusV5(Set<GpuUdt> gpuUdts) {
        return gpuUdts != null ?
                gpuUdts.stream().map(NvcaConverter::toGpuV5).collect(Collectors.toSet()) :
                Collections.emptySet();
    }

    // GpuV5 to GpuV4 converter
    public static Set<GpuV4Udt> toGpusV4(Set<GpuV5Udt> gpuV5s) {

        return gpuV5s != null ?
                gpuV5s.stream().map(NvcaConverter::toGpuV4).collect(Collectors.toSet()) :
                Collections.emptySet();
    }

    // InstanceTypeUdt to InstanceTypeV5Udt converter
    public static InstanceTypeV5Udt toInstanceTypeV5(@NonNull InstanceTypeUdt instanceType) {
        return InstanceTypeV5Udt.builder()
                .value(instanceType.getValue())
                .name(instanceType.getName())
                .gpuCount(instanceType.getGpuCount())
                .description(instanceType.getDescription())
                .isDefault(instanceType.getIsDefault())
                .cpuCores(instanceType.getCpuCores())
                .systemMemory(instanceType.getSystemMemory())
                .gpuMemory(instanceType.getGpuMemory())
                .build();
    }

    private static InstanceTypeV5Udt toInstanceTypeV5(@NonNull InstanceTypeV3Udt instanceType) {
        return InstanceTypeV5Udt.builder()
                .value(instanceType.getValue())
                .name(instanceType.getName())
                .gpuCount(instanceType.getGpuCount())
                .description(instanceType.getDescription())
                .isDefault(instanceType.getIsDefault())
                .cpuCores(instanceType.getCpuCores())
                .systemMemory(instanceType.getSystemMemory())
                .gpuMemory(instanceType.getGpuMemory())
                .os(instanceType.getOs())
                .cpuArch(instanceType.getCpuArch())
                .driverVersion(instanceType.getDriverVersion())
                .storage(instanceType.getStorage())

                // Adding "SINGLE" as default value
                .nodeType(NodeTypeEnum.SINGLE.toString())
                .build();
    }

    private static GpuV5Udt toGpuV5(@NonNull GpuV4Udt gpuV4) {
        return GpuV5Udt.builder()
                .name(gpuV4.getName())
                .capacity(gpuV4.getCapacity())
                .instanceTypes(gpuV4.getInstanceTypes()
                                       .stream()
                                       .map(NvcaConverter::toInstanceTypeV5)
                                       .collect(Collectors.toSet()))
                .build();
    }

    private static GpuV5Udt toGpuV5(@NonNull GpuUdt gpuUdt) {
        return GpuV5Udt.builder()
                .name(gpuUdt.getName())
                .capacity(0)
                .instanceTypes(gpuUdt.getInstanceTypes()
                                       .stream()
                                       .map(NvcaConverter::toInstanceTypeV5)
                                       .collect(Collectors.toSet()))
                .build();
    }

    private static GpuV4Udt toGpuV4(@NonNull GpuV5Udt gpuV5) {
        return GpuV4Udt.builder()
                .name(gpuV5.getName())
                .capacity(gpuV5.getCapacity())
                .instanceTypes(gpuV5.getInstanceTypes()
                                       .stream()
                                       .map(NvcaConverter::toInstanceTypeV3)
                                       .collect(Collectors.toSet()))
                .build();
    }

    private static InstanceTypeV3Udt toInstanceTypeV3(@NonNull InstanceTypeV5Udt instanceTypeV5Udt) {
        return InstanceTypeV3Udt.builder()
                .value(instanceTypeV5Udt.getValue())
                .name(instanceTypeV5Udt.getName())
                .gpuCount(instanceTypeV5Udt.getGpuCount())
                .description(instanceTypeV5Udt.getDescription())
                .isDefault(instanceTypeV5Udt.getIsDefault())
                .cpuCores(instanceTypeV5Udt.getCpuCores())
                .systemMemory(instanceTypeV5Udt.getSystemMemory())
                .gpuMemory(instanceTypeV5Udt.getGpuMemory())
                .cpuArch(instanceTypeV5Udt.getCpuArch())
                .os(instanceTypeV5Udt.getOs())
                .driverVersion(instanceTypeV5Udt.getDriverVersion())
                .storage(instanceTypeV5Udt.getStorage())
                .build();
    }

    public static NodeTypeEnum getNodeTypeEnum(
            @NonNull InstanceTypeV5Udt instanceTypeV5Udt) {

        // If value is null in DB then returning default value
        String valueFromDb = instanceTypeV5Udt.getNodeType();
        if (valueFromDb == null) {
            return NodeTypeEnum.SINGLE;
        }

        try {
            return NodeTypeEnum.valueOf(instanceTypeV5Udt.getNodeType());

        } catch (Exception exception) {
            log.error(
                    "Failed to parse the nodeType, configured value in DB {}, error: {}, exception -",
                    valueFromDb, exception.getMessage(), exception);

            throw new IcmsInternalServerException(
                    String.format("Failed to parse instanceTypeUsage, configured value in DB - %s",
                                  valueFromDb));
        }
    }
}
