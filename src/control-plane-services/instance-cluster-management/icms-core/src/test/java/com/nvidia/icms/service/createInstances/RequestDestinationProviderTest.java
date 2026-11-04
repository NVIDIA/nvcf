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

import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.service.extensions.api.InstanceDestinationProvider;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.uec.UnifiedErrorReporter;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.Map;
import java.util.Set;

@ExtendWith(MockitoExtension.class)
class RequestDestinationProviderTest extends CreateInstancesTestBase {

    @Mock private InstanceDestinationProvider instanceDestinationProvider;
    @Mock private ByocRequestDestinationProvider byocProvider;
    @Mock private ClusterTargetingHelper clusterTargetingHelper;
    @Mock private TelemetryEventClient telemetryEventClient;

    private RequestDestinationProvider provider;
    private SpotInstanceRequestSchema instanceRequest;
    private CloudHealthEntity cloudHealthEntity;

    @BeforeEach
    void setUp() {
        UnifiedErrorReporter unifiedErrorReporter = new UnifiedErrorReporter(telemetryEventClient);
        provider = new RequestDestinationProvider(
                instanceDestinationProvider,
                byocProvider,
                clusterTargetingHelper,
                unifiedErrorReporter,
                ComputePlatformTestFixtures.nonByocComputePlatformService());

        instanceRequest = createInstanceRequest();
        cloudHealthEntity = createCloudHealthEntity();
    }

    // -------------------------------------------------------------------------
    // getAllTargetedDestinations — orchestration
    // -------------------------------------------------------------------------

