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

import static com.nvidia.icms.util.TestUtil.DUMMY_ARTIFACT_URL;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CACHE_HANDLE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CACHE_SIZE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENVIRONMENT_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_NAME;
import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.FunctionType;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationCredentialsResponse;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterHeartbeatRequest;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.sns.model.SnsTerminationMessageModel;
import com.nvidia.icms.service.byoc.ByocCreateService;
import com.nvidia.icms.service.byoc.ByocTerminateService;
import com.nvidia.icms.service.byoc.ClusterHealthService;
import com.nvidia.icms.service.byoc.ClusterQueueAccessCredsService;
import com.nvidia.icms.service.byoc.ClusterRegistrationService;
import java.util.HashMap;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
@Slf4j
public class ByocServiceTest {

    @Mock
    ClusterRepository clusterRepository;

    @Mock
    ClusterQueueAccessCredsService clusterQueueAccessCredsService;

    @Mock
    ClusterRegistrationService clusterRegistrationService;

    @Mock
    ClusterHealthService clusterHeartbeatService;

    @Mock
    ByocCreateService byocCreateService;

    @Mock
    ByocTerminateService byocTerminateService;

    @Mock
    FunctionBillingService functionBillingService;

    private ByocService byocService;

    @BeforeEach
    void init() {

        byocService =
                new ByocService(clusterQueueAccessCredsService, clusterRegistrationService,
                                clusterHeartbeatService, clusterRepository,
                                byocTerminateService);
    }

    @Test
    void registerCluster_success() {
        Map<String, Object> auditProps = new HashMap<>();
        when(clusterRegistrationService.registerCluster(Mockito.any(),
                                                        eq(DUMMY_CLUSTER_ID),
                                                        eq(auditProps))).thenReturn(
                new BartRegistrationResponse());

        BartRegistrationResponse bartRegistrationResponse =
                byocService.registerCluster(new BartRegistrationRequest(),
                                            DUMMY_CLUSTER_ID, auditProps);

        Assertions.assertNotNull(bartRegistrationResponse);
        verify(clusterRegistrationService).registerCluster(Mockito.any(),
                                                           eq(DUMMY_CLUSTER_ID), eq(auditProps));
    }

    @Test
    void deleteCluster_success() {
        Map<String, Object> auditProps = new HashMap<>();
        doNothing().when(clusterRegistrationService).deleteCluster(DUMMY_CLUSTER_ID, auditProps);

        byocService.deleteCluster(DUMMY_CLUSTER_ID, auditProps);

        verify(clusterRegistrationService).deleteCluster(DUMMY_CLUSTER_ID, auditProps);
    }

    @Test
    void getClusterQueuesInfo() {

        when(clusterQueueAccessCredsService.getClusterQueuesInfo(DUMMY_CLUSTER_ID)).thenReturn(
                new BartRegistrationCredentialsResponse());

        BartRegistrationCredentialsResponse bartRegistrationCredentialsResponse =
                byocService.getClusterQueuesInfo(DUMMY_CLUSTER_ID);

        Assertions.assertNotNull(bartRegistrationCredentialsResponse);
        verify(clusterQueueAccessCredsService).getClusterQueuesInfo(DUMMY_CLUSTER_ID);

    }

    @Test
    void registerClusterHeartbeat_success() {
        doNothing().when(clusterHeartbeatService)
                .registerClusterHeartbeat(Mockito.any(), eq(DUMMY_CLUSTER_ID));

        byocService.registerClusterHeartbeat(new ClusterHeartbeatRequest(), DUMMY_CLUSTER_ID);

        verify(clusterHeartbeatService).registerClusterHeartbeat(Mockito.any(),
                                                                 eq(DUMMY_CLUSTER_ID));
    }

    @Test
    void getClusterEntityFromByocClusterId_success() {
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(
                Optional.of(getDummyClusterEntity()));

        Optional<ClusterEntity> clusterEntityOptional =
                byocService.getClusterEntityFromByocClusterId(DUMMY_CLUSTER_ID);

        Assertions.assertTrue(clusterEntityOptional.isPresent());
        Assertions.assertEquals("id", clusterEntityOptional.get().getClusterId());
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true);
    }

    @Test
    void getClusterEntityFromByocClusterId_emptyClusterName() {
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(
                Optional.empty());

        Optional<ClusterEntity> clusterEntityOptional =
                byocService.getClusterEntityFromByocClusterId(DUMMY_CLUSTER_ID);

        Assertions.assertTrue(clusterEntityOptional.isEmpty());
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true);
    }


    @Test
    void terminateInstances_success() {
        when(byocTerminateService.terminateInstances(Mockito.anySet(),
                                                          Mockito.anyMap())).thenReturn(
                new TerminateInstancesResponse());

        TerminateInstancesResponse terminateInstancesResponse =
                byocService.terminateInstances(Set.of(), Map.of());

        Assertions.assertNotNull(terminateInstancesResponse);
        verify(byocTerminateService).terminateInstances(Mockito.anySet(),
                                                             Mockito.anyMap());
    }

    @Test
    void getCloudProviderForByocCluster_success() {
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(
                Optional.of(getDummyClusterEntity()));

        CloudProvider cloudProvider = byocService.getCloudProviderForByocCluster(DUMMY_CLUSTER_ID);

        Assertions.assertEquals(CloudProvider.GDN, cloudProvider);
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true);
    }

    @Test
    void getCloudProviderForByocCluster_errorCase() {
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(
                Optional.empty());

        IcmsNotFoundException exception = assertThrows(IcmsNotFoundException.class,
                                                      () -> byocService.getCloudProviderForByocCluster(
                                                              DUMMY_CLUSTER_ID));

        // Assert
        Assertions.assertEquals(
                "Cloud not find any cluster with cluster_id clusterId",
                exception.getBody().getDetail());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true);
    }
}
