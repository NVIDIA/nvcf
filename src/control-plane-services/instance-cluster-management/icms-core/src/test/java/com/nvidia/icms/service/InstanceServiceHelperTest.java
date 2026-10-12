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

import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENVIRONMENT_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_ERROR_SOURCE;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_NCA_ID_ACCOUNT_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static com.nvidia.icms.util.TestUtil.customObjectMapper;
import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;
import static com.nvidia.icms.service.telemetry.model.Events.ASYNC_EVENT_TRIGGER_FAILED;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.configuration.aws.AwsConfigurationProperties;
import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.event.NcaIdAccountNameEvent;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.instance.InstancePerZoneRepository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.nats.NatsMessageSenderClient;
import com.nvidia.icms.outbound.sqs.SqsMessageSenderClient;
import com.nvidia.icms.service.metrics.InstanceErrorMetricsService;
import com.nvidia.icms.service.scheduled.gpuusage.GpuUsageEventService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TestUtil;

import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import java.util.UUID;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.context.ApplicationEventPublisher;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;

@ExtendWith(MockitoExtension.class)
class InstanceServiceHelperTest {

    private ObjectMapper objectMapper;

    @Mock
    private ByocConfigurationProperties byocConfigurationProperties;

    @Mock
    private ClusterRepository clusterRepository;

    @Mock
    IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    SqsMessageSenderClient sqsMessageSenderClient;

    @Mock
    InstanceErrorMetricsService instanceErrorMetricsService;

    @Mock
    InstancePerZoneRepository instancePerZoneRepository;

    @Mock
    NvcaConfigurationProperties nvcaConfigurationProperties;

    @Mock
    NatsConfigurationProperties natsConfigurationProperties;

    @Mock
    NatsMessageSenderClient natsMessageSenderClient;

    @Mock
    GpuUsageEventService gpuUsageEventService;

    @Mock
    CloudHealthRepository cloudHealthRepository;

    @Mock
    ApplicationEventPublisher applicationEventPublisher;

    @Mock
    TelemetryEventClient telemetryEventClient;

    @Mock
    AwsConfigurationProperties awsConfigurationProperties;

    @Mock
    private LatestInstanceStateEventService latestInstanceStateEventService;

    @Mock
    private io.micrometer.tracing.Tracer tracer;

    private InstanceServiceHelper instanceServiceHelper;

    @Captor
    private ArgumentCaptor<NcaIdAccountNameEvent> eventCaptor;

    @Captor
    private ArgumentCaptor<List<GenericMetric>> metricsCaptor;

    @BeforeEach
    void init() {
        objectMapper = customObjectMapper();

        instanceServiceHelper =
                new InstanceServiceHelper(objectMapper, byocConfigurationProperties, clusterRepository,
                                      icmsConfigurationProperties, sqsMessageSenderClient,
                                      instancePerZoneRepository,
                                      instanceErrorMetricsService,
                                      nvcaConfigurationProperties, natsConfigurationProperties,
                                      natsMessageSenderClient, gpuUsageEventService,
                                      cloudHealthRepository,
                                      applicationEventPublisher, telemetryEventClient,
                                      latestInstanceStateEventService,
                                      tracer);
    }

    @Test
    void generateRequestInfo_withValidInputs_returnsSuccess() {

        String functionId = "93fd632f-c0e6-4455-8ae8-4d6657ac6136";
        String functionVersionId = "93fd632f-c0e6-4455-8ae8-4d6657ac6137";

        var instanceRequest = getDummyInstanceRequest(functionId, functionVersionId);
        var response =
                instanceServiceHelper.generateRequestInfo(instanceRequest, DUMMY_CUSTOMER_ID,
                                                      DUMMY_REQUEST_ID, DUMMY_NCA_ID_ACCOUNT_NAME);

        assertNotNull(response);
        assertEquals(
                "{\"requestId\":\"dummy_request_id\",\"sub\":\"dummy_customer_id\",\"spotInstanceRequestAction\":null,\"instanceCount\":1,"
                        + "\"launchSpecification\":{\"instanceType\":\"dummy_gpu_4.small\",\"containerImage\":\"dummy_container_image\","
                        + "\"gpu\":\"dummy_gpu\",\"backend\":\"AWS\",\"ncaId\":null,\"modelCacheEnabled\":false,"
                        + "\"functionId\":\"93fd632f-c0e6-4455-8ae8-4d6657ac6136\",\"versionId\":\"93fd632f-c0e6-4455-8ae8-4d6657ac6137\","
                        + "\"maxRuntimeDuration\":null,\"terminationGracePeriodDuration\":null,\"resultHandlingStrategy\":null,"
                        + "\"deploymentId\":null,\"gpuSpecificationId\":null,\"taskId\":null,"
                        + "\"functionName\":\"dummy_function_name\",\"taskName\":null,\"ncaIdAccountName\":\"dummy_account_name\"}}",
                response);
    }

    @Test
    void getClusterIdsOfClusterToSkipHealthCheck_withClusterEntryPresentInDb_returnSuccess() {
        var ncaId = "nca-id-1";
        var clusterName = "cluster-name-1";

        var mapOfNacIdToClusterName = Map.of(ncaId, clusterName);
        var listOfMapOfNacIdToClusterName = List.of(mapOfNacIdToClusterName);

        doReturn(listOfMapOfNacIdToClusterName).when(byocConfigurationProperties)
                .getSkipHealthCheckClusters();

        var clusterEntity = getDummyClusterEntity();
        clusterEntity.setNcaId(ncaId);
        clusterEntity.setClusterName(clusterName);

        doReturn(Optional.of(clusterEntity)).when(clusterRepository)
                .getClusterByAccountAndName(ncaId, clusterName);

        var clusterIds = instanceServiceHelper.getClusterIdsOfClusterToSkipHealthCheck();

        assertNotNull(clusterIds);
        assertTrue(clusterIds.contains("id"));
    }

