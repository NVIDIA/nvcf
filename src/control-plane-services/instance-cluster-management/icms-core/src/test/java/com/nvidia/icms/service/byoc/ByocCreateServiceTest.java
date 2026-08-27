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
package com.nvidia.icms.service.byoc;

import static com.nvidia.icms.util.TestUtil.DUMMY_CACHE_SIZE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_GROUP_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENVIRONMENT_VALUE;
import static com.nvidia.icms.util.TestUtil.getDummyGpuV5;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.amazonaws.services.sqs.model.QueueAttributeName;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.nvct.ResultHandlingStrategy;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.ngc.NgcRequestHandler;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.outbound.sqs.model.GdnLaunchSpecification;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import com.nvidia.icms.service.FunctionBillingService;
import com.nvidia.icms.service.extensions.api.InstanceCreationService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.extensions.api.LaunchSpecificationService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.uec.IcmsHttpUnifiedErrorException;
import com.nvidia.icms.uec.IcmsUnifiedError;
import com.nvidia.icms.uec.UnifiedErrorReporter;
import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import org.junit.jupiter.api.Test;
import org.mockito.Mockito;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;


@ExtendWith(MockitoExtension.class)
public class ByocCreateServiceTest {

    public static final String BYOC_CREATE_CONSTANT = "byoc-create";
    private static final UUID FUNCTION_ID = UUID.randomUUID();
    private static final UUID FUNCTION_VERSION_ID = UUID.randomUUID();
    private static final String OWNER_NCA_ID = "nca-id";
    private static final String ACCOUNT_NAME = "account_name";

    @Mock
    ClusterRepository clusterRepository;

    @Mock
    InstanceRequestV2Repository instanceRequestV2Repository;

    @Mock
    ByocConfigurationProperties byocConfigurationProperties;

    @Mock
    TelemetryEventClient telemetryEventClient;

    @Mock
    AppAuditService auditService;

    @Mock
    IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    InstanceServiceHelper instanceServiceHelper;
    @Mock
    InstanceCreationService instanceCreationService;
    @Mock
    FunctionBillingService functionBillingService;

    @Mock
    NvcaClusterRepository nvcaClusterRepository;

    @Mock
    NvcaConfigurationProperties nvcaConfigurationProperties;

    @Mock
    ClusterTargetingHelper clusterTargetingHelper;

    @Mock
    ByocServiceHelper byocServiceHelper;

    @Mock
    LaunchSpecificationService launchSpecificationService;

    @Mock
    NgcRequestHandler ngcRequestHandler;

    UnifiedErrorReporter unifiedErrorReporter;

    private ByocCreateService byocCreateService;

    @Captor
    private ArgumentCaptor<List<ByocSqsMessageModel>> byocMessagesCaptor;

    @BeforeEach
    void init() {

        unifiedErrorReporter = new UnifiedErrorReporter(telemetryEventClient);

        ByocMessageGenerator byocMessageGenerator = new ByocMessageGenerator(
                byocConfigurationProperties, byocServiceHelper,
                instanceServiceHelper);

        byocCreateService =
                new ByocCreateService(instanceRequestV2Repository,
                                      byocConfigurationProperties,
                                      telemetryEventClient,
                                      auditService,
                                      instanceServiceHelper,
                                      instanceCreationService,
                                      byocServiceHelper, functionBillingService,
                                      byocMessageGenerator,
                                      launchSpecificationService,
                                      new ComputePlatformService(List.of()));

    }

