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
package com.nvidia.icms.service.byoc.nvca.clustermanagement;

import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterSource;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterConfigurationRepository;
import java.time.Instant;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.anySet;
import org.mockito.Mock;

import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import org.mockito.junit.jupiter.MockitoExtension;

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterCapabilitiesEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.GpuRequestSchema;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeRequestSchema;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterRegion;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterUpdateRequest;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterConfigurationByClusterIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountKey;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneKey;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE_VALUE;
import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;
import com.nvidia.icms.errors.IcmsBadRequestException;
import static org.mockito.Mockito.never;

@ExtendWith(MockitoExtension.class)
class ClusterReconfigurationServiceTest {

    @Mock
    ClusterRepository clusterRepository;

    @Mock
    NvcaClusterRepository nvcaClusterRepository;

    @Mock
    TelemetryEventClient telemetryEventClient;

    @Mock
    AppAuditService auditService;

    @Mock
    ClusterCreationService clusterCreationService;

    @Mock
    ClusterTerminateService clusterTerminateService;

    @Mock
    NvcaClusterRegistrationService clusterRegistrationService;

    @Mock
    InstanceServiceHelper instanceServiceHelper;

    @Mock
    NvcaConfigurationProperties nvcaConfigurationProperties;

    @Mock
    NvcaClusterConfigurationRepository nvcaClusterConfigurationRepository;

    ClusterReconfigurationService clusterReconfigurationService;

    @BeforeEach
    void init() {

        clusterReconfigurationService =
                new ClusterReconfigurationService(nvcaClusterRepository,
                        clusterRepository, telemetryEventClient, auditService,
                        clusterCreationService, clusterTerminateService, clusterRegistrationService,
                        instanceServiceHelper, nvcaConfigurationProperties, nvcaClusterConfigurationRepository);
    }

