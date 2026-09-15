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

import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getClusterIdFromAuthClientId;
import static com.nvidia.icms.util.TestUtil.DUMMY_AUTH_CLIENT_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_GROUP_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_LONG_BYOC_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_LONG_BYOC_CLUSTER_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_LONG_CLUSTER_DESCRIPTION;
import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;
import static com.nvidia.icms.util.TestUtil.getDummyClusterGroupsByAccountEntity;
import static com.nvidia.icms.util.TestUtil.getDummyGpuRequestSchema;
import static com.nvidia.icms.util.TestUtil.getDummyInstanceType;
import static com.nvidia.icms.util.TestUtil.getDummyInstanceTypeRequestSchema;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.atLeastOnce;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.cfg.DateTimeFeature;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.inbound.rest.converters.NvcaRequestSchemaToUdtConverter;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterCapabilitiesEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.GpuRequestSchema;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeRequestSchema;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationResponse;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterConfigurationRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.byoc.nvca.ClusterOidcIdentityService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.AuthUtils;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ClusterCreationServiceTest {

    @Mock
    ClusterRepository clusterRepository;

    @Mock
    NvcaClusterRepository nvcaClusterRepository;

    @Mock
    TelemetryEventClient telemetryEventClient;

    @Mock
    AppAuditService auditService;
    
    @Mock
    NvcaConfigurationProperties nvcaConfigurationProperties;

    @Mock
    NvcaClusterConfigurationRepository nvcaClusterConfigurationRepository;

    @Mock
    ClusterOidcIdentityService clusterOidcIdentityService;

    ObjectMapper objectMapper;

    private ClusterCreationService clusterCreationService;

    @BeforeEach
    void init() {
        objectMapper = JsonMapper.builder()
                .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                .disable(DateTimeFeature.WRITE_DATES_AS_TIMESTAMPS)
                .build();

        clusterCreationService =
                new ClusterCreationService(objectMapper, nvcaClusterRepository, clusterRepository,
                                           telemetryEventClient, auditService, nvcaConfigurationProperties,
                                           nvcaClusterConfigurationRepository, clusterOidcIdentityService);
    }

    @Test
    void clusterCreation_createsNewClusterInNewClusterGroup_Success() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        ClusterCreationResponse clusterCreationResponse = clusterCreationService
                .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        assertEquals(getClusterIdFromAuthClientId(DUMMY_AUTH_CLIENT_ID),
                     clusterCreationResponse.getClusterId());
        assertNotNull(clusterCreationResponse.getClusterGroupId());
        verify(nvcaConfigurationProperties, atLeastOnce()).isAuthorizedNcaIdRegexValidationEnabled();
        verify(nvcaClusterConfigurationRepository, never()).saveOrUpdateConfiguration(any(), any(), any());
    }

    @Test
    void clusterCreation_persistsAuthClientId_fromOAuthClientIdRequestField() {

        // Prepare: standard happy-path setup, plus a captor on the saved entity to verify
        // that toClusterEntity() copies the request's oAuthClientId field into auth_client_id.
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterCreationService.clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                               new HashMap<>());

        // Assert: the persisted ClusterEntity carries the request's oAuthClientId value on authClientId
        org.mockito.ArgumentCaptor<com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity> savedCaptor =
                org.mockito.ArgumentCaptor.forClass(
                        com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity.class);
        verify(nvcaClusterRepository).saveClusterInfo(savedCaptor.capture());
        com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity saved = savedCaptor.getValue();
        assertEquals(clusterCreationRequest.getOAuthClientId(), saved.getAuthClientId());
    }

    @Test
    void clusterCreation_persistsNullAuthClientId_whenOAuthClientIdIsNull() {
        // Prepare: oAuthClientId omitted from request -> null must be preserved on authClientId
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        clusterCreationRequest.setOAuthClientId(null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterCreationService.clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                               new HashMap<>());

        // Assert: authClientId is null (no normalization)
        org.mockito.ArgumentCaptor<com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity> savedCaptor =
                org.mockito.ArgumentCaptor.forClass(
                        com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity.class);
        verify(nvcaClusterRepository).saveClusterInfo(savedCaptor.capture());
        com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity saved = savedCaptor.getValue();
        Assertions.assertNull(saved.getAuthClientId());
    }

    @Test
    void clusterCreation_createsNewClusterInNewClusterGroup_withInvalidAuthorizedNcaIdFormat_throwsError() {
        // Prepare
        when(nvcaConfigurationProperties.isAuthorizedNcaIdRegexValidationEnabled()).thenReturn(true);

        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("invalid"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(), true))
                .thenReturn(Optional.empty());

        // Act
        IcmsBadRequestException exception = assertThrows(
                IcmsBadRequestException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                String.format("Invalid authorizedNCAIds '%s'. It must match regex %s",
                              "invalid",
                              ClusterCreationService.AUTHORIZED_NCA_ID_REGEX),
                exception.getBody().getDetail());
        verify(nvcaConfigurationProperties, atLeastOnce()).isAuthorizedNcaIdRegexValidationEnabled();
        verify(nvcaClusterRepository, never()).saveClusterInfo(any());
    }

    @Test
    void clusterCreation_createsNewClusterInNewClusterGroup_withoutSsaClientGeneratesClusetrId_Success() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        clusterCreationRequest.setOAuthClientId(null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        ClusterCreationResponse clusterCreationResponse = clusterCreationService
                .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        assertNotNull(clusterCreationResponse.getClusterId());
        assertNotNull(clusterCreationResponse.getClusterGroupId());
        verify(nvcaClusterConfigurationRepository, never()).saveOrUpdateConfiguration(any(), any(), any());
    }

    @Test
    void clusterCreation_withConfigMaps_savesConfiguration() {
        // Prepare
        ClusterCreationRequest request = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("n1"), null);
        request.setClusterConfigurations(Map.of("k1", "v1"));
        request.setClusterConfigurationFiles(Map.of("f1", "base64"));

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(request.getOAuthClientId(), true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterCreationService.clusterCreation(request, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        verify(nvcaClusterConfigurationRepository).saveOrUpdateConfiguration(
                any(), // clusterId
                org.mockito.ArgumentMatchers.eq(Map.of("k1", "v1")),
                org.mockito.ArgumentMatchers.eq(Map.of("f1", "base64"))
        );
    }

    @Test
    void clusterCreation_withoutConfigMaps_doesNotSaveConfiguration() {
        // Prepare
        ClusterCreationRequest request = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("n1"), null);
        request.setClusterConfigurations(null);
        request.setClusterConfigurationFiles(null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(request.getOAuthClientId(), true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterCreationService.clusterCreation(request, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        verify(nvcaClusterConfigurationRepository, never()).saveOrUpdateConfiguration(any(), any(), any());
    }

    @Test
    void clusterCreation_createsNewCluster_withClusterAlreadyExist_throwsError() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(clusterCreationRequest.getOAuthClientId());
        clusterEntity.setClusterName(DUMMY_BYOC_CLUSTER_NAME);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.of(clusterEntity));

        // Act
        IcmsBadRequestException exception = assertThrows(
                IcmsBadRequestException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("Cluster with clusterName cluster-name already exists",
                                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_createsNewClusterInNewClusterGroup_withSsaClientIdUsedByAnotherCluster_throwsError() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(clusterCreationRequest.getOAuthClientId());
        clusterEntity.setClusterName(DUMMY_BYOC_CLUSTER_NAME + "_1");
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.of(clusterEntity));

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("Provided dummy_client_id client ID is already registered "
                                        + "with another cluster", exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_createsNewClusterInNewClusterGroup_withPrimaryNcaIdInAuthId_throwsError() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of(DUMMY_BYOC_NCA_ID, "ncaId2"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("Specified ncaId dummy_byoc_nca_id is duplicated in the set "
                                        + "of authorized ncaIds.", exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_createsNewCluster_withWildcardAndOtherNcaIdInAuth_throwsError() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("*", "ncaId2"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("If specified authorized nca ids contains * then it should "
                                        + "not have other entries.",
                                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_createsNewCluster_withEmptyNcaIdInAuthIds_throwsError() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("", "ncaId2"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("Specified authorized nca ids should not "
                                        + "contain empty string.", exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_createsNewClusterInNewClusterGroup_withoutDynamicGpuDiscovery_Success() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), Set.of(getDummyGpuRequestSchema()));
        clusterCreationRequest.setCapabilities(Set.of());

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());

        // Act
        ClusterCreationResponse clusterCreationResponse = clusterCreationService
                .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        assertEquals(getClusterIdFromAuthClientId(DUMMY_AUTH_CLIENT_ID),
                     clusterCreationResponse.getClusterId());
        assertNotNull(clusterCreationResponse.getClusterGroupId());
        verify(nvcaClusterConfigurationRepository, never()).saveOrUpdateConfiguration(any(), any(), any());
    }

    @Test
    void clusterCreation_createsNewCluster_withoutMultipleDefaultInstanceTypes_throwsError() {

        // Prepare
        GpuRequestSchema gpuRequestSchema = getDummyGpuRequestSchema();
        InstanceTypeRequestSchema instanceType1 = getDummyInstanceTypeRequestSchema();
        InstanceTypeRequestSchema instanceType2 = getDummyInstanceTypeRequestSchema();
        instanceType2.setName("dummy_name_2");
        gpuRequestSchema.setInstanceTypes(Set.of(instanceType1, instanceType2));

        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), Set.of(gpuRequestSchema));
        clusterCreationRequest.setCapabilities(Set.of());

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("There should be exactly one default instance type for each gpu, "
                                        + "however gpu dummy_gpu is having 2 default instance types",
                                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_withDynamicGpuDiscoveryAndGpusProvided_throwsPreConditionFailed() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), Set.of(getDummyGpuRequestSchema()));

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());

        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("Cluster capabilities contains DynamicGPUDiscovery so gpus "
                                        + "must not be provided",
                                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_withBothDynamicGpuDiscoveryAndGpusNotProvided_throwsPreConditionFailed() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), Set.of(getDummyGpuRequestSchema()));
        clusterCreationRequest.setGpus(Set.of());
        clusterCreationRequest.setCapabilities(Set.of());

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());

        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("Cluster capabilities does not contain DynamicGPUDiscovery "
                                        + "so gpus must be provided",
                                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_withClusterLongClusterName_throwsError() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_LONG_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_LONG_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());

        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("The cluster name cluster-name-aaaaaaaaaaaaaaaaaaaa exceeds "
                                        + "the limit of 32 chars",
                                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_withClusterLongClusterGroupName_throwsError() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_LONG_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_LONG_BYOC_CLUSTER_GROUP_NAME)).thenReturn(
                Optional.empty());

        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("The cluster group name dummy_long_group_name_aaaaaaaaaaa "
                                        + "exceeds the limit of 32 chars",
                                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_withClusterLongClusterDescription_throwsError() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        clusterCreationRequest.setClusterDescription(DUMMY_LONG_CLUSTER_DESCRIPTION);
        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());

        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("The cluster description dummy_long_description_aaaaaaaaaa "
                                        + "exceeds the limit of 32 chars",
                                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_inExistingClusterGroup_Success() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(
                Optional.of(getDummyClusterGroupsByAccountEntity(DUMMY_BYOC_CLUSTER_GROUP_NAME,
                                                                 DUMMY_BYOC_NCA_ID)));
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());

        // Act
        ClusterCreationResponse clusterCreationResponse = clusterCreationService
                .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        assertEquals(getClusterIdFromAuthClientId(DUMMY_AUTH_CLIENT_ID),
                     clusterCreationResponse.getClusterId());
        assertEquals(DUMMY_CLUSTER_GROUP_ID, clusterCreationResponse.getClusterGroupId());
        verify(nvcaConfigurationProperties, atLeastOnce()).isAuthorizedNcaIdRegexValidationEnabled();
    }

    @Test
    void clusterCreation_inExistingClusterGroup_withInvalidAuthorizedNcaIdFormat_throwsError() {
        // Prepare
        when(nvcaConfigurationProperties.isAuthorizedNcaIdRegexValidationEnabled()).thenReturn(true);

        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("invalid"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(
                Optional.of(getDummyClusterGroupsByAccountEntity(DUMMY_BYOC_CLUSTER_GROUP_NAME,
                                                                 DUMMY_BYOC_NCA_ID)));
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(), true))
                .thenReturn(Optional.empty());

        // Act
        IcmsBadRequestException exception = assertThrows(
                IcmsBadRequestException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                String.format("Invalid authorizedNCAIds '%s'. It must match regex %s",
                              "invalid",
                              ClusterCreationService.AUTHORIZED_NCA_ID_REGEX),
                exception.getBody().getDetail());
        verify(nvcaConfigurationProperties, atLeastOnce()).isAuthorizedNcaIdRegexValidationEnabled();
        verify(nvcaClusterRepository, never()).saveClusterInfo(any());
    }

    @Test
    void clusterCreation_inExistingClusterGroupInNvca1flow_throwsError() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());

        ClusterGroupsByAccountEntity clusterGroupsByAccountEntity =
                getDummyClusterGroupsByAccountEntity(DUMMY_BYOC_CLUSTER_GROUP_NAME,
                                                     DUMMY_BYOC_NCA_ID);
        clusterGroupsByAccountEntity.setGpus(Set.of(
                GpuUdt.builder().instanceTypes(Set.of(getDummyInstanceType()))
                        .name(DUMMY_GPU_NAME).build()));
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(
                Optional.of(clusterGroupsByAccountEntity));

        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        Assertions.assertEquals("This cluster group dummy_group_name can not be used since "
                                        + "it was already registered with old flow",
                                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_withInvalidClusterNameFormat_throwsError() {

        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                "_cluster_with_underscore", DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);

        // Act
        IcmsBadRequestException exception = assertThrows(
                IcmsBadRequestException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                new HashMap<>()));

        // Assert
        Assertions.assertEquals("Provided clusterName _cluster_with_underscore is not with " +
                        "RFC1123 subdomain format [a-z0-9]([-a-z0-9]*[a-z0-9])?(\\\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*",
                exception.getBody().getDetail());
    }

    @Test
    void validateAndGetClusterSource_WhenSourceIsNull_ReturnsNgcManaged() {
        // Act
        String result = ClusterCreationService.validateAndGetClusterSource(null);

        // Assert
        assertEquals("ngc-managed", result);
    }

    @Test
    void validateAndGetClusterSource_WhenSourceIsEmpty_ReturnsNgcManaged() {
        // Act
        String result = ClusterCreationService.validateAndGetClusterSource("");

        // Assert
        assertEquals("ngc-managed", result);
    }

    @Test
    void validateAndGetClusterSource_WhenSourceIsValid_ReturnsLowerCaseVersion() {
        // Act
        String result = ClusterCreationService.validateAndGetClusterSource("NGC-MANAGED");

        // Assert
        assertEquals("ngc-managed", result);
    }

    @Test
    void validateAndGetClusterSource_WhenSourceIsInvalid_ThrowsException() {
        // Act & Assert
        IcmsBadRequestException exception = assertThrows(IcmsBadRequestException.class,
                () -> ClusterCreationService.validateAndGetClusterSource("invalid-source"));

        // Assert
        assertEquals("The provided invalid-source cluster source is not one of the supported sources",
                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_withNullClusterSource_usesDefaultNgcManaged() {
        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        clusterCreationRequest.setClusterSource(null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        ClusterCreationResponse clusterCreationResponse = clusterCreationService
                .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        assertEquals(getClusterIdFromAuthClientId(DUMMY_AUTH_CLIENT_ID),
                     clusterCreationResponse.getClusterId());
        assertNotNull(clusterCreationResponse.getClusterGroupId());
    }

    @Test
    void clusterCreation_withEmptyClusterSource_usesDefaultNgcManaged() {
        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        clusterCreationRequest.setClusterSource("");

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        ClusterCreationResponse clusterCreationResponse = clusterCreationService
                .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        assertEquals(getClusterIdFromAuthClientId(DUMMY_AUTH_CLIENT_ID),
                     clusterCreationResponse.getClusterId());
        assertNotNull(clusterCreationResponse.getClusterGroupId());
    }

    @Test
    void clusterCreation_withValidClusterSource_convertsToLowerCase() {
        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        clusterCreationRequest.setClusterSource("NGC-MANAGED");

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        ClusterCreationResponse clusterCreationResponse = clusterCreationService
                .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert - Capture ClusterEntity and verify clusterSource is lowercase
        org.mockito.ArgumentCaptor<ClusterEntity> clusterEntityCaptor = 
                org.mockito.ArgumentCaptor.forClass(ClusterEntity.class);
        verify(nvcaClusterRepository).saveClusterInfo(clusterEntityCaptor.capture());
        
        ClusterEntity capturedEntity = clusterEntityCaptor.getValue();
        assertEquals("ngc-managed", capturedEntity.getClusterSource());
        assertEquals(getClusterIdFromAuthClientId(DUMMY_AUTH_CLIENT_ID),
                     clusterCreationResponse.getClusterId());
        assertNotNull(clusterCreationResponse.getClusterGroupId());
    }

    @Test
    void clusterCreation_withInvalidClusterSource_throwsError() {
        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        clusterCreationRequest.setClusterSource("invalid-source");

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());

        // Act
        IcmsBadRequestException exception = assertThrows(
                IcmsBadRequestException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        assertEquals("The provided invalid-source cluster source is not one of the supported sources",
                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_externalClusterWithWildcardInAuthorizedNcaIds_throwsError() {
        // Prepare - External cluster (no client ID) with wildcard in authorized NCA IDs
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("*"), null);
        clusterCreationRequest.setOAuthClientId(null); // External cluster

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class, () -> clusterCreationService
                        .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID,
                                         new HashMap<>()));

        // Assert
        assertEquals("External clusters can not be publicly accessible",
                exception.getBody().getDetail());
    }

    @Test
    void clusterCreation_externalClusterWithoutWildcard_success() {
        // Prepare - External cluster (no client ID) with specific authorized NCA IDs
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        clusterCreationRequest.setOAuthClientId(null); // External cluster

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        ClusterCreationResponse clusterCreationResponse = clusterCreationService
                .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        assertNotNull(clusterCreationResponse.getClusterId());
        assertNotNull(clusterCreationResponse.getClusterGroupId());
    }

    @Test
    void clusterCreation_internalClusterWithWildcardInAuthorizedNcaIds_success() {
        // Prepare - cluster (with client ID) with wildcard is allowed
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("*"), null);
        clusterCreationRequest.setOAuthClientId(DUMMY_AUTH_CLIENT_ID);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                                                         true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        ClusterCreationResponse clusterCreationResponse = clusterCreationService
                .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        assertEquals(getClusterIdFromAuthClientId(DUMMY_AUTH_CLIENT_ID),
                     clusterCreationResponse.getClusterId());
        assertNotNull(clusterCreationResponse.getClusterGroupId());
    }

    @Test
    void clusterCreation_externalClusterWithEmptyAuthorizedNcaIds_success() {
        // Prepare - External cluster with empty authorized NCA IDs is allowed
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of(), null);
        clusterCreationRequest.setOAuthClientId(null); // External cluster

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        ClusterCreationResponse clusterCreationResponse = clusterCreationService
                .clusterCreation(clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>());

        // Assert
        assertNotNull(clusterCreationResponse.getClusterId());
        assertNotNull(clusterCreationResponse.getClusterGroupId());
    }

    // ==================== Tests for getClusterIdFromAuthClientId ====================

    @Test
    void getClusterIdFromAuthClientId_returnsConsistentUuid() {
        // Act
        String clusterId1 = ClusterCreationService.getClusterIdFromAuthClientId("test-client-id");
        String clusterId2 = ClusterCreationService.getClusterIdFromAuthClientId("test-client-id");

        // Assert
        assertEquals(clusterId1, clusterId2);
        assertNotNull(UUID.fromString(clusterId1)); // Valid UUID
    }

    @Test
    void getClusterIdFromAuthClientId_differentInputsProduceDifferentOutputs() {
        // Act
        String clusterId1 = ClusterCreationService.getClusterIdFromAuthClientId("client-id-1");
        String clusterId2 = ClusterCreationService.getClusterIdFromAuthClientId("client-id-2");

        // Assert
        Assertions.assertNotEquals(clusterId1, clusterId2);
    }

    // ==================== Tests for validateAttributes ====================

    @Test
    void validateAttributes_withValidAttribute_success() {
        // Prepare
        when(nvcaConfigurationProperties.getClusterAttributes())
                .thenReturn(List.of("attribute1", "attribute2"));

        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> clusterCreationService.validateAttributes(Set.of("attribute1")));
    }

    @Test
    void validateAttributes_withInvalidAttribute_throwsException() {
        // Prepare
        when(nvcaConfigurationProperties.getClusterAttributes())
                .thenReturn(List.of("attribute1", "attribute2"));

        // Act
        IcmsBadRequestException exception = assertThrows(
                IcmsBadRequestException.class,
                () -> clusterCreationService.validateAttributes(Set.of("invalidAttribute")));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("invalidAttribute attribute is not a known attribute"));
    }

    @Test
    void validateAttributes_withEmptySet_success() {
        // Prepare
        when(nvcaConfigurationProperties.getClusterAttributes())
                .thenReturn(List.of("attribute1"));

        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> clusterCreationService.validateAttributes(Set.of()));
    }

    // ==================== Tests for validateRegion ====================

    @Test
    void validateRegion_withValidRegion_success() {
        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> ClusterCreationService.validateRegion("us-east-1"));
    }

    @Test
    void validateRegion_withValidRegionUpperCase_success() {
        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> ClusterCreationService.validateRegion("US-WEST-2"));
    }

    @Test
    void validateRegion_withInvalidRegion_throwsException() {
        // Act
        IcmsBadRequestException exception = assertThrows(
                IcmsBadRequestException.class,
                () -> ClusterCreationService.validateRegion("invalid-region"));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("invalid-region region is not one of the supported regions"));
    }

    // ==================== Tests for validateAuthorizedNcaIds ====================

    @Test
    void validateAuthorizedNcaIds_matchingIds_success() {
        // Prepare
        Set<String> expected = new HashSet<>(Set.of("ncaId1", "ncaId2"));
        Set<String> actual = new HashSet<>(Set.of("ncaId1", "ncaId2"));

        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> ClusterCreationService.validateAuthorizedNcaIds(expected, actual));
    }

    @Test
    void validateAuthorizedNcaIds_mismatchedIds_throwsException() {
        // Prepare
        Set<String> expected = new HashSet<>(Set.of("ncaId1", "ncaId2"));
        Set<String> actual = new HashSet<>(Set.of("ncaId1", "ncaId3"));

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class,
                () -> ClusterCreationService.validateAuthorizedNcaIds(expected, actual));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("Authorized nca ids are not matching"));
    }

    @Test
    void validateAuthorizedNcaIds_differentSizes_throwsException() {
        // Prepare
        Set<String> expected = new HashSet<>(Set.of("ncaId1", "ncaId2"));
        Set<String> actual = new HashSet<>(Set.of("ncaId1"));

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class,
                () -> ClusterCreationService.validateAuthorizedNcaIds(expected, actual));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("Authorized nca ids are not matching"));
    }

    // ==================== Tests for isGpuSharedBetweenClusters ====================

    @Test
    void isGpuSharedBetweenClusters_withSharedGpu_returnsTrue() {
        // Prepare
        InstanceTypeV5Udt instanceType = InstanceTypeV5Udt.builder()
                .name("instance1")
                .value("instance-value")
                .description("instance description")
                .isDefault(true)
                .cpuCores(4)
                .gpuCount(1)
                .systemMemory("16GB")
                .gpuMemory("8GB")
                .cpuArch("x86_64")
                .os("linux")
                .driverVersion("525.0")
                .storage("100GB")
                .nodeType("COMPUTE")
                .build();
        GpuV5Udt gpu1 = GpuV5Udt.builder()
                .name("shared-gpu")
                .instanceTypes(Set.of(instanceType))
                .build();
        GpuV5Udt gpu2 = GpuV5Udt.builder()
                .name("shared-gpu")
                .instanceTypes(Set.of(instanceType))
                .build();

        // Act
        boolean result = ClusterCreationService.isGpuSharedBetweenClusters(
                Set.of(gpu1), Set.of(gpu2));

        // Assert
        Assertions.assertTrue(result);
    }

    @Test
    void isGpuSharedBetweenClusters_withNoSharedGpu_returnsFalse() {
        // Prepare
        InstanceTypeV5Udt instanceType = InstanceTypeV5Udt.builder()
                .name("instance1")
                .isDefault(true)
                .build();
        GpuV5Udt gpu1 = GpuV5Udt.builder()
                .name("gpu1")
                .instanceTypes(Set.of(instanceType))
                .build();
        GpuV5Udt gpu2 = GpuV5Udt.builder()
                .name("gpu2")
                .instanceTypes(Set.of(instanceType))
                .build();

        // Act
        boolean result = ClusterCreationService.isGpuSharedBetweenClusters(
                Set.of(gpu1), Set.of(gpu2));

        // Assert
        Assertions.assertFalse(result);
    }

    @Test
    void isGpuSharedBetweenClusters_withEmptyProvidedGpus_returnsFalse() {
        // Prepare
        InstanceTypeV5Udt instanceType = InstanceTypeV5Udt.builder()
                .name("instance1")
                .isDefault(true)
                .build();
        GpuV5Udt gpu = GpuV5Udt.builder()
                .name("gpu")
                .instanceTypes(Set.of(instanceType))
                .build();

        // Act
        boolean result = ClusterCreationService.isGpuSharedBetweenClusters(
                Set.of(), Set.of(gpu));

        // Assert
        Assertions.assertFalse(result);
    }

    // ==================== Tests for defaultGpuValidation ====================

    @Test
    void defaultGpuValidation_withValidGpu_success() {
        // Prepare
        InstanceTypeV5Udt instanceType = InstanceTypeV5Udt.builder()
                .name("instance1")
                .isDefault(true)
                .build();
        GpuV5Udt gpu = GpuV5Udt.builder()
                .name("valid-gpu")
                .instanceTypes(Set.of(instanceType))
                .build();

        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> ClusterCreationService.defaultGpuValidation(Set.of(gpu)));
    }

    @Test
    void defaultGpuValidation_withNoDefaultInstanceType_throwsException() {
        // Prepare
        InstanceTypeV5Udt instanceType = InstanceTypeV5Udt.builder()
                .name("instance1")
                .isDefault(false)
                .build();
        GpuV5Udt gpu = GpuV5Udt.builder()
                .name("test-gpu")
                .instanceTypes(Set.of(instanceType))
                .build();

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class,
                () -> ClusterCreationService.defaultGpuValidation(Set.of(gpu)));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("There should be exactly one default instance type"));
    }

    @Test
    void defaultGpuValidation_withMultipleDefaultInstanceTypes_throwsException() {
        // Prepare
        InstanceTypeV5Udt instanceType1 = InstanceTypeV5Udt.builder()
                .name("instance1")
                .isDefault(true)
                .build();
        InstanceTypeV5Udt instanceType2 = InstanceTypeV5Udt.builder()
                .name("instance2")
                .isDefault(true)
                .build();
        GpuV5Udt gpu = GpuV5Udt.builder()
                .name("test-gpu")
                .instanceTypes(Set.of(instanceType1, instanceType2))
                .build();

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class,
                () -> ClusterCreationService.defaultGpuValidation(Set.of(gpu)));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("2 default instance types"));
    }

    @Test
    void defaultGpuValidation_withEmptyGpuSet_success() {
        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> ClusterCreationService.defaultGpuValidation(Set.of()));
    }

    // ==================== Tests for validateAuthorizedNcaIdsForExternalCluster ====================

    @Test
    void validateAuthorizedNcaIdsForExternalCluster_externalWithWildcard_throwsException() {
        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class,
                () -> ClusterCreationService.validateAuthorizedNcaIdsForExternalCluster(
                        Set.of("*"), null));

        // Assert
        assertEquals("External clusters can not be publicly accessible",
                exception.getBody().getDetail());
    }

    @Test
    void validateAuthorizedNcaIdsForExternalCluster_externalWithoutWildcard_success() {
        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> ClusterCreationService.validateAuthorizedNcaIdsForExternalCluster(
                Set.of("ncaId1"), null));
    }

    @Test
    void validateAuthorizedNcaIdsForExternalCluster_ssaWithWildcard_success() {
        // Act & Assert - Should not throw (OAuth cluster with wildcard is allowed)
        Assertions.assertDoesNotThrow(() -> ClusterCreationService.validateAuthorizedNcaIdsForExternalCluster(
                Set.of("*"), "oauth-client-id"));
    }

    @Test
    void validateAuthorizedNcaIdsForExternalCluster_nullAuthorizedNcaIds_success() {
        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> ClusterCreationService.validateAuthorizedNcaIdsForExternalCluster(null, null));
    }

    // ==================== Tests for validateClusterProvider ====================

    @Test
    void validateClusterProvider_matchingProvider_success() {
        // Prepare
        ClusterEntity existingCluster = getDummyClusterEntity();
        existingCluster.setClusterProvider(ClusterProviderEnum.GDN);
        when(clusterRepository.getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID))
                .thenReturn(Set.of(existingCluster));

        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> clusterCreationService.validateClusterProvider(DUMMY_CLUSTER_GROUP_ID, ClusterProviderEnum.GDN));
    }

    @Test
    void validateClusterProvider_mismatchedProvider_throwsException() {
        // Prepare
        ClusterEntity existingCluster = getDummyClusterEntity();
        existingCluster.setClusterProvider(ClusterProviderEnum.AWS);
        when(clusterRepository.getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID))
                .thenReturn(Set.of(existingCluster));

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class,
                () -> clusterCreationService.validateClusterProvider(
                        DUMMY_CLUSTER_GROUP_ID, ClusterProviderEnum.GDN));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("Specified cloudProvider GDN is different than cloudProvider of clusterGroup"));
    }

    @Test
    void validateClusterProvider_emptyClusterGroup_success() {
        // Prepare
        when(clusterRepository.getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID))
                .thenReturn(Set.of());

        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> clusterCreationService.validateClusterProvider(DUMMY_CLUSTER_GROUP_ID, ClusterProviderEnum.GDN));
    }

    // ==================== Tests for validateGpusForNewCluster ====================

    @Test
    void validateGpusForNewCluster_dynamicGpuWithEmptyGpus_success() {
        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> clusterCreationService.validateGpusForNewCluster(
                Set.of(),
                Set.of(ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY.toString())));
    }

    @Test
    void validateGpusForNewCluster_noDynamicGpuWithGpus_success() {
        // Prepare
        InstanceTypeV5Udt instanceType = InstanceTypeV5Udt.builder()
                .name("instance1")
                .isDefault(true)
                .build();
        GpuV5Udt gpu = GpuV5Udt.builder()
                .name("valid-gpu")
                .instanceTypes(Set.of(instanceType))
                .build();

        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> clusterCreationService.validateGpusForNewCluster(Set.of(gpu), Set.of()));
    }

    @Test
    void validateGpusForNewCluster_dynamicGpuWithGpus_throwsException() {
        // Prepare
        InstanceTypeV5Udt instanceType = InstanceTypeV5Udt.builder()
                .name("instance1")
                .isDefault(true)
                .build();
        GpuV5Udt gpu = GpuV5Udt.builder()
                .name("valid-gpu")
                .instanceTypes(Set.of(instanceType))
                .build();

        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> clusterCreationService.validateGpusForNewCluster(
                        Set.of(gpu),
                        Set.of(ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY.toString())));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("DynamicGPUDiscovery so gpus must not be provided"));
    }

    @Test
    void validateGpusForNewCluster_noDynamicGpuWithEmptyGpus_throwsException() {
        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> clusterCreationService.validateGpusForNewCluster(Set.of(), Set.of()));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("DynamicGPUDiscovery so gpus must be provided"));
    }

    // ==================== Tests for validateClusterNamingForLength ====================

    @Test
    void validateClusterNamingForLength_validLengths_success() {
        // Prepare
        ClusterCreationRequest request = dummyCreateNewClusterRequest(
                "short-name", "short-group", DUMMY_BYOC_NCA_ID, Set.of(), null);
        request.setClusterDescription("short desc");

        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> clusterCreationService.validateClusterNamingForLength(request));
    }

    @Test
    void validateClusterNamingForLength_longClusterName_throwsException() {
        // Prepare
        ClusterCreationRequest request = dummyCreateNewClusterRequest(
                "this-is-a-very-long-cluster-name-that-exceeds-32-chars",
                "group", DUMMY_BYOC_NCA_ID, Set.of(), null);

        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> clusterCreationService.validateClusterNamingForLength(request));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("exceeds the limit of 32 chars"));
    }

    @Test
    void validateClusterNamingForLength_nullDescription_success() {
        // Prepare
        ClusterCreationRequest request = dummyCreateNewClusterRequest(
                "short-name", "short-group", DUMMY_BYOC_NCA_ID, Set.of(), null);
        request.setClusterDescription(null);

        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> clusterCreationService.validateClusterNamingForLength(request));
    }

    // ==================== Tests for validateClusterGroupForNvca2Flow ====================

    @Test
    void validateClusterGroupForNvca2Flow_emptyGpus_success() {
        // Prepare
        ClusterGroupsByAccountEntity clusterGroup =
                getDummyClusterGroupsByAccountEntity(DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID);
        clusterGroup.setGpus(null);

        // Act & Assert - Should not throw
        Assertions.assertDoesNotThrow(() -> clusterCreationService.validateClusterGroupForNvca2Flow(clusterGroup));
    }

    @Test
    void validateClusterGroupForNvca2Flow_withGpus_throwsException() {
        // Prepare
        ClusterGroupsByAccountEntity clusterGroup =
                getDummyClusterGroupsByAccountEntity(DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID);
        clusterGroup.setGpus(Set.of(GpuUdt.builder()
                .name("gpu1")
                .instanceTypes(Set.of(getDummyInstanceType()))
                .build()));

        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> clusterCreationService.validateClusterGroupForNvca2Flow(clusterGroup));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("can not be used since it was already registered with old flow"));
    }

    // ==================== Tests for getMetadataForCluster ====================

    @Test
    void getMetadataForCluster_returnsMetadataMap() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterStatus(ClusterStatusEnum.READY);
        clusterEntity.setAuthorizedNcaIds(Set.of("ncaId1"));

        // Act
        Map<String, Object> metadata = ClusterCreationService.getMetadataForCluster(
                clusterEntity, "registered");

        // Assert
        assertNotNull(metadata);
        // getDummyClusterEntity() sets clusterGroupId to "group_id"
        assertEquals("group_id", metadata.get("clusterGroupId"));
        assertEquals("registered", metadata.get("clusterRegistrationStatus"));
        assertEquals("READY", metadata.get("clusterStatus"));
    }

    @Test
    void getMetadataForCluster_withNullOptionalFields_returnsMetadataWithoutNulls() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterStatus(ClusterStatusEnum.NOT_READY);
        clusterEntity.setNvcaLastConnected(null);
        clusterEntity.setAttributes(null);
        clusterEntity.setCapabilities(null);
        clusterEntity.setAuthorizedNcaIds(null);
        clusterEntity.setGpusV5(null);
        clusterEntity.setCreationQueues(null);
        clusterEntity.setClusterCreationQueues(null);
        clusterEntity.setClusterCreationQueuesForTasks(null);

        // Act
        Map<String, Object> metadata = ClusterCreationService.getMetadataForCluster(
                clusterEntity, "created");

        // Assert
        assertNotNull(metadata);
        // getDummyClusterEntity() sets clusterGroupId to "group_id"
        assertEquals("group_id", metadata.get("clusterGroupId"));
        Assertions.assertNull(metadata.get("NVCA_LAST_CONNECTED"));
    }

    // ==================== Tests for NCA ID mismatch ====================

    @Test
    void clusterCreation_ncaIdMismatchInRequestBody_throwsException() {
        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME,
                "different-nca-id", // NCA ID in body is different
                Set.of("ncaId1"), null);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());

        // Act
        IcmsBadRequestException exception = assertThrows(
                IcmsBadRequestException.class,
                () -> clusterCreationService.clusterCreation(
                        clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>()));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("nca-id in request does not match"));
    }

    // ==================== Tests for cluster in existing group with different provider ====================

    @Test
    void clusterCreation_inExistingGroupWithDifferentProvider_throwsException() {
        // Prepare
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1"), null);
        clusterCreationRequest.setCloudProvider(ClusterProviderEnum.AWS);

        ClusterEntity existingCluster = getDummyClusterEntity();
        existingCluster.setClusterProvider(ClusterProviderEnum.GDN);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(
                Optional.of(getDummyClusterGroupsByAccountEntity(DUMMY_BYOC_CLUSTER_GROUP_NAME,
                        DUMMY_BYOC_NCA_ID)));
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                true)).thenReturn(Optional.empty());
        when(clusterRepository.getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID))
                .thenReturn(Set.of(existingCluster));

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class,
                () -> clusterCreationService.clusterCreation(
                        clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>()));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("Specified cloudProvider AWS is different than cloudProvider of clusterGroup"));
    }

    // ==================== Tests for cluster with shared GPUs and different authorized NCA IDs ====================

    @Test
    void clusterCreation_inExistingGroupWithSharedGpusDifferentAuthNcaIds_throwsException() {
        // Prepare
        GpuRequestSchema gpuRequest = getDummyGpuRequestSchema();
        ClusterCreationRequest clusterCreationRequest = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("differentNcaId"), Set.of(gpuRequest));
        clusterCreationRequest.setCapabilities(Set.of());

        ClusterEntity existingCluster = getDummyClusterEntity();
        existingCluster.setClusterProvider(ClusterProviderEnum.GDN);
        existingCluster.setGpusV5(NvcaRequestSchemaToUdtConverter.toGpuV5Udts(Set.of(gpuRequest)));
        existingCluster.setAuthorizedNcaIds(Set.of("existingNcaId"));

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(
                Optional.of(getDummyClusterGroupsByAccountEntity(DUMMY_BYOC_CLUSTER_GROUP_NAME,
                        DUMMY_BYOC_NCA_ID)));
        when(clusterRepository.getClusterInfoByClusterId(clusterCreationRequest.getOAuthClientId(),
                true)).thenReturn(Optional.empty());
        when(clusterRepository.getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID))
                .thenReturn(Set.of(existingCluster));

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class,
                () -> clusterCreationService.clusterCreation(
                        clusterCreationRequest, DUMMY_BYOC_NCA_ID, new HashMap<>()));

        // Assert
        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("Authorized nca ids are not matching"));
    }

    // ==================== Tests for JWKS / OIDC issuer persistence ====================

    @Test
    void clusterCreation_withJwksAndOidcIssuer_appliesFieldsToClusterRow() {
        // Regression guard: registration request carries jwks + oidcIssuer.
        // The single-table design stores those fields on the canonical
        // cluster_by_cluster_id row, inside the existing oidcClusterIdentityEnabled
        // flag gate.
        when(nvcaConfigurationProperties.isOidcClusterIdentityEnabled()).thenReturn(true);

        String jwks = "{\"keys\":[]}";
        String issuer = "https://k8s.example.com/oidc";

        ClusterCreationRequest request = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        request.setJwks(jwks);
        request.setOidcIssuer(issuer);

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(request.getOAuthClientId(), true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        clusterCreationService.clusterCreation(request, DUMMY_BYOC_NCA_ID, new HashMap<>());

        verify(clusterOidcIdentityService).applyOidcIdentity(
                org.mockito.ArgumentMatchers.any(ClusterEntity.class),
                org.mockito.ArgumentMatchers.eq(jwks),
                org.mockito.ArgumentMatchers.eq(issuer),
                org.mockito.ArgumentMatchers.anyString());
    }

    @Test
    void clusterCreation_withoutJwksOrOidcIssuer_skipsOidcWrite() {
        // Null-case: registration without JWKS/issuer must not synthesize
        // cluster OIDC identity — guards against accidental writes with null jwks.
        ClusterCreationRequest request = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1"), null);
        // Leave jwks and oidcIssuer unset (null)

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(request.getOAuthClientId(), true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        clusterCreationService.clusterCreation(request, DUMMY_BYOC_NCA_ID, new HashMap<>());

        verify(clusterOidcIdentityService, never()).applyOidcIdentity(
                org.mockito.ArgumentMatchers.any(ClusterEntity.class),
                org.mockito.ArgumentMatchers.anyString(),
                org.mockito.ArgumentMatchers.anyString(),
                org.mockito.ArgumentMatchers.anyString());
    }

    @Test
    void clusterCreation_withFlagEnabledAndJwks_passesComputedFingerprintToOidcRepo() {
        // Regression guard for the first-heartbeat compare-and-skip: fingerprint
        // must be computed at register time via the shared util so the rotation
        // path compares byte-for-byte.
        when(nvcaConfigurationProperties.isOidcClusterIdentityEnabled()).thenReturn(true);

        String jwks = "{\"keys\":[]}";

        ClusterCreationRequest request = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        request.setJwks(jwks);
        request.setOidcIssuer("https://k8s.example.com/oidc");

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(request.getOAuthClientId(), true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        clusterCreationService.clusterCreation(request, DUMMY_BYOC_NCA_ID, new HashMap<>());

        String expectedFingerprint;
        try {
            expectedFingerprint = AuthUtils.computeJwksFingerprint(jwks);
        } catch (java.text.ParseException e) {
            throw new AssertionError("test JWKS should parse", e);
        }
        verify(clusterOidcIdentityService).applyOidcIdentity(
                org.mockito.ArgumentMatchers.any(ClusterEntity.class),
                org.mockito.ArgumentMatchers.eq(jwks),
                org.mockito.ArgumentMatchers.eq("https://k8s.example.com/oidc"),
                org.mockito.ArgumentMatchers.eq(expectedFingerprint));
    }

    @Test
    void clusterCreation_withFlagDisabled_skipsOidcWriteEvenIfRequestCarriesJwks() {
        // Managed-NVCF parity: when the oidcClusterIdentityEnabled feature flag is
        // off, the service must skip cluster OIDC persistence even if the
        // caller supplied jwks/oidcIssuer.
        // Flag is off because nvcaConfigurationProperties.isOidcClusterIdentityEnabled()
        // returns false (default Mockito return).

        ClusterCreationRequest request = dummyCreateNewClusterRequest(
                DUMMY_BYOC_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_BYOC_NCA_ID,
                Set.of("ncaId1", "ncaId2"), null);
        request.setJwks("{\"keys\":[]}");
        request.setOidcIssuer("https://k8s.example.com/oidc");

        when(clusterRepository.getClusterByAccountAndName(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME)).thenReturn(Optional.empty());
        when(clusterRepository.getClusterInfoByClusterId(request.getOAuthClientId(), true))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).saveClusterInfo(any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        clusterCreationService.clusterCreation(request, DUMMY_BYOC_NCA_ID, new HashMap<>());

        verify(clusterOidcIdentityService, never()).applyOidcIdentity(
                org.mockito.ArgumentMatchers.any(ClusterEntity.class),
                org.mockito.ArgumentMatchers.anyString(),
                org.mockito.ArgumentMatchers.anyString(),
                org.mockito.ArgumentMatchers.anyString());
    }

    // ==================== Private helper methods ====================

    private ClusterCreationRequest dummyCreateNewClusterRequest(
            String clusterName, String clusterGroupName,
            String ncaId, Set<String> authorizedNcaIds,
            Set<GpuRequestSchema> gpuRequestSchemas) {
        return ClusterCreationRequest.builder()
                .clusterName(clusterName)
                .clusterDescription("dummy_description")
                .clusterGroupName(clusterGroupName)
                .ncaId(ncaId)
                .authorizedNCAIds(authorizedNcaIds)
                .gpus(gpuRequestSchemas)
                .cloudProvider(ClusterProviderEnum.GDN)
                .capabilities(Set.of(ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY.toString()))
                .attributes(Set.of())
                .oAuthClientId(DUMMY_AUTH_CLIENT_ID)
                .nvcaVersion("dummy_version")
                .region("US-EAST-1")
                .build();
    }
}