    //******************************************************************
    // Commented cases
    //******************************************************************
/*
// Convert to just this class
    @Test
    void processCreateRequest_withTaskCreationQueue_validByocRequest_returnsSuccess() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();

        SpotInstanceRequestSchema instanceRequest = getTargetingInstanceRequestSchemaForTask(
                DUMMY_BYOC_INSTANCE_TYPE,
                DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        when(byocConfigurationProperties.getInstanceBatchCount()).thenReturn(1);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());
        doNothing().when(instanceRequestV2Repository).insert(Mockito.any(InstanceRequestV2Entity.class));
        doReturn(true).when(instanceServiceHelper).isTaskClusterCreationQueuesAllowed(Boolean.TRUE);
        doReturn(TaskType.CONTAINER).when(instanceServiceHelper).getTaskType(instanceRequest);

        when(clusterTargetingHelper.getAllClusterHealthInMap())
                .thenReturn(Map.of(DUMMY_CLUSTER_ID,
                                   getDummyCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU_NAME,
                                                             CloudHealthStatus.HEALTHY,
                                                             ResourceProvider.BYOC, 10, 0,
                                                             10)));

        // Mocking NVCA call with valid response
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(Set.of(
                        getDummyClusterGroup(
                                clusterGroupName, ClusterStatusEnum.READY,
                                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME,
                                Set.of(DUMMY_BYOC_AUTHORIZED_NCA_ID), DUMMY_BYOC_NCA_ID,
                                Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE,
                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                                     DUMMY_GPU_NAME)), "region_1",
                                Set.of("attribute_1", "attribute_2", "attribute_3", "attribute_4"),
                                true, true)));

        doNothing().when(instanceServiceHelper)
                .sendTaskMessage(eq("dummy_task_url"), anyList(),
                                 eq(BYOC_CREATE_CONSTANT), eq(DUMMY_CLUSTER_ID));

        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD))
                .thenReturn(Set.of(
                        getDummyClusterGroup(
                                clusterGroupName, ClusterStatusEnum.READY,
                                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME,
                                Set.of(DUMMY_BYOC_AUTHORIZED_NCA_ID), DUMMY_BYOC_NCA_ID,
                                Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE,
                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                                     DUMMY_GPU_NAME)), "region_1",
                                Set.of("attribute_1", "attribute_3", "attribute_4"),
                                true, true)));

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(getDummyClusterEntity(clusterGroupName)));

        when(instanceServiceHelper.generateRequestInfo(
                eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                Mockito.anyString(), eq(null))).thenReturn("dummy_client_request_info");

        // Act
        Optional<CreateSpotInstancesResponse> optionalCreateInstancesResponse =
                byocCreateService.processCreateRequest(
                        DUMMY_CUSTOMER_ID,
                        instanceRequest,
                        new HashMap<>()
                );

        // Assert
        Assertions.assertTrue(optionalCreateInstancesResponse.isPresent());
        CreateSpotInstancesResponse createInstancesResponse =
                optionalCreateInstancesResponse.get();
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertTrue(
                StringUtils.isNotBlank(createInstancesResponse.getRequestId().toString()));

        // Verify
        verify(byocConfigurationProperties).isEnabled();
        verify(byocConfigurationProperties).getInstanceBatchCount();
        verify(instanceServiceHelper).sendTaskMessage(eq("dummy_task_url"),
                                                  anyList(),
                                                  eq(BYOC_CREATE_CONSTANT),
                                                  eq(DUMMY_CLUSTER_ID));
        verify(instanceRequestV2Repository).insert(
                Mockito.argThat(instanceRequestEntity ->
                                        instanceRequestEntity.getCustomer().equals(DUMMY_CUSTOMER_ID) &&
                                                instanceRequestEntity.getRequest()
                                                        .equals("dummy_client_request_info")
                ));

        verify(clusterRepository, times(2)).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(instanceServiceHelper).generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                      Mockito.anyString(), eq(null));
        verify(instanceServiceHelper, times(2)).isTaskClusterCreationQueuesAllowed(Boolean.TRUE);
        verifyNoMoreInteractions(clusterRepository);*
    }

    @Test
    void processInstanceRequest_withTaskCreationQueue_validByocRequest_returnsSuccess() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();

        SpotInstanceRequestSchema instanceRequest = getTargetingInstanceRequestSchemaForTask(
                DUMMY_BYOC_INSTANCE_TYPE,
                DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        when(byocConfigurationProperties.getInstanceBatchCount()).thenReturn(1);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());
        doNothing().when(instanceRequestV2Repository).insert(Mockito.any(InstanceRequestV2Entity.class));
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(false);
        when(instanceServiceHelper.isTaskClusterCreationQueuesAllowed(Boolean.TRUE)).thenReturn(true);
        doReturn(TaskType.CONTAINER).when(instanceServiceHelper).getTaskType(instanceRequest);

        when(clusterTargetingHelper.getAllClusterHealthInMap())
                .thenReturn(Map.of(DUMMY_CLUSTER_ID,
                                   getDummyCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU_NAME,
                                                             CloudHealthStatus.HEALTHY,
                                                             ResourceProvider.BYOC, 10, 0,
                                                             10)));

        // Mocking NVCA call with valid response
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(Set.of(
                        getDummyClusterGroup(
                                clusterGroupName, ClusterStatusEnum.READY,
                                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME  ,  // Match the cluster name in createInstanceRequestSchema
                                Set.of(DUMMY_BYOC_AUTHORIZED_NCA_ID), DUMMY_BYOC_NCA_ID,
                                Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE,
                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                                     DUMMY_GPU_NAME)), "region_1",
                                Set.of("attribute_1", "attribute_2", "attribute_3", "attribute_4"),
                                true, true)));

        doNothing().when(instanceServiceHelper)
                .sendTaskMessage(eq("dummy_task_url"), anyList(),
                                 eq(BYOC_CREATE_CONSTANT), eq(DUMMY_CLUSTER_ID));

        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD))
                .thenReturn(Set.of(
                        getDummyClusterGroup(
                                clusterGroupName, ClusterStatusEnum.READY,
                                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME  ,  // Match the cluster name in createInstanceRequestSchema
                                Set.of(DUMMY_BYOC_AUTHORIZED_NCA_ID), DUMMY_BYOC_NCA_ID,
                                Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE,
                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                                     DUMMY_GPU_NAME)), "region_1",
                                Set.of("attribute_1", "attribute_3", "attribute_4"),
                                true, true)));

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(ClusterEntity.builder()
                                    .clusterId(DUMMY_CLUSTER_ID)
                                    .clusterName(DUMMY_BYOC_CLUSTER_NAME  )  // Match the cluster name in createInstanceRequestSchema
                                    .clusterGroupId(DUMMY_CLUSTER_GROUP_ID)
                                    .clusterGroupName(clusterGroupName)
                                    .clusterStatus(ClusterStatusEnum.READY)
                                    .clusterProvider(ClusterProviderEnum.GDN)
                                    .build()));

        when(instanceServiceHelper.generateRequestInfo(
                eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                anyString(), eq(null))).thenReturn("dummy_client_request_info");

        // Mock icmsConfigurationProperties for ByocValidationService
        when(icmsConfigurationProperties.getCreationQueueUrlFromInstanceType(anyString(), any()))
                .thenReturn("dummy_task_url");

        // Act
        CreateSpotInstancesResponse createInstancesResponse =
                byocCreateService.processCreateRequest(
                        DUMMY_CUSTOMER_ID,
                        instanceRequest,
                        new HashMap<>()
                );

        // Assert
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertTrue(
                StringUtils.isNotBlank(createInstancesResponse.getRequestId().toString()));

        // Verify
        verify(byocConfigurationProperties).getInstanceBatchCount();
        verify(instanceServiceHelper).sendTaskMessage(eq("dummy_task_url"),
                                                  anyList(),
                                                  eq(BYOC_CREATE_CONSTANT),
                                                  eq(DUMMY_CLUSTER_ID));
        verify(instanceRequestV2Repository).insert(
                Mockito.argThat(instanceRequestEntity ->
                                        instanceRequestEntity.getCustomer().equals(DUMMY_CUSTOMER_ID) &&
                                                instanceRequestEntity.getRequest()
                                                        .equals("dummy_client_request_info")
                ));

        verify(clusterRepository, times(2)).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(instanceServiceHelper).generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                      anyString(), eq(null));
        verifyNoMoreInteractions(clusterRepository);
    }

    @Test
    void processInstanceRequest_validByocRequestWithHelmUtilsEnabled_returnsSuccess() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName, DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);
        instanceRequest.setHelmChart("https://abc.def.zyx.com/zyx/charts/name-dummy-v12.3.4.tgz");
        when(byocConfigurationProperties.getInstanceBatchCount()).thenReturn(1);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());
        doNothing().when(instanceRequestV2Repository).insert(Mockito.any(InstanceRequestV2Entity.class));

        when(clusterRepository.getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                DUMMY_BYOC_NCA_ID, clusterGroupName))
                .thenReturn(List.of(getDummyClusterGroupResponse(clusterGroupName,
                                                                 DUMMY_BYOC_NCA_ID,
                                                                 DUMMY_BYOC_INSTANCE_TYPE,
                                                                 DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                                 DUMMY_GPU_NAME, 8)));
        when(clusterRepository.getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                ClusterRepository.WILDCARD, clusterGroupName))
                .thenReturn(List.of());

        when(clusterRepository.getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID)).thenReturn(
                Set.of(getDummyClusterEntity(clusterGroupName)));

        doNothing().when(instanceServiceHelper)
                .sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL), anyList(),
                                     eq(BYOC_CREATE_CONSTANT), eq(null));
        when(kubernetesResourceProvider.generateBase64EncodedPodSpecForHelm(Mockito.any(),
                                                                            Mockito.any(),
                                                                            eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                            eq(8),
                                                                            eq(DUMMY_GPU_NAME),
                                                                            eq(ClusterProviderEnum.GDN.name()),
                                                                            eq(FunctionType.STREAMING))).thenReturn(
                "dummy_pod_spec");

        when(kubernetesResourceProvider.getEmbeddedSecretTemplateForHelm(Mockito.any())).thenReturn(
                "dummy-embedded-secret-template");

        when(kubernetesResourceProvider.injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"))).thenReturn(
                List.of("secret1"));

        when(instanceServiceHelper.generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                   anyString(), eq(null))).thenReturn(
                "dummy_client_request_info");

        when(byocCreateService.processCreateRequest(anyString(), any(), any(), any(), any())).thenReturn(new CreateSpotInstancesResponse(UUID.randomUUID()));

        // Act
        CreateSpotInstancesResponse createInstancesResponse =
                createInstanceService.processInstanceRequest(
                        DUMMY_CUSTOMER_ID,
                        instanceRequest,
                        new HashMap<>()
                );

        // Assert
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertTrue(
                StringUtils.isNotBlank(createInstancesResponse.getRequestId().toString()));

        // Verify
        verify(byocConfigurationProperties).getInstanceBatchCount();
        verify(instanceServiceHelper).sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL),
                                                      anyList(),
                                                      eq(BYOC_CREATE_CONSTANT),
                                                      eq(null));
        verify(instanceRequestV2Repository).insert(Mockito.argThat(instanceRequestEntity ->
                                                                       instanceRequestEntity.getCustomer()
                                                                               .equals(DUMMY_CUSTOMER_ID)
                                                                               &&
                                                                               instanceRequestEntity.getRequest()
                                                                                       .equals("dummy_client_request_info")
        ));
        verify(kubernetesResourceProvider).generateBase64EncodedPodSpecForHelm(Mockito.any(),
                                                                               Mockito.any(),
                                                                               eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                               eq(8),
                                                                               eq(DUMMY_GPU_NAME),
                                                                               eq(ClusterProviderEnum.GDN.name()),
                                                                               eq(FunctionType.STREAMING));
        verify(kubernetesResourceProvider).getEmbeddedSecretTemplateForHelm(Mockito.any());
        verify(kubernetesResourceProvider).injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"));
        verify(telemetryEventClient).triggerEvent(anyList());
        verify(clusterRepository).getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                DUMMY_BYOC_NCA_ID, clusterGroupName);
        verify(clusterRepository).getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(clusterRepository).getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                ClusterRepository.WILDCARD, clusterGroupName);
        verify(instanceServiceHelper).generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                      anyString(), eq(null));
    }


    @Test
    void checkAndProcessByocCreateRequest_withRequestHavingGPUCountAsMinusOne_returnsSuccessWithRequiredGPUAsZero() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName, DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        when(byocConfigurationProperties.isEnabled()).thenReturn(true);
        when(byocConfigurationProperties.getInstanceBatchCount()).thenReturn(1);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());
        doNothing().when(instanceRequestV2Repository).insert(Mockito.any(InstanceRequestV2Entity.class));

        when(clusterRepository.getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                DUMMY_BYOC_NCA_ID, clusterGroupName))
                .thenReturn(List.of(getDummyClusterGroupResponse(clusterGroupName,
                                                                 DUMMY_BYOC_NCA_ID,
                                                                 DUMMY_BYOC_INSTANCE_TYPE,
                                                                 DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                                 DUMMY_GPU_NAME, -1)));
        when(clusterRepository.getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                ClusterRepository.WILDCARD, clusterGroupName))
                .thenReturn(List.of());

        when(clusterRepository.getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID)).thenReturn(
                Set.of(getDummyClusterEntity(clusterGroupName)));

        doNothing().when(instanceServiceHelper)
                .sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL), anyList(),
                                     eq(BYOC_CREATE_CONSTANT), eq(null));

        when(kubernetesResourceProvider.generateBase64EncodedPodSpec(Mockito.any(), Mockito.any(),
                                                                     eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                     eq(RESOURCE_LIMIT_NVIDIA_GPU_KEY),
                                                                     eq(0),
                                                                     eq(DUMMY_GPU_NAME),
                                                                     eq(ClusterProviderEnum.GDN.name()),
                                                                     eq(FunctionType.STREAMING))).thenReturn(
                "dummy_pod_spec");

        when(kubernetesResourceProvider.getEmbeddedSecretTemplate(Mockito.any())).thenReturn(
                "dummy-embedded-secret-template");

        when(kubernetesResourceProvider.injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"))).thenReturn(
                List.of("secret1"));

        when(instanceServiceHelper.generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                   Mockito.anyString(), eq(null))).thenReturn(
                "dummy_client_request_info");

        // Act
        Optional<CreateSpotInstancesResponse> optionalCreateInstancesResponse =
                byocCreateService.checkAndProcessByocCreateRequest(
                        DUMMY_CUSTOMER_ID,
                        instanceRequest,
                        new HashMap<>()
                );

        // Assert
        Assertions.assertTrue(optionalCreateInstancesResponse.isPresent());
        CreateSpotInstancesResponse createInstancesResponse =
                optionalCreateInstancesResponse.get();
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertTrue(
                StringUtils.isNotBlank(createInstancesResponse.getRequestId().toString()));

        // Verify
        verify(byocConfigurationProperties).isEnabled();
        verify(byocConfigurationProperties).getInstanceBatchCount();
        verify(instanceServiceHelper).sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL),
                                                      anyList(),
                                                      eq(BYOC_CREATE_CONSTANT),
                                                      eq(null));
        verify(instanceRequestV2Repository).insert(Mockito.argThat(instanceRequestEntity ->
                                                                       instanceRequestEntity.getCustomer()
                                                                               .equals(DUMMY_CUSTOMER_ID)
                                                                               &&
                                                                               instanceRequestEntity.getRequest()
                                                                                       .equals("dummy_client_request_info")
        ));
        verify(kubernetesResourceProvider).generateBase64EncodedPodSpec(Mockito.any(),
                                                                        Mockito.any(),
                                                                        eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                        eq(RESOURCE_LIMIT_NVIDIA_GPU_KEY),
                                                                        eq(0),
                                                                        eq(DUMMY_GPU_NAME),
                                                                        eq(ClusterProviderEnum.GDN.name()),
                                                                        eq(FunctionType.STREAMING));
        verify(kubernetesResourceProvider).getEmbeddedSecretTemplate(Mockito.any());
        verify(kubernetesResourceProvider).injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"));
        verify(telemetryEventClient).triggerEvent(anyList());
        verify(clusterRepository).getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                DUMMY_BYOC_NCA_ID, clusterGroupName);
        verify(clusterRepository).getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(clusterRepository).getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                ClusterRepository.WILDCARD, clusterGroupName);
        verify(instanceServiceHelper).generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                      Mockito.anyString(), eq(null));
    }


    @Test
    void processInstanceRequest_withNoGPUCountInRequest_returnsSuccessWithRequiredGPUAsOneByDefault() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName, DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        when(byocConfigurationProperties.getInstanceBatchCount()).thenReturn(1);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());
        doNothing().when(instanceRequestV2Repository).insert(Mockito.any(InstanceRequestV2Entity.class));

        when(clusterRepository.getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                DUMMY_BYOC_NCA_ID, clusterGroupName))
                .thenReturn(List.of(getDummyClusterGroupResponse(clusterGroupName,
                                                                 DUMMY_BYOC_NCA_ID,
                                                                 DUMMY_BYOC_INSTANCE_TYPE,
                                                                 DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                                 DUMMY_GPU_NAME, 0)));
        when(clusterRepository.getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                ClusterRepository.WILDCARD, clusterGroupName))
                .thenReturn(List.of());

        when(clusterRepository.getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID)).thenReturn(
                Set.of(getDummyClusterEntity(clusterGroupName)));

        doNothing().when(instanceServiceHelper)
                .sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL), anyList(),
                                     eq(BYOC_CREATE_CONSTANT), eq(null));

        when(kubernetesResourceProvider.generateBase64EncodedPodSpec(Mockito.any(), Mockito.any(),
                                                                     eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                     eq(RESOURCE_LIMIT_NVIDIA_GPU_KEY),
                                                                     eq(1),
                                                                     eq(DUMMY_GPU_NAME),
                                                                     eq(ClusterProviderEnum.GDN.name()),
                                                                     eq(FunctionType.STREAMING))).thenReturn(
                "dummy_pod_spec");

        when(kubernetesResourceProvider.getEmbeddedSecretTemplate(Mockito.any())).thenReturn(
                "dummy-embedded-secret-template");

        when(kubernetesResourceProvider.injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"))).thenReturn(
                List.of("secret1"));

        when(instanceServiceHelper.generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                   anyString(), eq(null))).thenReturn(
                "dummy_client_request_info");

        // Act
        CreateSpotInstancesResponse createInstancesResponse =
                createInstanceService.processInstanceRequest(
                        DUMMY_CUSTOMER_ID,
                        instanceRequest,
                        new HashMap<>()
                );

        // Assert
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertTrue(
                StringUtils.isNotBlank(createInstancesResponse.getRequestId().toString()));

        // Verify
        verify(byocConfigurationProperties).getInstanceBatchCount();
        verify(instanceServiceHelper).sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL),
                                                      anyList(),
                                                      eq(BYOC_CREATE_CONSTANT),
                                                      eq(null));
        verify(instanceRequestV2Repository).insert(Mockito.argThat(instanceRequestEntity ->
                                                                       instanceRequestEntity.getCustomer()
                                                                               .equals(DUMMY_CUSTOMER_ID)
                                                                               &&
                                                                               instanceRequestEntity.getRequest()
                                                                                       .equals("dummy_client_request_info")
        ));
        verify(kubernetesResourceProvider).generateBase64EncodedPodSpec(Mockito.any(),
                                                                        Mockito.any(),
                                                                        eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                        eq(RESOURCE_LIMIT_NVIDIA_GPU_KEY),
                                                                        eq(1),
                                                                        eq(DUMMY_GPU_NAME),
                                                                        eq(ClusterProviderEnum.GDN.name()),
                                                                        eq(FunctionType.STREAMING));
        verify(kubernetesResourceProvider).getEmbeddedSecretTemplate(Mockito.any());
        verify(kubernetesResourceProvider).injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"));
        verify(telemetryEventClient).triggerEvent(anyList());
        verify(clusterRepository).getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                DUMMY_BYOC_NCA_ID, clusterGroupName);
        verify(clusterRepository).getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(clusterRepository).getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                ClusterRepository.WILDCARD, clusterGroupName);
        verify(instanceServiceHelper).generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                      anyString(), eq(null));
    }

    @Test
    void processInstanceRequest_errorInsertingInDB_throwsException() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName, DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        when(byocConfigurationProperties.getInstanceBatchCount()).thenReturn(1);

        when(clusterRepository.getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                DUMMY_BYOC_NCA_ID, clusterGroupName))
                .thenReturn(List.of(getDummyClusterGroupResponse(clusterGroupName,
                                                                 DUMMY_BYOC_NCA_ID,
                                                                 DUMMY_BYOC_INSTANCE_TYPE,
                                                                 DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                                 DUMMY_GPU_NAME, 8)));
        when(clusterRepository.getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                ClusterRepository.WILDCARD, clusterGroupName))
                .thenReturn(List.of());

        when(clusterRepository.getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID)).thenReturn(
                Set.of(getDummyClusterEntity(clusterGroupName)));

        doNothing().when(instanceServiceHelper)
                .sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL), anyList(),
                                     eq(BYOC_CREATE_CONSTANT), eq(null));

        when(kubernetesResourceProvider.generateBase64EncodedPodSpec(Mockito.any(), Mockito.any(),
                                                                     eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                     eq(RESOURCE_LIMIT_NVIDIA_GPU_KEY),
                                                                     eq(8),
                                                                     eq(DUMMY_GPU_NAME),
                                                                     eq(ClusterProviderEnum.GDN.name()),
                                                                     eq(FunctionType.STREAMING))).thenReturn(
                "dummy_pod_spec");

        when(kubernetesResourceProvider.getEmbeddedSecretTemplate(Mockito.any())).thenReturn(
                "dummy-embedded-secret-template");

        when(kubernetesResourceProvider.injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"))).thenReturn(
                List.of("secret1"));

        doThrow(new IcmsInternalServerException("dummy_error")).when(instanceRequestV2Repository)
                .insert(Mockito.any(InstanceRequestV2Entity.class));

        when(instanceServiceHelper.generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                   anyString(), eq(null))).thenReturn(
                "dummy_client_request_info");

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> createInstanceService.processInstanceRequest(
                                                                    DUMMY_CUSTOMER_ID,
                                                                    instanceRequest,
                                                                    new HashMap<>()
                                                            ));

        // Assert
        assertEquals("dummy_error", exception.getBody().getDetail());

        // Verify
        verify(byocConfigurationProperties).getInstanceBatchCount();
        verify(instanceServiceHelper).sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL),
                                                      anyList(),
                                                      eq(BYOC_CREATE_CONSTANT),
                                                      eq(null));
        verify(instanceRequestV2Repository).insert(Mockito.argThat(instanceRequestEntity ->
                                                                       instanceRequestEntity.getCustomer()
                                                                               .equals(DUMMY_CUSTOMER_ID)
                                                                               &&
                                                                               instanceRequestEntity.getRequest()
                                                                                       .equals("dummy_client_request_info")
        ));
        verify(kubernetesResourceProvider).generateBase64EncodedPodSpec(Mockito.any(),
                                                                        Mockito.any(),
                                                                        eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                        eq(RESOURCE_LIMIT_NVIDIA_GPU_KEY),
                                                                        eq(8),
                                                                        eq(DUMMY_GPU_NAME),
                                                                        eq(ClusterProviderEnum.GDN.name()),
                                                                        eq(FunctionType.STREAMING));
        verify(kubernetesResourceProvider).getEmbeddedSecretTemplate(Mockito.any());
        verify(kubernetesResourceProvider).injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"));
        verify(clusterRepository).getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                DUMMY_BYOC_NCA_ID, clusterGroupName);
        verify(clusterRepository).getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(clusterRepository).getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                ClusterRepository.WILDCARD, clusterGroupName);
    }

    @Test
    void processInstanceRequest_validByocWithModelCachingRequest_returnsSuccess() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var clusterEntity = getDummyClusterEntity(clusterGroupName);
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName, DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);
        // Setting artifacts Url as null as it is no longer hard dependency
        instanceRequest.setCacheHandle(DUMMY_CACHE_HANDLE);
        instanceRequest.setCacheArtifacts(true);
        instanceRequest.setCacheSize(5000000000L);

        when(byocConfigurationProperties.getInstanceBatchCount()).thenReturn(1);
        when(byocServiceHelper.isModelCachingEnabled(Mockito.any())).thenReturn(true);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());
        doNothing().when(instanceRequestV2Repository).insert(Mockito.any(InstanceRequestV2Entity.class));

        when(clusterRepository.getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                DUMMY_BYOC_NCA_ID, clusterGroupName))
                .thenReturn(List.of(getDummyClusterGroupResponse(clusterGroupName,
                                                                 DUMMY_BYOC_NCA_ID,
                                                                 DUMMY_BYOC_INSTANCE_TYPE,
                                                                 DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                                 DUMMY_GPU_NAME, 8)));
        when(clusterRepository.getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                ClusterRepository.WILDCARD, clusterGroupName))
                .thenReturn(List.of());

        when(clusterRepository.getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID)).thenReturn(
                Set.of(clusterEntity));

        doNothing().when(instanceServiceHelper)
                .sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL), anyList(),
                                     eq(BYOC_CREATE_CONSTANT), eq(null));

        when(kubernetesResourceProvider.generateBase64EncodedPodSpec(Mockito.any(), Mockito.any(),
                                                                     eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                     eq(RESOURCE_LIMIT_NVIDIA_GPU_KEY),
                                                                     eq(8),
                                                                     eq(DUMMY_GPU_NAME),
                                                                     eq(ClusterProviderEnum.GDN.name()),
                                                                     eq(FunctionType.STREAMING))).thenReturn(
                "dummy_pod_spec");

        when(kubernetesResourceProvider.getEmbeddedSecretTemplate(Mockito.any())).thenReturn(
                "dummy-embedded-secret-template");

        when(kubernetesResourceProvider.injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"))).thenReturn(
                List.of("secret1"));

        when(instanceServiceHelper.generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                   anyString(), eq(null))).thenReturn(
                "dummy_client_request_info");

        when(byocServiceHelper.getRoundedOfCacheSizeInGi(any())).thenReturn("15Gi");

        when(kubernetesResourceProvider.generateBase64EncodedBlockDeviceSpec(DUMMY_CACHE_HANDLE,
                                                                             "15Gi")).thenReturn(
                DUMMY_ENCODED_VALUE);
        when(kubernetesResourceProvider.generateBase64EncodedInitContainerJobSpec(Mockito.any(),
                                                                                  anyString(),
                                                                                  Mockito.any())).thenReturn(
                DUMMY_ENCODED_VALUE);

        // Act
        CreateSpotInstancesResponse createInstancesResponse =
                createInstanceService.processInstanceRequest(
                        DUMMY_CUSTOMER_ID,
                        instanceRequest,
                        new HashMap<>());

        // Assert
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertTrue(
                StringUtils.isNotBlank(createInstancesResponse.getRequestId().toString()));

        // Verify
        verify(byocConfigurationProperties).getInstanceBatchCount();
        verify(instanceServiceHelper).sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL),
                                                      anyList(),
                                                      eq(BYOC_CREATE_CONSTANT),
                                                      eq(null));
        verify(instanceRequestV2Repository).insert(Mockito.argThat(instanceRequestEntity ->
                                                                       instanceRequestEntity.getCustomer()
                                                                               .equals(DUMMY_CUSTOMER_ID)
                                                                               &&
                                                                               instanceRequestEntity.getRequest()
                                                                                       .equals("dummy_client_request_info")
        ));
        verify(kubernetesResourceProvider).generateBase64EncodedPodSpec(Mockito.any(),
                                                                        Mockito.any(),
                                                                        eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                        eq(RESOURCE_LIMIT_NVIDIA_GPU_KEY),
                                                                        eq(8),
                                                                        eq(DUMMY_GPU_NAME),
                                                                        eq(ClusterProviderEnum.GDN.name()),
                                                                        eq(FunctionType.STREAMING));
        verify(kubernetesResourceProvider).getEmbeddedSecretTemplate(Mockito.any());
        verify(kubernetesResourceProvider).injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"));
        verify(telemetryEventClient).triggerEvent(anyList());
        verify(clusterRepository).getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                DUMMY_BYOC_NCA_ID, clusterGroupName);
        verify(clusterRepository).getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(clusterRepository).getClusterGroupsByAccountAndNameInAuthorizedAccounts(
                ClusterRepository.WILDCARD, clusterGroupName);
        verify(instanceServiceHelper).generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                      anyString(), eq(null));
        verify(kubernetesResourceProvider).generateBase64EncodedInitContainerJobSpec(Mockito.any(),
                                                                                     Mockito.any(),
                                                                                     Mockito.any());
        verify(kubernetesResourceProvider).generateBase64EncodedBlockDeviceSpec(DUMMY_CACHE_HANDLE,
                                                                                "15Gi");
        verify(byocServiceHelper, times(2)).isModelCachingEnabled(Mockito.any());
    }


    // NVCA
    @Test
    void processInstanceRequest_validNvcaRequest_returnsSuccess() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName, DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        when(byocConfigurationProperties.getInstanceBatchCount()).thenReturn(1);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());
        doNothing().when(instanceRequestV2Repository).insert(Mockito.any(InstanceRequestV2Entity.class));

        // Mocking NVCA call with valid response
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(List.of(getDummyClustersByAuthorizedAccountResp(clusterGroupName,
                                                                            DUMMY_CLUSTER_GROUP_ID,
                                                                            DUMMY_CLUSTER_ID,
                                                                            DUMMY_BYOC_NCA_ID,
                                                                            DUMMY_BYOC_INSTANCE_TYPE,
                                                                            DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                                            DUMMY_GPU_NAME, 8,
                                                                            DUMMY_BYOC_AUTHORIZED_NCA_ID)));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD))
                .thenReturn(List.of());

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(getDummyClusterEntity(clusterGroupName)));

        doNothing().when(instanceServiceHelper)
                .sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL), anyList(),
                                     eq(BYOC_CREATE_CONSTANT), eq(DUMMY_CLUSTER_ID));

        when(kubernetesResourceProvider.generateBase64EncodedPodSpec(Mockito.any(), Mockito.any(),
                                                                     eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                     eq(RESOURCE_LIMIT_NVIDIA_GPU_KEY),
                                                                     eq(8),
                                                                     eq(DUMMY_GPU_NAME),
                                                                     eq(ClusterProviderEnum.GDN.name()),
                                                                     eq(FunctionType.STREAMING))).thenReturn(
                "dummy_pod_spec");

        when(kubernetesResourceProvider.getEmbeddedSecretTemplate(Mockito.any())).thenReturn(
                "dummy-embedded-secret-template");

        when(kubernetesResourceProvider.injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"))).thenReturn(
                List.of("secret1"));


        when(instanceServiceHelper.generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                   anyString(), eq(null))).thenReturn(
                "dummy_client_request_info");

        // Act
        CreateSpotInstancesResponse createInstancesResponse =
                createInstanceService.processInstanceRequest(
                        DUMMY_CUSTOMER_ID,
                        instanceRequest,
                        new HashMap<>()
                );

        // Assert
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertTrue(
                StringUtils.isNotBlank(createInstancesResponse.getRequestId().toString()));

        // Verify
        verify(byocConfigurationProperties).getInstanceBatchCount();
        verify(instanceServiceHelper).sendFunctionMessage(eq(DUMMY_BYOC_CREATION_QUEUE_URL),
                                                      anyList(),
                                                      eq(BYOC_CREATE_CONSTANT),
                                                      eq(DUMMY_CLUSTER_ID));
        verify(instanceRequestV2Repository).insert(Mockito.argThat(instanceRequestEntity ->
                                                                       instanceRequestEntity.getCustomer()
                                                                               .equals(DUMMY_CUSTOMER_ID)
                                                                               &&
                                                                               instanceRequestEntity.getRequest()
                                                                                       .equals("dummy_client_request_info")
        ));
        verify(kubernetesResourceProvider).generateBase64EncodedPodSpec(Mockito.any(),
                                                                        Mockito.any(),
                                                                        eq(DUMMY_BYOC_INSTANCE_TYPE_VALUE),
                                                                        eq(RESOURCE_LIMIT_NVIDIA_GPU_KEY),
                                                                        eq(8),
                                                                        eq(DUMMY_GPU_NAME),
                                                                        eq(ClusterProviderEnum.GDN.name()),
                                                                        eq(FunctionType.STREAMING));
        verify(kubernetesResourceProvider).getEmbeddedSecretTemplate(Mockito.any());
        verify(kubernetesResourceProvider).injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"));
        verify(telemetryEventClient).triggerEvent(anyList());
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID);
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);
        verify(instanceServiceHelper).generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                      anyString(), eq(null));
        verifyNoMoreInteractions(clusterRepository);
    }

    void checkAndProcessTargetingByocCreateRequest_validNvcaRequest_returnsSuccess() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        when(byocConfigurationProperties.isEnabled()).thenReturn(true);
        when(byocConfigurationProperties.getInstanceBatchCount()).thenReturn(1);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());
        doNothing().when(instanceRequestV2Repository).insert(Mockito.any(InstanceRequestV2Entity.class));

        CloudHealthEntity cloudHealth = CloudHealthEntity.builder()
                .status(CloudHealthStatus.HEALTHY)
                .gpuUsage(Map.of(DUMMY_GPU_NAME, GpuCapacity.builder().available(10)
                        .allocated(0).capacity(10).build()))
                .key(CloudHealthKey.builder()
                             .cloudProvider(ResourceProvider.BYOC)
                             .zone(DUMMY_CLUSTER_ID)
                             .build()).build();

        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(DUMMY_CLUSTER_ID, cloudHealth));

        // Mocking NVCA call with valid response
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(Set.of(
                        getDummyClusterGroup(
                                clusterGroupName, ClusterStatusEnum.READY,
                                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME,
                                Set.of(DUMMY_BYOC_AUTHORIZED_NCA_ID), DUMMY_BYOC_NCA_ID,
                                Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE,
                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                                     DUMMY_GPU_NAME)), "region_1",
                                Set.of("attribute_1", "attribute_2", "attribute_3", "attribute_4"),
                                true, false)));

        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD))
                .thenReturn(Set.of(
                        getDummyClusterGroup(
                                clusterGroupName, ClusterStatusEnum.READY,
                                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME,
                                Set.of(DUMMY_BYOC_AUTHORIZED_NCA_ID), DUMMY_BYOC_NCA_ID,
                                Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE,
                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                                     DUMMY_GPU_NAME)), "region_1",
                                Set.of("attribute_1", "attribute_3", "attribute_4"),
                                true, false)));

        when(clusterTargetingHelper.getAllClusterHealthInMap())
                .thenReturn(Map.of(DUMMY_CLUSTER_ID,
                                   getDummyCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU_NAME,
                                                             CloudHealthStatus.HEALTHY,
                                                             ResourceProvider.BYOC, 10, 0,
                                                             10)));

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(getDummyClusterEntity(clusterGroupName)));

        doNothing().when(instanceServiceHelper)
                .sendFunctionMessage(eq("dummy_url"), anyList(),
                                     eq(BYOC_CREATE_CONSTANT), eq(DUMMY_CLUSTER_ID));

        when(kubernetesResourceProvider.generateBase64EncodedPodSpec(
                any(), any(), any(), any(), anyInt(), any(), any(), any())).thenReturn(
                "dummy_pod_spec");

        when(kubernetesResourceProvider.getEmbeddedSecretTemplate(Mockito.any())).thenReturn(
                "dummy-embedded-secret-template");

        when(kubernetesResourceProvider.injectPodNameAndGenerateBase64EncodedSecretList(
                Mockito.any(), eq("dummy-embedded-secret-template"))).thenReturn(
                List.of("secret1"));

        when(instanceServiceHelper.generateRequestInfo(
                eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                Mockito.anyString(), eq(null))).thenReturn("dummy_client_request_info");

        // Act
        Optional<CreateSpotInstancesResponse> optionalCreateInstancesResponse =
                byocCreateService.checkAndProcessByocCreateRequest(
                        DUMMY_CUSTOMER_ID,
                        instanceRequest,
                        new HashMap<>()
                );

        // Assert
        Assertions.assertTrue(optionalCreateInstancesResponse.isPresent());
        CreateSpotInstancesResponse createInstancesResponse =
                optionalCreateInstancesResponse.get();
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertTrue(
                StringUtils.isNotBlank(createInstancesResponse.getRequestId().toString()));

        // Verify
        verify(byocConfigurationProperties).isEnabled();
        verify(byocConfigurationProperties).getInstanceBatchCount();
        verify(instanceServiceHelper).sendFunctionMessage(eq("dummy_url"),
                                                      anyList(),
                                                      eq(BYOC_CREATE_CONSTANT),
                                                      eq(DUMMY_CLUSTER_ID));
        verify(instanceRequestV2Repository).insert(
                Mockito.argThat(instanceRequestEntity ->
                                        instanceRequestEntity.getCustomer().equals(DUMMY_CUSTOMER_ID) &&
                                                instanceRequestEntity.getRequest()
                                                        .equals("dummy_client_request_info")
                ));

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(instanceServiceHelper).generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                      Mockito.anyString(), eq(null));
        verifyNoMoreInteractions(clusterRepository);
    }


    @Test
    void checkAndProcessByocCreateRequest_withModelCache_validByocTasksRequest_returnsSuccess() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();

        SpotInstanceRequestSchema instanceRequest = getTargetingInstanceRequestSchemaForTask(
                DUMMY_BYOC_INSTANCE_TYPE,
                DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);
        instanceRequest.setCacheArtifacts(true);
        instanceRequest.setCacheHandle(DUMMY_CACHE_HANDLE);
        instanceRequest.setCacheSize(DUMMY_CACHE_SIZE);

        when(byocConfigurationProperties.getInstanceBatchCount()).thenReturn(1);
        when(byocServiceHelper.isModelCachingEnabled(Mockito.any())).thenReturn(true);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());
        doNothing().when(instanceRequestV2Repository).insert(Mockito.any(InstanceRequestV2Entity.class));
        doReturn(true).when(instanceServiceHelper).isTaskClusterCreationQueuesAllowed(Boolean.TRUE);
        doReturn(TaskType.CONTAINER).when(instanceServiceHelper).getTaskType(instanceRequest);

        when(clusterTargetingHelper.getAllClusterHealthInMap())
                .thenReturn(Map.of(DUMMY_CLUSTER_ID,
                                   getDummyCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU_NAME,
                                                             CloudHealthStatus.HEALTHY,
                                                             ResourceProvider.BYOC, 10, 0,
                                                             10)));

        // Mocking NVCA call with valid response
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(Set.of(
                        getDummyClusterGroup(
                                clusterGroupName, ClusterStatusEnum.READY,
                                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME  ,  // Match the cluster name in createInstanceRequestSchema
                                Set.of(DUMMY_BYOC_AUTHORIZED_NCA_ID), DUMMY_BYOC_NCA_ID,
                                Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE,
                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                                     DUMMY_GPU_NAME)), "region_1",
                                Set.of("attribute_1", "attribute_2", "attribute_3", "attribute_4"),
                                true, true)));

        doNothing().when(instanceServiceHelper)
                .sendTaskMessage(eq("dummy_task_url"), anyList(),
                                 eq(BYOC_CREATE_CONSTANT), eq(DUMMY_CLUSTER_ID));

        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD))
                .thenReturn(Set.of(
                        getDummyClusterGroup(
                                clusterGroupName, ClusterStatusEnum.READY,
                                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME  ,  // Match the cluster name in createInstanceRequestSchema
                                Set.of(DUMMY_BYOC_AUTHORIZED_NCA_ID), DUMMY_BYOC_NCA_ID,
                                Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE,
                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                                     DUMMY_GPU_NAME)), "region_1",
                                Set.of("attribute_1", "attribute_3", "attribute_4"),
                                true, true)));

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(getDummyClusterEntity(clusterGroupName)));

        when(instanceServiceHelper.generateRequestInfo(
                eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                anyString(), eq(null))).thenReturn("dummy_client_request_info");

        // Act
        CreateSpotInstancesResponse createInstancesResponse =
                createInstanceService.processInstanceRequest(
                        DUMMY_CUSTOMER_ID,
                        instanceRequest,
                        new HashMap<>()
                );

        // Assert
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertTrue(
                StringUtils.isNotBlank(createInstancesResponse.getRequestId().toString()));

        // Verify
        verify(byocConfigurationProperties).getInstanceBatchCount();
        verify(instanceServiceHelper).sendTaskMessage(eq("dummy_task_url"),
                                                  anyList(),
                                                  eq(BYOC_CREATE_CONSTANT),
                                                  eq(DUMMY_CLUSTER_ID));
        verify(instanceRequestV2Repository).insert(
                Mockito.argThat(instanceRequestEntity ->
                                        instanceRequestEntity.getCustomer().equals(DUMMY_CUSTOMER_ID) &&
                                                instanceRequestEntity.getRequest()
                                                        .equals("dummy_client_request_info")
                ));

        verify(clusterRepository, times(2)).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(instanceServiceHelper).generateRequestInfo(eq(instanceRequest), eq(DUMMY_CUSTOMER_ID),
                                                      anyString(), eq(null));
        verify(instanceServiceHelper, times(2)).isTaskClusterCreationQueuesAllowed(Boolean.TRUE);
        verifyNoMoreInteractions(clusterRepository);
        verify(byocServiceHelper, times(2)).getRoundedOfCacheSizeInBytes(DUMMY_CACHE_SIZE);
    }

    * */