    @Test
    void reconfigureCluster_withInvalidAuthorizedNcaIdFormat_throwsError() {
        // Prepare
        when(nvcaConfigurationProperties.isAuthorizedNcaIdRegexValidationEnabled()).thenReturn(true);

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("v1");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                Set.of("invalid"),
                Collections.emptySet());

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Act
        IcmsBadRequestException exception = assertThrows(
                IcmsBadRequestException.class, () -> clusterReconfigurationService.reconfigureCluster(
                        clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        // Assert
        assertEquals(
                String.format("Invalid authorizedNCAIds '%s'. It must match regex %s",
                              "invalid",
                              ClusterCreationService.AUTHORIZED_NCA_ID_REGEX),
                exception.getBody().getDetail());
        verify(nvcaConfigurationProperties).isAuthorizedNcaIdRegexValidationEnabled();
        verify(nvcaClusterRepository, never()).updateClusterConfiguration(any(), anySet());
    }

    @Test
    void reconfigureCluster_withNvcaVersionChange_success() {
        // Prepare
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(DUMMY_BYOC_INSTANCE_TYPE)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("AZURE")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("old_version"); // Set initial NVCA version
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(), clusterEntity.getAuthorizedNcaIds(),
                Set.of(gpuRequestSchema));

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        ClusterEntity expectedClusterEntity = getDummyClusterEntity();
        expectedClusterEntity.setRegistrationTime(clusterEntity.getRegistrationTime());
        expectedClusterEntity.setNvcaLastConnected(clusterEntity.getNvcaLastConnected());
        expectedClusterEntity.setNvcaVersion("dummy_version");
        expectedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());
        expectedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                                                         clusterEntity.getNcaId(),
                                                         clusterEntity.getClusterId(),
                                                         new HashMap<>());

        // Assert
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(nvcaClusterRepository).updateClusterConfiguration(expectedClusterEntity,
                                                                 clusterEntity.getAuthorizedNcaIds());
        verify(nvcaConfigurationProperties).isAuthorizedNcaIdRegexValidationEnabled();
        verifyNoConfigSave();
    }

    @Test
    void reconfigureCluster_basicReconfiguration_success() {
        // Prepare
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(DUMMY_BYOC_INSTANCE_TYPE)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("AZURE")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(), 
                clusterEntity.getAuthorizedNcaIds(),
                Set.of(gpuRequestSchema));

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        verify(telemetryEventClient).triggerEvent(anyList());
        verify(nvcaConfigurationProperties).isAuthorizedNcaIdRegexValidationEnabled();
        verifyNoConfigSave();
    }

    @Test
    void reconfigureCluster_withConfigMaps_savesConfiguration() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("v1");
        ClusterUpdateRequest updateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(), clusterEntity.getAuthorizedNcaIds(),
                Collections.emptySet());
        updateRequest.setClusterConfigurations(Map.of("k", "v"));
        updateRequest.setClusterConfigurationFiles(Map.of("f", "x"));

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(updateRequest, clusterEntity.getNcaId(),
                clusterEntity.getClusterId(), new HashMap<>());

        // Assert
        verify(nvcaClusterConfigurationRepository).saveOrUpdateConfiguration(
                eq(clusterEntity.getClusterId()),
                eq(Map.of("k", "v")),
                eq(Map.of("f", "x"))
        );
    }

    @Test
    void reconfigureCluster_withoutConfigMaps_doesNotSaveConfiguration() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("v1");
        ClusterUpdateRequest updateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(), clusterEntity.getAuthorizedNcaIds(),
                Collections.emptySet());
        updateRequest.setClusterConfigurations(null);
        updateRequest.setClusterConfigurationFiles(null);

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(updateRequest, clusterEntity.getNcaId(),
                clusterEntity.getClusterId(), new HashMap<>());

        // Assert
        verifyNoConfigSave();
    }

    @Test
    void reconfigureCluster_withGpuChanges_success() {
        // Prepare
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name("Standard_ND96amsr_A100_v4_2x")
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version"); // Set NVCA version to allow reconfiguration
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN); // Set cloud provider to GDN
        clusterEntity.setClusterCreationQueuesForTasks(new HashMap<>()); // Initialize empty queues map
        
        // Create a different GPU schema to trigger GPU changes
        GpuRequestSchema newGpuRequestSchema = GpuRequestSchema.builder()
                .name("NEW_GPU")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of(newGpuRequestSchema)); // Use new GPU schema to trigger changes
        clusterUpdateRequest.setCapabilities(Set.of());

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstanceEntitiesFromZone(clusterEntity.getClusterId()))
                .thenReturn(Collections.emptyList());
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(instanceServiceHelper).getActiveInstanceEntitiesFromZone(clusterEntity.getClusterId());
        verify(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        verify(telemetryEventClient).triggerEvent(anyList());
        verifyNoConfigSave();
    }

    @Test
    void reconfigureCluster_withInvalidClusterId_throwsException() {
        // Prepare
        String invalidClusterId = "invalid-cluster-id";
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                "test-group",
                Set.of("test-nca-id"),
                Collections.emptySet());

        when(clusterRepository.getClusterInfoByClusterId(invalidClusterId, false))
                .thenReturn(Optional.empty());

        // Act & Assert
        IcmsNotFoundException exception = assertThrows(IcmsNotFoundException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        "test-nca-id",
                        invalidClusterId,
                        new HashMap<>()));

        assertEquals("Cluster with clusterId invalid-cluster-id does not exist", exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withNvca1Flow_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion(null); // Set to null to simulate NVCA 1.0 flow
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Collections.emptySet());

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Act & Assert
        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("This cluster " + clusterEntity.getClusterId() + " can not be reconfigured since it was registered with NVCA 1.0 flow",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withClusterGroupChange_success() {
        // Prepare
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(DUMMY_BYOC_INSTANCE_TYPE)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("AZURE")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN); // Set cloud provider to GDN
        clusterEntity.setClusterCreationQueuesForTasks(new HashMap<>()); // Initialize empty queues map
        clusterEntity.setNvcaVersion("dummy_version"); // Set NVCA version to allow reconfiguration
        
        String newClusterGroupName = "new-group";
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                newClusterGroupName,
                clusterEntity.getAuthorizedNcaIds(),
                Set.of(gpuRequestSchema));
        clusterUpdateRequest.setCapabilities(Set.of()); // Clear capabilities to match GPU changes test

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstanceEntitiesFromZone(clusterEntity.getClusterId()))
                .thenReturn(Collections.emptyList());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(), newClusterGroupName))
                .thenReturn(Optional.of(ClusterGroupsByAccountEntity.builder()
                        .key(ClusterGroupsByAccountKey.builder()
                                .clusterGroupName(newClusterGroupName)
                                .ncaId(clusterEntity.getNcaId())
                                .build())
                        .clusterGroupId("test-group-id")
                        .build()));
        doNothing().when(clusterTerminateService).deleteClusterForReconfiguration(
                any(), any(), any(), any());
        doNothing().when(clusterCreationService).validateClusterNamingForLength(any());

        ClusterEntity newClusterEntity = getDummyClusterEntity();
        newClusterEntity.setClusterGroupName(newClusterGroupName);

        // Mock behavior
        doReturn(clusterEntity).when(clusterCreationService).clusterCreationUpdateInDb(
                any(), any(), any(), any(), any());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(instanceServiceHelper).getActiveInstanceEntitiesFromZone(clusterEntity.getClusterId());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(), newClusterGroupName);
        verify(clusterTerminateService).deleteClusterForReconfiguration(
                any(), any(), any(), any());
        verify(clusterCreationService).validateClusterNamingForLength(any());
        verifyNoConfigSave();
    }

    @Test
    void reconfigureCluster_withActiveInstancesForRemovedGpu_throwsException() {
        // Prepare
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name("NEW_GPU_TYPE")
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("NEW_GPU")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);
        
        // Create update request that removes existing GPU
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of(gpuRequestSchema));
        clusterUpdateRequest.setCapabilities(Set.of());

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        when(instanceServiceHelper.getActiveInstanceEntitiesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of(getDummyInstanceByZoneEntity()));

        // Mock active instances for the GPU being removed
        when(instanceServiceHelper.getActiveInstancesFromZoneForInstanceType(
                clusterEntity.getClusterId(),
                Set.of(DUMMY_BYOC_INSTANCE_TYPE)))
                .thenReturn(Set.of("active-instance-1"));

        // Act & Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("Cluster reconfiguration failed, active instances exists for removed [Standard_ND96amsr_A100_v4_1x] GPUs, activeInstanceIds [active-instance-1]",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withActiveInstancesForRemovedNcaId_throwsException() {
        // Prepare
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(DUMMY_BYOC_INSTANCE_TYPE)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("AZURE")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);
        
        // Create update request that removes an authorized NCA ID
        Set<String> newAuthorizedNcaIds = new HashSet<>(clusterEntity.getAuthorizedNcaIds());
        String removedNcaId = newAuthorizedNcaIds.iterator().next();
        newAuthorizedNcaIds.remove(removedNcaId);
        
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                newAuthorizedNcaIds,
                Set.of(gpuRequestSchema));

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        
        // Mock active instances for the NCA ID being removed
        InstanceByZoneEntity activeInstance = new InstanceByZoneEntity();
        InstanceByZoneKey key = new InstanceByZoneKey();
        key.setInstanceId("active-instance-1");
        key.setZone(clusterEntity.getClusterId());
        key.setTruncatedTs(Instant.now());
        activeInstance.setKey(key);
        activeInstance.setNcaId(removedNcaId);
        
        when(instanceServiceHelper.getActiveInstanceEntitiesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of(activeInstance));

        // Act & Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("Cluster reconfiguration failed, active instances exists for removed [ncaId2] NcaIds, activeInstanceIds [active-instance-1]",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withCloudProviderChangeInSameGroup_throwsException() {
        // Prepare
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(DUMMY_BYOC_INSTANCE_TYPE)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("AZURE")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);
        
        // Create update request that changes cloud provider while keeping same group
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of(gpuRequestSchema));
        clusterUpdateRequest.setCloudProvider(ClusterProviderEnum.AWS); // Change cloud provider

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Act & Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("Cloud provider can not be changed when cluster group is same",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withNullClusterSource_usesDefaultNgcManaged() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setClusterSource(null);

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        verify(telemetryEventClient).triggerEvent(anyList());
        verifyNoConfigSave();
    }

    @Test
    void reconfigureCluster_withEmptyClusterSource_usesDefaultNgcManaged() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setClusterSource("");

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        verify(telemetryEventClient).triggerEvent(anyList());
        verifyNoConfigSave();
    }

    @Test
    void reconfigureCluster_withValidClusterSource_convertsToLowerCase() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setClusterSource("NGC-MANAGED");

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert - Capture the ClusterEntity and verify clusterSource is lowercase
        org.mockito.ArgumentCaptor<ClusterEntity> clusterEntityCaptor = 
                org.mockito.ArgumentCaptor.forClass(ClusterEntity.class);
        verify(nvcaClusterRepository).updateClusterConfiguration(clusterEntityCaptor.capture(), anySet());
        
        ClusterEntity capturedEntity = clusterEntityCaptor.getValue();
        assertEquals("ngc-managed", capturedEntity.getClusterSource());
    }

    @Test
    void reconfigureCluster_withInvalidClusterSource_throwsError() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setClusterSource("invalid-source");

        // Act & Assert
        IcmsBadRequestException exception = assertThrows(
                IcmsBadRequestException.class,
                () -> clusterReconfigurationService.reconfigureCluster(
                        clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("The provided invalid-source cluster source is not one of the supported sources",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_externalClusterWithWildcardInAuthorizedNcaIds_throwsError() {
        // Prepare - External cluster (no client ID) with wildcard in authorized NCA IDs
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(DUMMY_BYOC_INSTANCE_TYPE)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("AZURE")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setAuthClientId(null); // External cluster

        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                Set.of("*"), // Wildcard in authorized NCA IDs
                Set.of(gpuRequestSchema));

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Act & Assert
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class,
                () -> clusterReconfigurationService.reconfigureCluster(
                        clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("External clusters can not be publicly accessible",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_externalClusterWithoutWildcard_success() {
        // Prepare - External cluster (no client ID) with specific authorized NCA IDs
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(DUMMY_BYOC_INSTANCE_TYPE)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("AZURE")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setAuthClientId(null); // External cluster

        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                Set.of("ncaId1", "ncaId2"), // Specific NCA IDs
                Set.of(gpuRequestSchema));

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(
                clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        verify(telemetryEventClient).triggerEvent(anyList());
        verifyNoConfigSave();
    }

    @Test
    void reconfigureCluster_internalClusterWithWildcardInAuthorizedNcaIds_success() {
        // Prepare - OAuth cluster (with client ID) with wildcard is allowed
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(DUMMY_BYOC_INSTANCE_TYPE)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("AZURE")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setAuthClientId("test-client-id"); // OAuth cluster

        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                Set.of("*"), // Wildcard allowed for OAuth clusters
                Set.of(gpuRequestSchema));

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(
                clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        verify(telemetryEventClient).triggerEvent(anyList());
        verify(nvcaClusterConfigurationRepository, org.mockito.Mockito.never())
                .saveOrUpdateConfiguration(any(), any(), any());
    }

    @Test
    void reconfigureCluster_externalClusterWithEmptyAuthorizedNcaIds_success() {
        // Prepare - External cluster with empty authorized NCA IDs is allowed
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(DUMMY_BYOC_INSTANCE_TYPE)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("AZURE")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setAuthClientId(null); // External cluster
        clusterEntity.setAuthorizedNcaIds(Set.of());

        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                Set.of(), // Empty authorized NCA IDs
                Set.of(gpuRequestSchema));

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(
                clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        verify(telemetryEventClient).triggerEvent(anyList());
        verify(nvcaClusterConfigurationRepository, org.mockito.Mockito.never())
                .saveOrUpdateConfiguration(any(), any(), any());
    }

    @Test
    void reconfigureCluster_withNcaIdMismatch_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Act & Assert - NCA ID in path doesn't match NCA ID in cluster
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        "different-nca-id",
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("There exists an entry for the cluster with different ncaId. Specified ncaId ncaId, existing ncaId different-nca-id",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withPrimaryNcaIdInAuthorizedNcaIds_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                Set.of(clusterEntity.getNcaId(), "otherNcaId"), // Primary NCA ID in authorized list
                Set.of());

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Act & Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("Specified ncaId ncaId is duplicated in the set of authorized ncaIds.",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withWildcardAndOtherNcaIdsInAuthorized_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                Set.of("*", "otherNcaId"), // Wildcard with other NCA IDs
                Set.of());

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Act & Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("If specified authorized nca ids contains * then it should not have other entries.",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withEmptyStringInAuthorizedNcaIds_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                Set.of("", "validNcaId"), // Empty string in authorized NCA IDs
                Set.of());

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Act & Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("Specified authorized nca ids should not contain empty string.",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withInvalidRegion_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setRegion("invalid-region");

        // Act & Assert
        IcmsBadRequestException exception = assertThrows(IcmsBadRequestException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("The provided invalid-region region is not one of the supported regions",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withInvalidAttribute_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setAttributes(Set.of("InvalidAttribute"));

        // Mock clusterCreationService.validateAttributes to throw exception
        org.mockito.Mockito.doThrow(new IcmsBadRequestException(
                "The provided InvalidAttribute attribute is not a known attribute. Known attributes: [KataRuntimeIsolation]"))
                .when(clusterCreationService).validateAttributes(Set.of("InvalidAttribute"));

        // Act & Assert
        IcmsBadRequestException exception = assertThrows(IcmsBadRequestException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("The provided InvalidAttribute attribute is not a known attribute. Known attributes: [KataRuntimeIsolation]",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withDynamicGpuDiscoveryAndGpusProvided_throwsException() {
        // Prepare
        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(DUMMY_BYOC_INSTANCE_TYPE)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .name("AZURE")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setCapabilities(Set.of()); // Existing cluster does NOT have DynamicGPUDiscovery
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of(gpuRequestSchema)); // GPUs provided
        clusterUpdateRequest.setCapabilities(Set.of(ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY.toString())); // With DynamicGPUDiscovery

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Mock validateGpusForNewCluster to throw exception
        org.mockito.Mockito.doThrow(new PreConditionFailedException(
                "Cluster capabilities contains DynamicGPUDiscovery so gpus must not be provided"))
                .when(clusterCreationService).validateGpusForNewCluster(any(), eq(Set.of(ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY.toString())));

        // Act & Assert
        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("Cluster capabilities contains DynamicGPUDiscovery so gpus must not be provided",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withoutDynamicGpuDiscoveryAndGpusNotProvided_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setCapabilities(Set.of()); // Existing cluster does NOT have DynamicGPUDiscovery
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of()); // No GPUs provided
        clusterUpdateRequest.setCapabilities(Set.of()); // No DynamicGPUDiscovery

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Mock validateGpusForNewCluster to throw exception
        org.mockito.Mockito.doThrow(new PreConditionFailedException(
                "Cluster capabilities does not contain DynamicGPUDiscovery so gpus must be provided"))
                .when(clusterCreationService).validateGpusForNewCluster(any(), eq(Set.of()));

        // Act & Assert
        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("Cluster capabilities does not contain DynamicGPUDiscovery so gpus must be provided",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withClusterGroupNameToNewGroupTooLong_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);

        String longGroupName = "this-is-a-very-long-cluster-group-name-exceeding-32-chars";
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                longGroupName, // New cluster group name too long
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstanceEntitiesFromZone(clusterEntity.getClusterId()))
                .thenReturn(Collections.emptyList());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(), longGroupName))
                .thenReturn(Optional.empty());

        // Act & Assert
        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("The cluster group name " + longGroupName + " exceeds the limit of 32 chars",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withClusterDescriptionTooLong_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);

        String longDescription = "this-is-a-very-long-description-exceeding-32-chars";
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                "new-group-name", // New cluster group
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setClusterDescription(longDescription);

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstanceEntitiesFromZone(clusterEntity.getClusterId()))
                .thenReturn(Collections.emptyList());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(), "new-group-name"))
                .thenReturn(Optional.empty());

        // Act & Assert
        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("The cluster description " + longDescription + " exceeds the limit of 32 chars",
                exception.getBody().getDetail());
    }

    @Test
    void reconfigureCluster_withConfigMapsRemoved_deletesConfiguration() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setClusterConfigurations(null);
        clusterUpdateRequest.setClusterConfigurationFiles(null);

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Mock existing configuration in DB
        ClusterConfigurationByClusterIdEntity existingConfig =
                new ClusterConfigurationByClusterIdEntity(
                        clusterEntity.getClusterId(),
                        Map.of("existingKey", "existingValue"),
                        Map.of("existingFile", "existingContent")
                );
        when(nvcaClusterConfigurationRepository.findByClusterId(clusterEntity.getClusterId()))
                .thenReturn(Optional.of(existingConfig));

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert - Should delete existing configuration
        verify(nvcaClusterConfigurationRepository).deleteByClusterId(clusterEntity.getClusterId());
    }

    @Test
    void reconfigureCluster_withConfigMapsRemovedButNoExistingConfig_doesNotDelete() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setClusterConfigurations(null);
        clusterUpdateRequest.setClusterConfigurationFiles(null);

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Mock no existing configuration in DB
        when(nvcaClusterConfigurationRepository.findByClusterId(clusterEntity.getClusterId()))
                .thenReturn(Optional.empty());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert - Should not attempt to delete
        verify(nvcaClusterConfigurationRepository, org.mockito.Mockito.never())
                .deleteByClusterId(any());
    }

    @Test
    void reconfigureCluster_withNullAuthorizedNcaIds_setsEmptySet() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                null, // Null authorized NCA IDs
                Set.of());

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert - Capture the ClusterEntity and verify authorizedNcaIds is empty set
        org.mockito.ArgumentCaptor<ClusterEntity> clusterEntityCaptor = 
                org.mockito.ArgumentCaptor.forClass(ClusterEntity.class);
        verify(nvcaClusterRepository).updateClusterConfiguration(clusterEntityCaptor.capture(), anySet());
        
        ClusterEntity capturedEntity = clusterEntityCaptor.getValue();
        Assertions.assertNotNull(capturedEntity.getAuthorizedNcaIds());
        Assertions.assertTrue(capturedEntity.getAuthorizedNcaIds().isEmpty());
    }

    @Test
    void reconfigureCluster_withNullAttributes_setsEmptySet() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setAttributes(null); // Null attributes

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert - Capture the ClusterEntity and verify attributes is empty set
        org.mockito.ArgumentCaptor<ClusterEntity> clusterEntityCaptor = 
                org.mockito.ArgumentCaptor.forClass(ClusterEntity.class);
        verify(nvcaClusterRepository).updateClusterConfiguration(clusterEntityCaptor.capture(), anySet());
        
        ClusterEntity capturedEntity = clusterEntityCaptor.getValue();
        Assertions.assertNotNull(capturedEntity.getAttributes());
        Assertions.assertTrue(capturedEntity.getAttributes().isEmpty());
    }

    @Test
    void reconfigureCluster_withRegionUpperCase_convertsToLowerCase() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                clusterEntity.getClusterGroupName(),
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());
        clusterUpdateRequest.setRegion("US-EAST-1"); // Uppercase region

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterConfiguration(any(), anySet());

        // Act
        clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                clusterEntity.getNcaId(),
                clusterEntity.getClusterId(),
                new HashMap<>());

        // Assert - Capture the ClusterEntity passed to updateClusterConfiguration and verify region is lowercase
        org.mockito.ArgumentCaptor<ClusterEntity> clusterEntityCaptor = 
                org.mockito.ArgumentCaptor.forClass(ClusterEntity.class);
        verify(nvcaClusterRepository).updateClusterConfiguration(clusterEntityCaptor.capture(), anySet());
        
        ClusterEntity capturedEntity = clusterEntityCaptor.getValue();
        assertEquals("us-east-1", capturedEntity.getRegion());
    }

    @Test
    void reconfigureCluster_toExistingClusterGroupRegisteredWithNvca1_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion("dummy_version");
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);

        String newGroupName = "existing-nvca1-group";
        ClusterUpdateRequest clusterUpdateRequest = dummyClusterUpdateRequest(
                newGroupName,
                clusterEntity.getAuthorizedNcaIds(),
                Set.of());

        ClusterGroupsByAccountEntity nvca1ClusterGroup = ClusterGroupsByAccountEntity.builder()
                .key(ClusterGroupsByAccountKey.builder()
                        .clusterGroupName(newGroupName)
                        .ncaId(clusterEntity.getNcaId())
                        .build())
                .clusterGroupId("test-group-id")
                .gpus(Set.of(com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt.builder()
                        .name("GPU")
                        .build())) // NVCA 1.0 group has gpus
                .build();

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstanceEntitiesFromZone(clusterEntity.getClusterId()))
                .thenReturn(Collections.emptyList());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(), newGroupName))
                .thenReturn(Optional.of(nvca1ClusterGroup));

        // Mock validateClusterGroupForNvca2Flow to throw exception
        org.mockito.Mockito.doThrow(new PreConditionFailedException(
                "This cluster group " + newGroupName + " can not be used since it was already registered with old flow"))
                .when(clusterCreationService).validateClusterGroupForNvca2Flow(nvca1ClusterGroup);

        // Act & Assert
        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                () -> clusterReconfigurationService.reconfigureCluster(clusterUpdateRequest,
                        clusterEntity.getNcaId(),
                        clusterEntity.getClusterId(),
                        new HashMap<>()));

        assertEquals("This cluster group " + newGroupName + " can not be used since it was already registered with old flow",
                exception.getBody().getDetail());
    }

    // ==================== Private helper methods ====================

    private ClusterUpdateRequest dummyClusterUpdateRequest(
            String clusterGroupName,
            Set<String> authorizedNcaIds,
            Set<GpuRequestSchema> gpuRequestSchemas) {
        return ClusterUpdateRequest.builder()
                .clusterDescription("cluster_description")
                .clusterGroupName(clusterGroupName)
                .authorizedNCAIds(authorizedNcaIds)
                .gpus(gpuRequestSchemas)
                .cloudProvider(ClusterProviderEnum.GDN)
                .capabilities(Set.of(ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY.toString()))
                .attributes(Set.of("KataRuntimeIsolation"))
                .nvcaVersion("dummy_version")
                .region(ClusterRegion.US_EAST_1.toString())
                .build();
    }

    private InstanceByZoneEntity getDummyInstanceByZoneEntity() {
        return InstanceByZoneEntity.builder()
                .key(InstanceByZoneKey.builder()
                             .instanceId(DUMMY_INSTANCE_ID)
                             .zone(DUMMY_ZONE)
                             .truncatedTs(Instant.now())
                             .build())
                .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .backend("AWS")
                .ncaId(DUMMY_NON_BYOC_NCA_ID)
                .requestId(DUMMY_REQUEST_ID)
                .gpu(DUMMY_GPU_NAME)
                .instanceStateCode(16)
                .instanceStateName(SpotInstanceInternalState.RUNNING)
                .customer(DUMMY_CUSTOMER_1)
                .imageId(DUMMY_CONTAINER_IMAGE)
                .requestState(SpotInstanceRequestState.ACTIVE)
                .requestStatusCode(SpotInstanceStatus.FULFILLED)
                .requestStatusUpdateTime(Instant.now())
                .updateTimestamp(Instant.now())
                .build();
    }

    private void verifyNoConfigSave() {
        verify(nvcaClusterConfigurationRepository, org.mockito.Mockito.never())
                .saveOrUpdateConfiguration(any(), any(), any());
    }
}