    @Test
    void getAllTargetedDestinations_shouldDelegateClusterValidationAndGenerationToByocProvider() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        RequestInstanceDestination dest = createDestination(null);

        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID)).thenReturn(Set.of(cluster));
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD)).thenReturn(Set.of());
        when(byocProvider.isClusterAllowed(eq(cluster), any())).thenReturn(true);
        when(byocProvider.generateTargetedDestinationList(any(), any(), eq(instanceRequest))).thenReturn(Set.of(dest));
        when(instanceDestinationProvider.getAuthorizedZones(any(), eq(DUMMY_BYOC_NCA_ID))).thenReturn(Set.of());
        when(instanceDestinationProvider.filterForAuthorizedZones(any(), eq(instanceRequest), any())).thenAnswer(i -> i.getArgument(0));
        when(instanceDestinationProvider.removeForTaskWithLongWait(eq(instanceRequest), any())).thenAnswer(i -> i.getArgument(1));

        Set<RequestInstanceDestination> result =
                provider.getAllTargetedDestinations(instanceRequest, Map.of(DUMMY_CLUSTER_ID, cloudHealthEntity));

        verify(byocProvider).isClusterAllowed(eq(cluster), any());
        verify(byocProvider).generateTargetedDestinationList(any(), any(), eq(instanceRequest));
        verify(instanceDestinationProvider).getAuthorizedZones(any(), eq(DUMMY_BYOC_NCA_ID));
        verify(instanceDestinationProvider).filterForAuthorizedZones(any(), eq(instanceRequest), any());
        verify(instanceDestinationProvider).removeForTaskWithLongWait(eq(instanceRequest), any());
        assertEquals(1, result.size());
    }

    @Test
    void getAllTargetedDestinations_whenClusterNotAllowed_shouldBeExcludedBeforeGeneration() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();

        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID)).thenReturn(Set.of(cluster));
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD)).thenReturn(Set.of());
        when(byocProvider.isClusterAllowed(eq(cluster), any())).thenReturn(false);
        when(byocProvider.generateTargetedDestinationList(eq(Set.of()), any(), eq(instanceRequest))).thenReturn(Set.of());
        when(instanceDestinationProvider.getAuthorizedZones(any(), any())).thenReturn(Set.of());
        when(instanceDestinationProvider.filterForAuthorizedZones(any(), any(), any())).thenAnswer(i -> i.getArgument(0));
        when(instanceDestinationProvider.removeForTaskWithLongWait(any(), any())).thenAnswer(i -> i.getArgument(1));

        Set<RequestInstanceDestination> result =
                provider.getAllTargetedDestinations(instanceRequest, Map.of(DUMMY_CLUSTER_ID, cloudHealthEntity));

        // Filtered-out cluster should not appear in the generation call
        verify(byocProvider).generateTargetedDestinationList(eq(Set.of()), any(), eq(instanceRequest));
        assertTrue(result.isEmpty());
    }

    @Test
    void getAllTargetedDestinations_whenNoTargetingEnabled_shouldExcludeCluster() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        cluster.setAllowClusterTargeting(false);

        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID)).thenReturn(Set.of(cluster));
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD)).thenReturn(Set.of());
        when(byocProvider.generateTargetedDestinationList(eq(Set.of()), any(), eq(instanceRequest))).thenReturn(Set.of());
        when(instanceDestinationProvider.getAuthorizedZones(any(), any())).thenReturn(Set.of());
        when(instanceDestinationProvider.filterForAuthorizedZones(any(), any(), any())).thenAnswer(i -> i.getArgument(0));
        when(instanceDestinationProvider.removeForTaskWithLongWait(any(), any())).thenAnswer(i -> i.getArgument(1));

        provider.getAllTargetedDestinations(instanceRequest, Map.of(DUMMY_CLUSTER_ID, cloudHealthEntity));

        // isClusterAllowed must NOT be called when allowClusterTargeting=false
        verify(byocProvider, never()).isClusterAllowed(any(), any());
    }

    // -------------------------------------------------------------------------
    // getAllNonTargetedDestinations — routing
    // -------------------------------------------------------------------------

    @Test
    void getAllNonTargetedDestinations_whenBackendIsNonByoc_shouldDelegateToNonByocProvider() {
        instanceRequest.setBackend("OCI");
        RequestInstanceDestination dest = createDestination(null);
        when(instanceDestinationProvider.getNonTargetedDestinations(instanceRequest)).thenReturn(Set.of(dest));

        Set<RequestInstanceDestination> result = provider.getAllNonTargetedDestinations(instanceRequest);

        verify(instanceDestinationProvider).getNonTargetedDestinations(instanceRequest);
        verify(byocProvider, never()).getNonTargetedDestinations(any());
        assertEquals(1, result.size());
    }

    @Test
    void getAllNonTargetedDestinations_whenBackendIsNotNonByoc_shouldDelegateToByocProvider() {
        instanceRequest.setBackend(null);
        RequestInstanceDestination dest = createDestination(null);
        when(byocProvider.getNonTargetedDestinations(instanceRequest)).thenReturn(Set.of(dest));

        Set<RequestInstanceDestination> result = provider.getAllNonTargetedDestinations(instanceRequest);

        verify(byocProvider).getNonTargetedDestinations(instanceRequest);
        verify(instanceDestinationProvider, never()).getNonTargetedDestinations(any());
        assertEquals(1, result.size());
    }

    @Test
    void getAllNonTargetedDestinations_whenDestinationsEmpty_shouldThrow() {
        instanceRequest.setBackend(null);
        when(byocProvider.getNonTargetedDestinations(instanceRequest)).thenReturn(Set.of());

        assertThrows(Exception.class, () -> provider.getAllNonTargetedDestinations(instanceRequest));
    }

    // -------------------------------------------------------------------------
    // getClusterIdsFromFilteredRequestInfo
    // -------------------------------------------------------------------------

    @Test
    void getClusterIdsFromFilteredRequestInfo_withSingleDestination_returnsClusterId() {
        Set<RequestInstanceDestination> destinations = Set.of(
                RequestInstanceDestination.builder()
                        .clusterId(DUMMY_CLUSTER_ID)
                        .clusterGroupId("group")
                        .build());

        assertEquals(DUMMY_CLUSTER_ID, provider.getClusterIdsFromFilteredRequestInfo(destinations));
    }

    @Test
    void getClusterIdsFromFilteredRequestInfo_withMultipleDestinations_returnsCommaSeparatedIds() {
        Set<RequestInstanceDestination> destinations = Set.of(
                RequestInstanceDestination.builder().clusterId("cluster-1").clusterGroupId("g1").build(),
                RequestInstanceDestination.builder().clusterId("cluster-2").clusterGroupId("g2").build());

        String result = provider.getClusterIdsFromFilteredRequestInfo(destinations);
        assertTrue(result.contains("cluster-1"));
        assertTrue(result.contains("cluster-2"));
    }

    @Test
    void getClusterIdsFromFilteredRequestInfo_withEmptySet_returnsNull() {
        assertNull(provider.getClusterIdsFromFilteredRequestInfo(Set.of()));
    }

    @Test
    void getClusterIdsFromFilteredRequestInfo_withNullClusterId_returnsNull() {
        Set<RequestInstanceDestination> destinations = Set.of(
                RequestInstanceDestination.builder()
                        .clusterId(null)
                        .clusterGroupId("group")
                        .build());

        assertNull(provider.getClusterIdsFromFilteredRequestInfo(destinations));
    }

    @Test
    void getClusterIdsFromFilteredRequestInfo_withBlankClusterId_returnsNull() {
        Set<RequestInstanceDestination> destinations = Set.of(
                RequestInstanceDestination.builder()
                        .clusterId("")
                        .clusterGroupId("group")
                        .build());

        assertNull(provider.getClusterIdsFromFilteredRequestInfo(destinations));
    }
}
