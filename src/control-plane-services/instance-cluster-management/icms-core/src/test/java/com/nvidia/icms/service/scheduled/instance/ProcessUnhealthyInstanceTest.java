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
package com.nvidia.icms.service.scheduled.instance;

import static com.nvidia.icms.inbound.rest.model.SpotInstanceStatus.INSTANCE_TERMINATED_CLOUD_OFFLINE;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_FULFILLMENT;
import static com.nvidia.icms.service.byoc.ByocUnhealthyInstanceService.getMetricForCloudOffline;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.getDummyClientRequestDataModel;
import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;
import static com.nvidia.icms.util.TestUtil.getTerminatedInstance;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.configuration.aws.AwsConfigurationProperties;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.FunctionDeploymentStagesService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.ByocTerminateService;
import com.nvidia.icms.service.byoc.ByocUnhealthyInstanceService;
import com.nvidia.icms.service.extensions.api.InstanceTerminationService;
import com.nvidia.icms.service.extensions.impl.NoOpUnhealthyInstanceService;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.scheduled.instance.UnhealthyInstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.CopyUtil;
import com.nvidia.icms.util.TestUtil;
import java.time.Duration;
import java.time.Instant;
import java.util.*;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InOrder;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ProcessUnhealthyInstanceTest {

    private static final String ZONE1 = "ZONE1";
    private static final String DUMMY_SNS_TOPIC_NAME = "sns-%s";

    @Mock
    private InstanceTerminationService instanceTerminationService;
    @Mock
    private ByocTerminateService byocTerminateService;
    @Mock
    private FunctionDeploymentStagesService functionDeploymentStagesService;
    @Mock
    private AppAuditService auditService;
    @Mock
    private TelemetryEventClient telemetryEventClient;
    @Mock
    private InstanceV2Repository instanceV2Repository;
    @Mock
    private InstanceServiceHelper instanceServiceHelper;
    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;
    @Mock
    private ByocConfigurationProperties byocConfigurationProperties;
    @Mock
    private AwsConfigurationProperties awsConfigurationProperties;
    @Mock
    private ClusterRepository clusterRepository;
    @Mock
    private NoOpUnhealthyInstanceService unhealthyInstanceService;

    private ByocUnhealthyInstanceService byocUnhealthyInstanceService;
    private ProcessUnhealthyInstance processUnhealthyInstance;

    @BeforeEach
    void setUp() {
        UnhealthyInstanceServiceHelper unhealthyInstanceServiceHelper =
                new UnhealthyInstanceServiceHelper(instanceServiceHelper, auditService);
        byocUnhealthyInstanceService = new ByocUnhealthyInstanceService(
                byocTerminateService,
                clusterRepository,
                instanceV2Repository,
                functionDeploymentStagesService,
                instanceServiceHelper,
                telemetryEventClient,
                byocConfigurationProperties,
                unhealthyInstanceServiceHelper);
        processUnhealthyInstance = new ProcessUnhealthyInstance(
                unhealthyInstanceService,
                byocUnhealthyInstanceService,
                telemetryEventClient,
                icmsConfigurationProperties,
                ComputePlatformTestFixtures.nonByocComputePlatformService());
    }


    @Test
    void terminateUnhealthyInstances_whenByocCloudUnhealthy_shouldTerminateAndAudit() {
        // Prepare
        String ZONE1 = "ZONE1"; // unhealthy
        String requestId = DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
        Map<String, Set<ClusterEntity>> requestIdToClusterEntityMap = new HashMap<>();
        Map<String, Set<InstanceV2Entity>> requestIdToInstanceIdsMap = new HashMap<>();
        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                                               requestId, CloudProvider.AZURE.toString());

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(ZONE1);

        doReturn(true).when(icmsConfigurationProperties)
                .isCloudFailureDetectionEnabled();
        doReturn(Set.of("*")).when(byocConfigurationProperties)
                .getTerminateCloudFailedInstancesFromClusters();

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(requestId);
        instanceEntity.setZone(ZONE1);
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity.setInstanceId("i1");
        instanceEntity.setRequestRawData(GsonCompatMapper.toJson(clientRequestDataModel));

        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                clientRequestDataModel.getLaunchSpecification());

        List<InstanceV2Entity> instanceEntities = List.of(instanceEntity);

        requestIdToClusterEntityMap.put(requestId, Set.of(clusterEntity));
        requestIdToInstanceIdsMap.put(requestId, Set.of(instanceEntity));

        doReturn(Optional.of(clusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(ZONE1, true);
        doNothing().when(byocTerminateService)
                .sendSqsMessageForInstanceTermination(requestIdToClusterEntityMap,
                                                      requestIdToInstanceIdsMap);

        doNothing().when(instanceV2Repository).update(Mockito.argThat(
                entity -> terminatedInstanceArgMatcher(entity, "i1")));
        InstanceV2Entity terminatedInstanceEntity1 = getTerminatedInstance(
                instanceEntity, "Terminated instance from unhealthy cloud");
        doReturn(terminatedInstanceEntity1)
                .when(byocTerminateService).updateInstanceEntityState(
                        instanceEntity, "Terminated instance from unhealthy cloud");

        doNothing().when(auditService).sendAuditEventForInstance(any(), any(), any());

        InstanceV2Entity deepCopiedInstanceEntity = CopyUtil.deepCopy(instanceEntity);
        deepCopiedInstanceEntity.setInstanceStateName(SpotInstanceInternalState.TERMINATED);
        doNothing().when(telemetryEventClient).triggerEvent(
                List.of(getMetricForCloudOffline(clusterEntity)));

        doNothing().when(telemetryEventClient).triggerEvent(
                List.of(getMetricForCloudOfflineInstanceTermination(deepCopiedInstanceEntity,
                                                                    CloudProvider.getCloudProviderFromClusterProvider(
                                                                            clusterEntity.getClusterProvider()),
                                                                    ResourceProvider.BYOC,
                                                                    clusterEntity.getTerminationQueueUrl(),
                                                                    clusterEntity.getClusterName())));

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities,
                                                             new HashSet<>());

        // Assert

        verifyNoInteractions(instanceTerminationService);
        verify(telemetryEventClient).triggerEvent(
                List.of(getMetricForCloudOffline(clusterEntity)));
        verify(telemetryEventClient).triggerEvent(
                List.of(getMetricForCloudOfflineInstanceTermination(deepCopiedInstanceEntity,
                                                                    CloudProvider.getCloudProviderFromClusterProvider(
                                                                            clusterEntity.getClusterProvider()),
                                                                    ResourceProvider.BYOC,
                                                                    clusterEntity.getTerminationQueueUrl(),
                                                                    clusterEntity.getClusterName())));
        verify(auditService).sendAuditEventForInstance(any(), any(), any());
        verify(clusterRepository).getClusterInfoByClusterId(ZONE1, true);
        verify(byocTerminateService).sendSqsMessageForInstanceTermination(
                requestIdToClusterEntityMap, requestIdToInstanceIdsMap);
        verify(instanceV2Repository).update(Mockito.argThat(
                entity -> terminatedInstanceArgMatcher(entity, "i1")));        verify(byocConfigurationProperties).getTerminateCloudFailedInstancesFromClusters();
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void terminateUnhealthyInstancesMonitoring_whenClusterInfoMissing_shouldTerminateAndAudit() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(ZONE1);

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        instanceEntity.setZone(ZONE1);
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity.setInstanceId("i1");

        List<InstanceV2Entity> instanceEntities =
                List.of(instanceEntity);
        doReturn(Optional.empty()).when(clusterRepository)
                .getClusterInfoByClusterId(ZONE1, true);

        doNothing()
                .when(byocTerminateService)
                .handleInstancesWithMissingClusterInfo(instanceEntity);

        doNothing().when(auditService).sendAuditEventForInstance(any(), any(), any());

        doReturn(true).when(icmsConfigurationProperties)
                .isCloudFailureDetectionEnabled();

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities, new HashSet<>());

        // Verify
        verifyNoInteractions(instanceTerminationService);
        verify(telemetryEventClient).triggerEvent(any());
        verify(auditService).sendAuditEventForInstance(any(), any(), any());
        verify(byocTerminateService).handleInstancesWithMissingClusterInfo(any());
        verify(byocTerminateService, never()).updateInstanceEntityState(any(), any());
        verify(clusterRepository).getClusterInfoByClusterId(ZONE1, true);    }

    @Test
    void terminateUnhealthyInstances_whenByocUnhealthyAndTerminationDisabled_shouldOnlyEmitTelemetry() {
        // Prepare
        String ZONE1 = "ZONE1"; // unhealthy
        String requestId = DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(ZONE1);

        doReturn(true).when(icmsConfigurationProperties)
                .isCloudFailureDetectionEnabled();
        doReturn(Set.of()).when(byocConfigurationProperties)
                .getTerminateCloudFailedInstancesFromClusters();

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(requestId);
        instanceEntity.setZone(ZONE1);
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity.setInstanceId("i1");

        List<InstanceV2Entity> instanceEntities = List.of(instanceEntity);

        doReturn(Optional.of(clusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(ZONE1, true);

        InstanceV2Entity deepCopiedInstanceEntity = CopyUtil.deepCopy(instanceEntity);
        deepCopiedInstanceEntity.setInstanceStateName(SpotInstanceInternalState.TERMINATED);
        doNothing().when(telemetryEventClient).triggerEvent(
                List.of(getMetricForCloudOffline(clusterEntity)));

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities,
                                                             new HashSet<>());

        // Verify
        verifyNoInteractions(auditService);
        verifyNoInteractions(instanceV2Repository);
        verifyNoInteractions(instanceTerminationService);

        verify(telemetryEventClient).triggerEvent(
                List.of(getMetricForCloudOffline(clusterEntity)));

        verify(clusterRepository).getClusterInfoByClusterId(ZONE1, true);
        verifyNoMoreInteractions(byocTerminateService);
        verify(byocConfigurationProperties).getTerminateCloudFailedInstancesFromClusters();
    }

    @Test
    void terminateUnhealthyInstances_whenByocTerminationSqsFails_shouldIgnoreException() {
        // Prepare
        String ZONE1 = "ZONE1"; // unhealthy
        String requestId = DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
        Map<String, Set<ClusterEntity>> requestIdToClusterEntityMap = new HashMap<>();
        Map<String, Set<InstanceV2Entity>> requestIdToInstanceIdsMap = new HashMap<>();

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(ZONE1);

        doReturn(true).when(icmsConfigurationProperties)
                .isCloudFailureDetectionEnabled();
        doReturn(Set.of(ZONE1)).when(byocConfigurationProperties)
                .getTerminateCloudFailedInstancesFromClusters();

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(requestId);
        instanceEntity.setZone(ZONE1);
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity.setInstanceId("i1");

        List<InstanceV2Entity> instanceEntities = List.of(instanceEntity);

        requestIdToClusterEntityMap.put(requestId, Set.of(clusterEntity));
        requestIdToInstanceIdsMap.put(requestId, Set.of(instanceEntity));

        doReturn(Optional.of(clusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(ZONE1, true);
        doThrow(new RuntimeException("Some exception")).when(byocTerminateService)
                .sendSqsMessageForInstanceTermination(requestIdToClusterEntityMap,
                                                      requestIdToInstanceIdsMap);

        InstanceV2Entity deepCopiedInstanceEntity = CopyUtil.deepCopy(instanceEntity);
        deepCopiedInstanceEntity.setInstanceStateName(SpotInstanceInternalState.TERMINATED);
        doNothing().when(telemetryEventClient).triggerEvent(
                List.of(getMetricForCloudOffline(clusterEntity)));

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities,
                                                             new HashSet<>());

        // Verify
        verifyNoInteractions(instanceTerminationService);
        verifyNoInteractions(instanceV2Repository);
        verifyNoInteractions(auditService);

        verify(telemetryEventClient).triggerEvent(
                List.of(getMetricForCloudOffline(clusterEntity)));
        verifyNoMoreInteractions(telemetryEventClient);

        verify(clusterRepository).getClusterInfoByClusterId(ZONE1, true);
        verify(byocTerminateService).sendSqsMessageForInstanceTermination(
                requestIdToClusterEntityMap, requestIdToInstanceIdsMap);
        verify(byocConfigurationProperties).getTerminateCloudFailedInstancesFromClusters();
    }

    private boolean terminatedInstanceArgMatcher(
            InstanceV2Entity instanceEntity,
            String expectedInstanceId) {
        return instanceEntity.getInstanceStateName()
                .equals(SpotInstanceInternalState.TERMINATED) &&
                instanceEntity.getRequestState().equals(SpotInstanceRequestState.CLOSED) &&
                instanceEntity.getInstanceId().equals(expectedInstanceId);
    }

    @Test
    void terminateUnhealthyInstances_whenCloudFailureDetectionDisabled_shouldSkip() {
        // Mock

        doReturn(false).when(icmsConfigurationProperties)
                .isCloudFailureDetectionEnabled();

        // Input
        InstanceRequestV2Entity instanceRequestEntity =
                TestUtil.getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                   PENDING_FULFILLMENT,
                                                   DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                   Instant.now(), ResourceProvider.BYOC);
        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(instanceRequestEntity.getRequestId());
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);

        List<InstanceV2Entity> instanceEntities = List.of(instanceEntity);

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities,
                                                             new HashSet<>());

        // Verify
        verifyNoInteractions(instanceTerminationService);
        verifyNoInteractions(telemetryEventClient);
        verifyNoInteractions(auditService);
    }

    @Test
    void terminateUnhealthyInstances_whenProviderFromCacheAndFeatureDisabled_shouldSkip() {

        // Input
        String requestId = DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
        InstanceV2Entity instanceEntity1 = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity1.setRequestId(requestId);
        instanceEntity1.setInstanceId("i1");
        instanceEntity1.setResourceProvider(null);

        InstanceV2Entity instanceEntity2 = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity2.setRequestId(requestId);
        instanceEntity2.setInstanceId("i2");
        instanceEntity2.setResourceProvider(null);

        InstanceV2Entity instanceEntity3 = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity3.setRequestId(requestId);
        instanceEntity3.setInstanceId("i3");
        instanceEntity3.setResourceProvider(null);

        List<InstanceV2Entity> instanceEntities =
                List.of(instanceEntity1, instanceEntity2, instanceEntity3);

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities,
                                                             new HashSet<>());

        // Verify
        verifyNoInteractions(instanceTerminationService);
        verifyNoInteractions(telemetryEventClient);
        verifyNoInteractions(auditService);
    }

    @Test
    void terminateUnhealthyInstances_whenClusterInSkipList_shouldSkipTermination() {
        // Prepare
        String ZONE1 = "cluster-id-1"; // unhealthy
        String instanceId1 = "i1";
        String instanceId2 = "i2";

        String requestId = DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(ZONE1);

        doReturn(true).when(icmsConfigurationProperties)
                .isCloudFailureDetectionEnabled();

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(requestId);
        instanceEntity.setZone(ZONE1);
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity.setInstanceId(instanceId1);

        List<InstanceV2Entity> instanceEntities = List.of(instanceEntity);

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities,
                                                             Set.of(ZONE1));

        // Verify
        verifyNoInteractions(instanceTerminationService);
        verifyNoInteractions(byocTerminateService);
        verifyNoInteractions(telemetryEventClient);
        verifyNoInteractions(auditService);    }

    @Test
    void terminateUnhealthyInstances_whenHeartbeatNull_shouldTerminate() {
        // Prepare
        String ZONE1 = "cluster-id-1"; // unhealthy
        String requestId = DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
        Map<String, Set<ClusterEntity>> requestIdToClusterEntitiesMap = new HashMap<>();
        Map<String, Set<InstanceV2Entity>> requestIdToInstanceEntitiesMap = new HashMap<>();
        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                                               requestId, CloudProvider.AZURE.toString());

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(ZONE1);
        clusterEntity.setHealthyHeartbeatReportTime(null); // null heartbeat time

        doReturn(true).when(icmsConfigurationProperties)
                .isCloudFailureDetectionEnabled();
        doReturn(true).when(byocConfigurationProperties)
                .isAutoTerminationOfInstancesFromUnhealthyCloudEnabled();
        doReturn(24).when(byocConfigurationProperties)
                .getTimeForAutoTerminatingInstancesFromUnhealthyCloudInHours();

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(requestId);
        instanceEntity.setZone(ZONE1);
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity.setInstanceId("i1");
        instanceEntity.setRequestRawData(GsonCompatMapper.toJson(clientRequestDataModel));

        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                clientRequestDataModel.getLaunchSpecification());

        List<InstanceV2Entity> instanceEntities = List.of(instanceEntity);

        requestIdToClusterEntitiesMap.put(requestId, Set.of(clusterEntity));
        requestIdToInstanceEntitiesMap.put(requestId, Set.of(instanceEntity));

        doReturn(Optional.of(clusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(ZONE1, true);
        doNothing().when(byocTerminateService)
                .sendSqsMessageForInstanceTermination(requestIdToClusterEntitiesMap,
                                                      requestIdToInstanceEntitiesMap);

        doNothing().when(instanceV2Repository).update(Mockito.argThat(
                entity -> terminatedInstanceArgMatcher(entity, "i1")));
        InstanceV2Entity terminatedInstanceEntity1 = getTerminatedInstance(
                instanceEntity, "Terminated instance from unhealthy cloud");
        doReturn(terminatedInstanceEntity1)
                .when(byocTerminateService).updateInstanceEntityState(
                        instanceEntity, "Terminated instance from unhealthy cloud");

        doNothing().when(auditService).sendAuditEventForInstance(any(), any(), any());

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities, new HashSet<>());

        // Verify

        verify(byocTerminateService).sendSqsMessageForInstanceTermination(
                requestIdToClusterEntitiesMap, requestIdToInstanceEntitiesMap);
        verify(instanceV2Repository).update(Mockito.argThat(
                entity -> terminatedInstanceArgMatcher(entity, "i1")));
        verify(auditService).sendAuditEventForInstance(any(), any(), any());
        verify(byocConfigurationProperties).isAutoTerminationOfInstancesFromUnhealthyCloudEnabled();
    }

    @Test
    void terminateUnhealthyInstances_whenHeartbeatRecent_shouldNotTerminate() {
        // Prepare
        String ZONE1 = "cluster-id-1"; // unhealthy
        String requestId = DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(ZONE1);
        // Set heartbeat time to 12 hours ago (recent)
        clusterEntity.setHealthyHeartbeatReportTime(Instant.now().minus(Duration.ofHours(12)));

        doReturn(true).when(icmsConfigurationProperties)
                .isCloudFailureDetectionEnabled();
        doReturn(true).when(byocConfigurationProperties)
                .isAutoTerminationOfInstancesFromUnhealthyCloudEnabled();
        doReturn(24).when(byocConfigurationProperties)
                .getTimeForAutoTerminatingInstancesFromUnhealthyCloudInHours();

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(requestId);
        instanceEntity.setZone(ZONE1);
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity.setInstanceId("i1");

        List<InstanceV2Entity> instanceEntities = List.of(instanceEntity);

        doReturn(Optional.of(clusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(ZONE1, true);

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities, new HashSet<>());

        // Verify
        // Should not terminate because heartbeat is recent
        verifyNoInteractions(instanceV2Repository);
        verifyNoInteractions(auditService);
        verify(byocTerminateService, never()).sendSqsMessageForInstanceTermination(any(), any());
        verify(byocConfigurationProperties).isAutoTerminationOfInstancesFromUnhealthyCloudEnabled();
        verify(byocConfigurationProperties).getTimeForAutoTerminatingInstancesFromUnhealthyCloudInHours();
    }

    @Test
    void terminateUnhealthyInstances_whenHeartbeatOld_shouldTerminate() {
        // Prepare
        String ZONE1 = "cluster-id-1"; // unhealthy
        String requestId = DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
        Map<String, Set<ClusterEntity>> requestIdToClusterEntitiesMap = new HashMap<>();
        Map<String, Set<InstanceV2Entity>> requestIdToInstanceEntitiesMap = new HashMap<>();
        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                                               requestId, CloudProvider.AZURE.toString());

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(ZONE1);
        // Set heartbeat time to 25 hours ago (old)
        clusterEntity.setHealthyHeartbeatReportTime(Instant.now().minus(Duration.ofHours(25)));

        doReturn(true).when(icmsConfigurationProperties)
                .isCloudFailureDetectionEnabled();
        doReturn(true).when(byocConfigurationProperties)
                .isAutoTerminationOfInstancesFromUnhealthyCloudEnabled();
        doReturn(24).when(byocConfigurationProperties)
                .getTimeForAutoTerminatingInstancesFromUnhealthyCloudInHours();

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(requestId);
        instanceEntity.setZone(ZONE1);
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity.setInstanceId("i1");
        instanceEntity.setRequestRawData(GsonCompatMapper.toJson(clientRequestDataModel));

        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                clientRequestDataModel.getLaunchSpecification());

        List<InstanceV2Entity> instanceEntities = List.of(instanceEntity);

        requestIdToClusterEntitiesMap.put(requestId, Set.of(clusterEntity));
        requestIdToInstanceEntitiesMap.put(requestId, Set.of(instanceEntity));

        doReturn(Optional.of(clusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(ZONE1, true);
        doNothing().when(byocTerminateService)
                .sendSqsMessageForInstanceTermination(requestIdToClusterEntitiesMap,
                                                      requestIdToInstanceEntitiesMap);

        doNothing().when(instanceV2Repository).update(Mockito.argThat(
                entity -> terminatedInstanceArgMatcher(entity, "i1")));
        InstanceV2Entity terminatedInstanceEntity1 = getTerminatedInstance(
                instanceEntity, "Terminated instance from unhealthy cloud");
        doReturn(terminatedInstanceEntity1)
                .when(byocTerminateService).updateInstanceEntityState(
                        instanceEntity, "Terminated instance from unhealthy cloud");

        doNothing().when(auditService).sendAuditEventForInstance(any(), any(), any());

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities, new HashSet<>());

        // Verify
        verify(byocTerminateService).sendSqsMessageForInstanceTermination(
                requestIdToClusterEntitiesMap, requestIdToInstanceEntitiesMap);
        verify(instanceV2Repository).update(Mockito.argThat(
                entity -> terminatedInstanceArgMatcher(entity, "i1")));
        verify(auditService).sendAuditEventForInstance(any(), any(), any());
        verify(byocConfigurationProperties).isAutoTerminationOfInstancesFromUnhealthyCloudEnabled();
        verify(byocConfigurationProperties).getTimeForAutoTerminatingInstancesFromUnhealthyCloudInHours();
    }

    @Test
    void terminateUnhealthyInstances_whenClusterNotWhitelistedAndHeartbeatOld_shouldTerminate() {
        // Prepare
        String ZONE1 = "cluster-id-1"; // unhealthy
        String requestId = DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
        Map<String, Set<ClusterEntity>> requestIdToClusterEntitiesMap = new HashMap<>();
        Map<String, Set<InstanceV2Entity>> requestIdToInstanceEntitiesMap = new HashMap<>();
        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                                               requestId, CloudProvider.AZURE.toString());

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(ZONE1);
        // Set heartbeat time to 25 hours ago (old)
        clusterEntity.setHealthyHeartbeatReportTime(Instant.now().minus(Duration.ofHours(25)));

        doReturn(true).when(icmsConfigurationProperties)
                .isCloudFailureDetectionEnabled();
        doReturn(true).when(byocConfigurationProperties)
                .isAutoTerminationOfInstancesFromUnhealthyCloudEnabled();
        doReturn(24).when(byocConfigurationProperties)
                .getTimeForAutoTerminatingInstancesFromUnhealthyCloudInHours();
        doReturn(Set.of("cluster-id-2")).when(byocConfigurationProperties) // Whitelist another cluster
                .getTerminateCloudFailedInstancesFromClusters();

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(requestId);
        instanceEntity.setZone(ZONE1);
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity.setInstanceId("i1");
        instanceEntity.setRequestRawData(GsonCompatMapper.toJson(clientRequestDataModel));

        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                clientRequestDataModel.getLaunchSpecification());

        List<InstanceV2Entity> instanceEntities = List.of(instanceEntity);

        requestIdToClusterEntitiesMap.put(requestId, Set.of(clusterEntity));
        requestIdToInstanceEntitiesMap.put(requestId, Set.of(instanceEntity));

        doReturn(Optional.of(clusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(ZONE1, true);
        doNothing().when(byocTerminateService)
                .sendSqsMessageForInstanceTermination(requestIdToClusterEntitiesMap,
                                                      requestIdToInstanceEntitiesMap);

        doNothing().when(instanceV2Repository).update(Mockito.argThat(
                entity -> terminatedInstanceArgMatcher(entity, "i1")));
        InstanceV2Entity terminatedInstanceEntity1 = getTerminatedInstance(
                instanceEntity, "Terminated instance from unhealthy cloud");
        doReturn(terminatedInstanceEntity1)
                .when(byocTerminateService).updateInstanceEntityState(
                        instanceEntity, "Terminated instance from unhealthy cloud");

        doNothing().when(auditService).sendAuditEventForInstance(any(), any(), any());

        // Act
        processUnhealthyInstance.terminateUnhealthyInstances(instanceEntities, new HashSet<>());

        // Verify
        verify(byocTerminateService).sendSqsMessageForInstanceTermination(
                requestIdToClusterEntitiesMap, requestIdToInstanceEntitiesMap);
        verify(instanceV2Repository).update(Mockito.argThat(
                entity -> terminatedInstanceArgMatcher(entity, "i1")));
        verify(auditService).sendAuditEventForInstance(any(), any(), any());
        verify(byocConfigurationProperties).isAutoTerminationOfInstancesFromUnhealthyCloudEnabled();
        verify(byocConfigurationProperties).getTimeForAutoTerminatingInstancesFromUnhealthyCloudInHours();
    }

    private GenericMetric getMetricForCloudOfflineInstanceTermination(
            InstanceV2Entity instanceEntity,
            CloudProvider cloudProvider,
            ResourceProvider resourceProvider,
            String terminationQueue,
            String clusterName) {
        Duration instanceLifeTime = Duration.between(
                instanceEntity.getInstanceUpdateTime(), Instant.now());

        ClientRequestDataModel.LaunchSpecification launchSpecification =
                GsonCompatMapper.fromJson(instanceEntity.getRequestRawData(),
                                          ClientRequestDataModel.class)
                        .getLaunchSpecification();

        return new GenericMetric()
                .withEventName(Events.INSTANCE_FAILED_CLOUD_OFFLINE.toString())
                .withCustomer(instanceEntity.getCustomer())
                .withInstanceId(instanceEntity.getInstanceId())
                .withCloudProvider(cloudProvider)
                .withMetadata(Map.of(TelemetryEventClient.EventMetaData.TERMINATION_QUEUE.getName(), terminationQueue))
                .withResourceProvider(resourceProvider)
                .withClusterName(clusterName)
                .withZoneName(instanceEntity.getZone())
                .withRequestId(instanceEntity.getRequestId())
                .withInstanceState(instanceEntity.getInstanceStateName().getStateName())
                .withInstanceLifeTime(instanceLifeTime.toSeconds())
                .withNcaId(launchSpecification.getNcaId())
                .withFunctionId(launchSpecification.getFunctionId())
                .withFunctionVersionId(launchSpecification.getVersionId())
                .withInstanceType(launchSpecification.getInstanceType())
                .withDeploymentId(launchSpecification.getDeploymentId())
                .withGpuSpecificationId(launchSpecification.getGpuSpecificationId())
                .withReasonForTermination(INSTANCE_TERMINATED_CLOUD_OFFLINE.toString());
    }
}