    //******************************************************************
    // GDN Launch Specification - Targeting Flow Tests
    //******************************************************************

    @Test
    void processCreateRequest_gdnValidationFails_throwsBadRequest() {
        SpotInstanceRequestSchema instanceRequest = buildInstanceRequestForFunction();
        Set<RequestInstanceDestination> destinations = Set.of(buildNonByocDestination());
        Map<String, Object> auditProps = new HashMap<>();

        when(launchSpecificationService.resolveGdnLaunchSpecification(
                any(SpotInstanceRequestSchema.class), Mockito.anyMap(), Mockito.anyMap()))
                .thenThrow(new IcmsBadRequestException("GDN registration not found for registrationId"));

        IcmsBadRequestException exception = assertThrows(IcmsBadRequestException.class, () ->
                byocCreateService.processCreateRequest(
                        DUMMY_CUSTOMER_ID, destinations, instanceRequest, auditProps));

        assertTrue(exception.getBody().getDetail().contains("GDN registration not found"));
        verify(instanceCreationService, never())
                .sendSqsMessages(any(), any(), any(), any(), Mockito.anyMap(), any());
        verify(instanceRequestV2Repository, never()).insert(any(InstanceRequestV2Entity.class));
    }

    @Test
    void processCreateRequest_gdnFunctionMismatch_throwsBadRequest() {
        SpotInstanceRequestSchema instanceRequest = buildInstanceRequestForFunction();
        Set<RequestInstanceDestination> destinations = Set.of(buildNonByocDestination());
        Map<String, Object> auditProps = new HashMap<>();

        when(launchSpecificationService.resolveGdnLaunchSpecification(
                any(SpotInstanceRequestSchema.class), Mockito.anyMap(), Mockito.anyMap()))
                .thenThrow(new IcmsBadRequestException(
                        "FunctionId mismatch: instance request functionId does not match GDN registration"));

        IcmsBadRequestException exception = assertThrows(IcmsBadRequestException.class, () ->
                byocCreateService.processCreateRequest(
                        DUMMY_CUSTOMER_ID, destinations, instanceRequest, auditProps));

        assertTrue(exception.getBody().getDetail().contains("FunctionId mismatch"));
        verify(instanceCreationService, never())
                .sendSqsMessages(any(), any(), any(), any(), Mockito.anyMap(), any());
    }

