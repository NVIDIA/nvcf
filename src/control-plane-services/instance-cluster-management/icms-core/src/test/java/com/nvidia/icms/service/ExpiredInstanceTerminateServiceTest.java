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

import static com.nvidia.icms.inbound.rest.model.SpotInstanceStatus.INSTANCE_TERMINATED_LIFETIME_EXPIRED;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.FULFILLED;
import static com.nvidia.icms.service.scheduled.instance.ExpiredInstanceServiceHelper.INSTANCE_LIFETIME_EXPIRED_ERROR_LOG;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_VERSION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_SNS_TOPIC_NAME;
import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;
import static com.nvidia.icms.util.TestUtil.getTerminatedInstance;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;

import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.configuration.aws.AwsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.byoc.ByocExpiredInstanceTerminateService;
import com.nvidia.icms.service.byoc.ByocTerminateService;
import com.nvidia.icms.service.extensions.impl.NoOpExpiredInstanceProcessor;
import com.nvidia.icms.service.extensions.api.InstanceTerminationService;
import com.nvidia.icms.service.scheduled.instance.ExpiredInstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TestUtil;
import com.nvidia.icms.util.audit.AuditUtils;

import java.time.Duration;
import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ExpiredInstanceTerminateServiceTest {

    @Mock
    private InstanceV2Repository instanceV2Repository;
    @Mock
    private InstanceTerminationService instanceTerminationService;
    @Mock
    private ByocTerminateService byocTerminateService;
    @Mock
    private TelemetryEventClient telemetryEventClient;
    @Mock
    private AppAuditService auditService;
    @Mock
    private AwsConfigurationProperties awsConfigurationProperties;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    private NoOpExpiredInstanceProcessor expiredInstanceProcessor;
    private ByocExpiredInstanceTerminateService byocExpiredInstanceTerminateService;
    private ExpiredInstanceTerminateService expiredInstanceTerminateService;

    @BeforeEach
    void setUp() {
        ExpiredInstanceServiceHelper expiredInstanceServiceHelper =
                new ExpiredInstanceServiceHelper(instanceServiceHelper, auditService);
        expiredInstanceProcessor = new NoOpExpiredInstanceProcessor();
        byocExpiredInstanceTerminateService = new ByocExpiredInstanceTerminateService(
                byocTerminateService,
                instanceV2Repository,
                telemetryEventClient,
                instanceServiceHelper,
                expiredInstanceServiceHelper);
        expiredInstanceTerminateService = new ExpiredInstanceTerminateService(
                expiredInstanceProcessor,
                byocExpiredInstanceTerminateService,
                ComputePlatformTestFixtures.nonByocComputePlatformService());
    }

    @Test
    void terminateExpiredInstances_withExpiredByocInstances_success() {
        // Input
        InstanceRequestV2Entity instanceRequestEntity =
                TestUtil.getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                                   FULFILLED,
                                                   DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                   Instant.now(), ResourceProvider.BYOC);

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(instanceRequestEntity.getRequestId());
        instanceEntity.setInstanceId(DUMMY_INSTANCE_ID + "_1");
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity.setZone("ZONE1");
        instanceEntity.setRequestRawData(instanceRequestEntity.getRequest());

        InstanceV2Entity instanceEntity2 = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity2.setRequestId(instanceRequestEntity.getRequestId());
        instanceEntity2.setInstanceId(DUMMY_INSTANCE_ID + "_2");
        instanceEntity2.setResourceProvider(ResourceProvider.BYOC);
        instanceEntity2.setZone("ZONE1");
        instanceEntity2.setRequestRawData(instanceRequestEntity.getRequest());

        List<InstanceV2Entity> instanceEntities =
                List.of(AuditUtils.deepCopyInstanceEntity(instanceEntity),
                        AuditUtils.deepCopyInstanceEntity(instanceEntity2));

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId("ZONE1");
        doReturn(Optional.of(clusterEntity)).when(byocTerminateService)
                .getClusterEntityFromClusterId("ZONE1");

        Map<String, Set<ClusterEntity>> requestIdToClusterEntityMap = new HashMap<>();
        Map<String, Set<InstanceV2Entity>> requestIdToInstanceIdsMap = new HashMap<>();
        requestIdToClusterEntityMap.put(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                        Set.of(clusterEntity));
        requestIdToInstanceIdsMap.put(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                      Set.of(instanceEntity,
                                             instanceEntity2));
        doNothing().when(byocTerminateService)
                .sendSqsMessageForInstanceTermination(any(), any());
        InstanceV2Entity terminatedInstanceEntity1 = getTerminatedInstance(
                instanceEntity, INSTANCE_LIFETIME_EXPIRED_ERROR_LOG);
        InstanceV2Entity terminatedInstanceEntity2 = getTerminatedInstance(
                instanceEntity2, INSTANCE_LIFETIME_EXPIRED_ERROR_LOG);
        doReturn(terminatedInstanceEntity1)
                .when(byocTerminateService).updateInstanceEntityState(
                        instanceEntity, INSTANCE_LIFETIME_EXPIRED_ERROR_LOG);
        doReturn(terminatedInstanceEntity2)
                .when(byocTerminateService).updateInstanceEntityState(
                        instanceEntity2, INSTANCE_LIFETIME_EXPIRED_ERROR_LOG);
        doNothing().when(instanceV2Repository).update(terminatedInstanceEntity1);
        doNothing().when(instanceV2Repository).update(terminatedInstanceEntity2);
        doNothing().when(auditService).sendAuditEventForInstance(any(), any(), any());
        doReturn(ClientRequestDataModel.LaunchSpecification.builder()
                         .versionId(DUMMY_FUNCTION_VERSION_ID)
                         .functionId(DUMMY_FUNCTION_ID)
                         .ncaId(DUMMY_BYOC_NCA_ID)
                         .instanceType(DUMMY_BYOC_INSTANCE_TYPE)
                         .deploymentId(UUID.randomUUID())
                         .gpuSpecificationId(UUID.randomUUID())
                         .build()).when(instanceServiceHelper)
                .getLaunchSpecificationForTelemetry(Mockito.any());

        // Act
        List<InstanceV2Entity> response =
                expiredInstanceTerminateService.terminateExpiredInstances(instanceEntities);

        // Assert
        assertEquals(2, response.size());
        var expectedInstanceIds = Set.of(DUMMY_INSTANCE_ID + "_1", DUMMY_INSTANCE_ID + "_2");
        for (InstanceV2Entity entity : response) {
            assertTrue(expectedInstanceIds.contains(entity.getInstanceId()));
        }
        verifyNoInteractions(instanceTerminationService);
        verify(byocTerminateService).getClusterEntityFromClusterId("ZONE1");
        verify(byocTerminateService, times(1)).sendSqsMessageForInstanceTermination(
                requestIdToClusterEntityMap, requestIdToInstanceIdsMap);
        verify(instanceV2Repository, times(2)).update(any());
        verify(instanceServiceHelper, times(2)).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void terminateExpiredInstances_whenInstanceAlreadyTerminated_shouldSkip() {
        InstanceV2Entity entity = TestUtil.getInstanceEntityForRunningInstance();
        entity.setInstanceStateName(SpotInstanceInternalState.TERMINATED);

        List<InstanceV2Entity> result =
                expiredInstanceTerminateService.terminateExpiredInstances(List.of(entity));

        assertTrue(result.isEmpty());
        verifyNoInteractions(instanceTerminationService, byocTerminateService);
    }

    @Test
    void terminateExpiredInstances_whenInstanceShuttingDown_shouldSkip() {
        InstanceV2Entity entity = TestUtil.getInstanceEntityForRunningInstance();
        entity.setInstanceStateName(SpotInstanceInternalState.SHUTTING_DOWN);

        List<InstanceV2Entity> result =
                expiredInstanceTerminateService.terminateExpiredInstances(List.of(entity));

        assertTrue(result.isEmpty());
        verifyNoInteractions(instanceTerminationService, byocTerminateService);
    }

    private GenericMetric getMetricExpiredInstanceTermination(
            InstanceV2Entity instanceEntity,
            CloudProvider cloudProvider,
            ResourceProvider resourceProvider,
            String terminationQueue) {
        Duration instanceLifeTime = Duration.between(
                instanceEntity.getInstanceUpdateTime(),
                Instant.now());

        Map<String, Object> metadata = new HashMap<>();
        metadata.put(TelemetryEventClient.EventMetaData.TERMINATION_QUEUE.getName(),
                terminationQueue);

        ClientRequestDataModel.LaunchSpecification launchSpecification =
                GsonCompatMapper.fromJson(instanceEntity.getRequestRawData(),
                                          ClientRequestDataModel.class)
                        .getLaunchSpecification();

        return new GenericMetric()
                .withEventName(Events.TERMINATE_LIFETIME_EXPIRED_INSTANCE.toString())
                .withCustomer(instanceEntity.getCustomer())
                .withInstanceId(instanceEntity.getInstanceId())
                .withCloudProvider(cloudProvider)
                .withResourceProvider(resourceProvider)
                .withMetadata(metadata)
                .withZoneName(instanceEntity.getZone())
                .withRequestId(instanceEntity.getRequestId())
                .withInstanceState(instanceEntity.getInstanceStateName().getStateName())
                .withInstanceLifeTime(instanceLifeTime.toSeconds())
                .withInstanceType(launchSpecification.getInstanceType())
                .withFunctionId(launchSpecification.getFunctionId())
                .withFunctionVersionId(launchSpecification.getVersionId())
                .withNcaId(launchSpecification.getNcaId())
                .withDeploymentId(launchSpecification.getDeploymentId())
                .withGpuSpecificationId(launchSpecification.getGpuSpecificationId())
                .withReasonForTermination(INSTANCE_TERMINATED_LIFETIME_EXPIRED.toString());
    }

}