    private SpotInstanceRequestSchema getDummyInstanceRequest(
            String functionId, String functionVersionId) {
        return SpotInstanceRequestSchema.builder()
                .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .environment(DUMMY_ENVIRONMENT_VALUE)
                .gpu(DUMMY_GPU_NAME)
                .instanceCount(1)
                .backend(CloudProvider.AWS.toString())
                .containerImage(DUMMY_CONTAINER_IMAGE)
                .functionId(UUID.fromString(functionId))
                .functionName(DUMMY_FUNCTION_NAME)
                .functionVersionId(UUID.fromString(functionVersionId))
                .build();
    }


    @Test
    void sendInstanceTaskError() {
        doNothing().when(instanceErrorMetricsService).recordTaskError(
                DUMMY_NON_BYOC_NCA_ID, DUMMY_ERROR_SOURCE);

        instanceServiceHelper.sendInstanceTaskError(DUMMY_NON_BYOC_NCA_ID, DUMMY_ERROR_SOURCE);

        // Validate
        verify(instanceErrorMetricsService).recordTaskError(DUMMY_NON_BYOC_NCA_ID,
                                                                DUMMY_ERROR_SOURCE);
    }

    @Test
    void parseRequestInfo_withUnrecognizedFieldInInInput_returnsSuccess() {
        ClientRequestDataModel clientRequestDataModelOutput =
                instanceServiceHelper.parseRequestInfo(
                        "{\"instanceCount\":1, \"new_key\": \"new_value\"}");

        // Assert
        assertNotNull(clientRequestDataModelOutput);
        assertEquals(1, clientRequestDataModelOutput.getInstanceCount());
    }

    @Test
    void gpuUsageEventForTerminatedInstance_withUnhealthyCluster_returnWithoutSendingEvent() {
        // Prepare
        InstanceV2Entity entity = TestUtil.getDummyInstanceEntity(
                SpotInstanceInternalState.TERMINATED,
                SpotInstanceRequestState.CLOSED,
                Instant.now(),
                ResourceProvider.BYOC);

        when(cloudHealthRepository.findByCloudAndZone(ResourceProvider.BYOC,
                                                      DUMMY_ZONE)).thenReturn(
                Optional.empty());

        // Act
        instanceServiceHelper.gpuUsageEventForTerminatedInstance(entity);

        // Assert
        verify(gpuUsageEventService, never()).sendGpuUsageEventForTerminatedInstance(entity);
        verify(cloudHealthRepository).findByCloudAndZone(ResourceProvider.BYOC, DUMMY_ZONE);
    }

    @Test
    void gpuUsageEventForTerminatedInstance_withHealthyCluster_sendGpuUsageEvent() {
        // Prepare
        InstanceV2Entity entity = TestUtil.getDummyInstanceEntity(
                SpotInstanceInternalState.TERMINATED,
                SpotInstanceRequestState.CLOSED,
                Instant.now(),
                ResourceProvider.BYOC);

        when(cloudHealthRepository.findByCloudAndZone(ResourceProvider.BYOC,
                                                      DUMMY_ZONE)).thenReturn(
                Optional.of(CloudHealthEntity.builder()
                                    .status(CloudHealthStatus.HEALTHY)
                                    .key(CloudHealthKey.builder()
                                                 .zone(DUMMY_ZONE)
                                                 .cloudProvider(ResourceProvider.BYOC)
                                                 .build())
                                    .build()));

        // Act
        instanceServiceHelper.gpuUsageEventForTerminatedInstance(entity);

        // Assert
        verify(gpuUsageEventService).sendGpuUsageEventForTerminatedInstance(entity);
        verify(cloudHealthRepository).findByCloudAndZone(ResourceProvider.BYOC, DUMMY_ZONE);
    }

    @Test
    void sendNcaIdAccountNameEventAsync_successCase() {
        // Arrange
        String ncaId = "test-nca-id";
        String requestId = "test-request-id";

        // Act
        instanceServiceHelper.sendNcaIdAccountNameEventAsync(ncaId, requestId);

        // Assert
        verify(applicationEventPublisher).publishEvent(eventCaptor.capture());
        NcaIdAccountNameEvent capturedEvent = eventCaptor.getValue();
        assertEquals(ncaId, capturedEvent.getNcaId());
        assertEquals(requestId, capturedEvent.getRequestId());
        verify(telemetryEventClient, never()).triggerEvent(any());
    }

    @Test
    void sendNcaIdAccountNameEventAsync_exceptionCase() {
        // Arrange
        String ncaId = "test-nca-id";
        String requestId = "test-request-id";
        String errorMessage = "Test exception";
        RuntimeException testException = new RuntimeException(errorMessage);

        doThrow(testException).when(applicationEventPublisher)
                .publishEvent(NcaIdAccountNameEvent.builder()
                                      .ncaId(ncaId)
                                      .requestId(requestId)
                                      .build());

        // Act
        instanceServiceHelper.sendNcaIdAccountNameEventAsync(ncaId, requestId);

        // Assert
        verify(applicationEventPublisher).publishEvent(NcaIdAccountNameEvent.builder()
                                                               .ncaId(ncaId)
                                                               .requestId(requestId)
                                                               .build());
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        GenericMetric metric = metricsCaptor.getValue().get(0);
        assertEquals(ASYNC_EVENT_TRIGGER_FAILED.toString(), metric.getEventName());
        assertEquals(errorMessage, metric.getError());
    }

}