    @Test
    void processCreateRequest_byocTaskForwardsModelsUnchanged() {
        SpotInstanceRequestSchema instanceRequest = getTargetingInstanceRequestSchemaForTask(
                DUMMY_BYOC_INSTANCE_TYPE, "A100", OWNER_NCA_ID);
        instanceRequest.setModels("[{\"name\":\"model-1\"}]");

        RequestInstanceDestination destination = RequestInstanceDestination.builder()
                .instanceType(InstanceTypeV5Udt.builder()
                        .name(DUMMY_BYOC_INSTANCE_TYPE)
                        .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                        .gpuCount(8)
                        .build())
                .clusterGroupName("NVCA_TARGETING")
                .clusterGroupId(DUMMY_CLUSTER_GROUP_ID)
                .creationQueueUrl("dummy-task-url")
                .ncaId(OWNER_NCA_ID)
                .authorizedNcaIds(Set.of(OWNER_NCA_ID))
                .cloudProvider(CloudProvider.AWS)
                .clusterId(DUMMY_CLUSTER_ID)
                .gpuName("A100")
                .instanceBatchCount(1)
                .capacityType(CapacityType.SPOT)
                .maxFulfillableInstances(instanceRequest.getInstanceCount())
                .build();

        when(launchSpecificationService.resolveGdnLaunchSpecification(
                any(SpotInstanceRequestSchema.class), Mockito.anyMap(), Mockito.anyMap()))
                .thenReturn(null);
        when(byocConfigurationProperties.getEnv()).thenReturn("stage");
        when(instanceServiceHelper.generateRequestInfo(
                eq(instanceRequest), eq(DUMMY_CUSTOMER_ID), anyString(), eq(null)))
                .thenReturn("dummy_request_info");
        doNothing().when(instanceRequestV2Repository).insert(any(InstanceRequestV2Entity.class));
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.anyList());
        doNothing().when(instanceServiceHelper).sendTaskMessage(
                eq("dummy-task-url"), anyList(), eq(BYOC_CREATE_CONSTANT), eq(DUMMY_CLUSTER_ID));

        CreateSpotInstancesResponse response = byocCreateService.processCreateRequest(
                DUMMY_CUSTOMER_ID, Set.of(destination), instanceRequest, new HashMap<>());

        assertNotNull(response);
        verify(instanceServiceHelper).sendTaskMessage(
                eq("dummy-task-url"), byocMessagesCaptor.capture(),
                eq(BYOC_CREATE_CONSTANT), eq(DUMMY_CLUSTER_ID));
        assertEquals(instanceRequest.getModels(),
                     byocMessagesCaptor.getValue().getFirst().getLaunchSpecification().getModels());
    }

    private SpotInstanceRequestSchema buildInstanceRequestForFunction() {
        return SpotInstanceRequestSchema.builder()
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .instanceCount(1)
                .instanceType("dummy_gpu_1.large")
                .containerImage(DUMMY_CONTAINER_IMAGE)
                .environment(DUMMY_ENVIRONMENT_VALUE)
                .gpu("DUMMY_GPU_1")
                .ncaId(OWNER_NCA_ID)
                .cacheSize(DUMMY_CACHE_SIZE)
                .maxRuntimeDuration(Duration.parse("PT2H"))
                .resultHandlingStrategy(ResultHandlingStrategy.NONE)
                .ownerNcaId(OWNER_NCA_ID)
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .build();
    }

    private RequestInstanceDestination buildNonByocDestination() {
        return RequestInstanceDestination.builder()
                .instanceType(InstanceTypeV5Udt.builder()
                        .name("dummy_gpu_1.large")
                        .value("dummy_gpu_1.large")
                        .gpuCount(1)
                        .build())
                .clusterGroupName("NONBYOC_REGION_TARGETING")
                .clusterGroupId(DUMMY_CLUSTER_GROUP_ID)
                .creationQueueUrl("dummy-nonbyoc-queue")
                .ncaId(OWNER_NCA_ID)
                .authorizedNcaIds(Set.of(OWNER_NCA_ID))
                .gpuName("DUMMY_GPU_1")
                .capacityType(CapacityType.SPOT)
                .maxFulfillableInstances(1)
                .build();
    }

    private SpotInstanceRequestSchema getTargetingInstanceRequestSchemaForTask(
            String instanceType,
            String gpu,
            String ncaId) {

        return SpotInstanceRequestSchema.builder()
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .instanceCount(1)
                .instanceType(instanceType)
                .containerImage(DUMMY_CONTAINER_IMAGE)
                .environment(DUMMY_ENVIRONMENT_VALUE)
                .gpu(gpu)
                .ncaId(ncaId)
                .cacheSize(DUMMY_CACHE_SIZE)
                .maxRuntimeDuration(Duration.parse("PT2H"))
                .maxQueuedDuration(Duration.parse("PT60M"))
                .terminationGracePeriodDuration(Duration.parse("PT1H"))
                .resultHandlingStrategy(ResultHandlingStrategy.NONE)
                .accountName(ACCOUNT_NAME)
                .ownerNcaId(OWNER_NCA_ID)
                .taskId(UUID.randomUUID())
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .build();
    }



    private ClusterEntity getDummyClusterEntity(String clusterGroupName) {
        return getDummyClusterEntity(clusterGroupName, ClusterProviderEnum.GDN);
    }

    private ClusterEntity getDummyClusterEntity(
            String clusterGroupName,
            ClusterProviderEnum clusterProviderEnum) {
        return ClusterEntity.builder()
                .clusterGroupId(DUMMY_CLUSTER_GROUP_ID)
                .clusterGroupName(clusterGroupName)
                .clusterId(DUMMY_CLUSTER_ID)
                .clusterName("dummy_cluster_name")
                .clusterStatus(ClusterStatusEnum.READY)
                .clusterProvider(clusterProviderEnum)
                .allowClusterTargeting(true)
                .clusterKeyId(DUMMY_CLUSTER_ID)
                .build();
    }

    private ClusterByGroupIdAndIdEntity getDummyClusterGroup(
            String clusterGroupName,
            ClusterStatusEnum clusterStatusEnum,
            String clusterId,
            String clusterName,
            Set<String> authorizedNcaId,
            String ncaId,
            Set<GpuV5Udt> gpuV5Set,
            String region,
            Set<String> attributes,
            boolean allowClusterTargeting,
            boolean allowTaskQueues) {
        var creationQueueMap = gpuV5Set.stream().collect(
                Collectors.toMap(GpuV5Udt::getName,
                                 gpuV5 -> CreationQueueUdt.builder()
                                         .queueType(QueueAttributeName.FifoQueue.toString())
                                         .url("dummy_url")
                                         .build()));

        var clusterCreationQueueMap = gpuV5Set.stream().collect(
                Collectors.toMap(GpuV5Udt::getName,
                                 gpuV5 -> CreationQueueUdt.builder()
                                         .queueType(QueueAttributeName.FifoQueue.toString())
                                         .url("dummy_task_url")
                                         .build()));

        return ClusterByGroupIdAndIdEntity.builder()
                .clusterGroupName(clusterGroupName)
                .key(ClusterByGroupIdAndIdKey.builder()
                             .clusterGroupId(DUMMY_CLUSTER_GROUP_ID)
                             .clusterId(clusterId)
                             .build())
                .clusterDescription("dummy_cluster_description")
                .k8sVersion("v1.25.9")
                .clusterStatus(clusterStatusEnum)
                .clusterName(clusterName)
                .authorizedNcaIds(authorizedNcaId)
                .ncaId(ncaId)
                .gpusV5(gpuV5Set)
                .clusterCreationQueues(creationQueueMap)
                .region(region)
                .attributes(attributes)
                .customAttributes(Set.of("cAttribute_1", "cAttribute_2"))
                .allowClusterTargeting(allowClusterTargeting)
                .allowTaskClusterCreationQueues(allowTaskQueues)
                .clusterCreationQueuesForTasks(clusterCreationQueueMap)
                .clusterProvider(ClusterProviderEnum.AWS)
                .build();
    }




    private void assertUnifiedErrorException(@NotNull IcmsHttpUnifiedErrorException exception,
                                             @NotNull IcmsUnifiedError icmsUnifiedError,
                                             @NotNull SpotInstanceRequestSchema instanceRequestSchema,
                                             String ncaId) {
        assertEquals(icmsUnifiedError, exception.unifiedError());

        assertNull(exception.unifiedErrorData().getRequestId());
        assertNull(exception.unifiedErrorData().getInstanceId());

        assertEquals(instanceRequestSchema.getFunctionId().toString(), exception.unifiedErrorData().getFunctionId());
        assertEquals(instanceRequestSchema.getFunctionVersionId().toString(), exception.unifiedErrorData().getFunctionVersionId());
        assertNull(exception.unifiedErrorData().getTaskId());

        assertEquals(instanceRequestSchema.getDeploymentId().toString(), exception.unifiedErrorData().getDeploymentId());
        assertEquals(instanceRequestSchema.getGpuSpecificationId().toString(), exception.unifiedErrorData().getGpuSpecificationId());
        assertEquals(ncaId, exception.unifiedErrorData().getNcaId());

    }


}
