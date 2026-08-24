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

import com.amazonaws.services.sqs.model.QueueAttributeName;
import com.nvidia.icms.service.extensions.api.ReservationProcessor;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.extensions.impl.NoOpInstanceDestinationProvider;
import com.nvidia.icms.configuration.aws.AwsConfigurationProperties;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.FunctionType;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.nvct.ResultHandlingStrategy;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleHelper;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.extensions.api.InstanceValidationService;
import com.nvidia.icms.service.byoc.ByocCreateService;
import com.nvidia.icms.service.byoc.ByocValidationService;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.uec.IcmsHttpUnifiedErrorException;
import com.nvidia.icms.uec.IcmsUnifiedError;
import com.nvidia.icms.uec.UnifiedErrorReporter;
import jakarta.validation.constraints.NotNull;
import org.apache.commons.lang3.StringUtils;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

import java.time.Duration;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;

import static com.nvidia.icms.service.platform.ComputePlatformTestFixtures.PLATFORM_CLUSTER_GROUP_ID;
import static com.nvidia.icms.service.platform.ComputePlatformTestFixtures.PLATFORM_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_READY_CLUSTER;
import static com.nvidia.icms.util.TestUtil.BASE64_ENCODED_TELEMETRIES;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_AUTHORIZED_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CREATION_QUEUE_URL;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CACHE_SIZE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_GROUP_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENVIRONMENT_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_OCI_NCA_ID;
import static com.nvidia.icms.util.TestUtil.getDummyCloudHealthEntity;
import static com.nvidia.icms.util.TestUtil.getDummyClustersByAuthorizedAccountResp;
import static com.nvidia.icms.util.TestUtil.getDummyGpuV5;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

@ExtendWith(MockitoExtension.class)
class CreateInstanceServiceTest extends CreateInstancesTestBase {

    private static final String TEST_CUSTOMER = "testCustomer";

    private static final String  TEST_NCA_ID = "test-nca-id";
    private static final String INVALID_INSTANCE_TYPE = "invalid-instance-type";

    private static final UUID FUNCTION_ID = UUID.randomUUID();
    private static final UUID FUNCTION_VERSION_ID = UUID.randomUUID();
    private static final String OWNER_NCA_ID = "nca-id";
    public static final String BYOC_CREATE_CONSTANT = "byoc-create";
    private static final String ACCOUNT_NAME = "account_name";
    private static final String CUSTOM_GROUP = "custom-group";
    @Mock
    private ByocCreateService byocCreateService;
    @Mock
    private InstanceLifecycleService instanceLifecycleService;
    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;
    @Mock
    private ByocConfigurationProperties byocConfigurationProperties;
    @Mock
    private InstanceValidationService instanceValidationService;

    private RequestDestinationProvider requestDestinationProvider;

    @Mock
    private ClusterRepository clusterRepository;
    @Mock
    private InstanceServiceHelper instanceServiceHelper;
    @Mock
    private InstanceLifecycleHelper instanceLifecycleHelper;
    @Mock
    ClusterTargetingHelper clusterTargetingHelper;
    @Mock
    TelemetryEventClient telemetryEventClient;
    @Mock
    InstanceRequestV2Repository instanceRequestV2Repository;
    @Mock
    NvcaClusterRepository nvcaClusterRepository;

    @Mock
    AwsConfigurationProperties awsConfigurationProperties;

    @Mock
    QueueManager queueManager;

    @Mock
    ReservationProcessor reservationProcessor;

    UnifiedErrorReporter unifiedErrorReporter;

    private CreateInstanceService createInstanceService;

    private ByocValidationService byocValidationService;

    private DestinationCreator destinationCreator;

    @BeforeEach
    void setUp() {
        unifiedErrorReporter = new UnifiedErrorReporter(telemetryEventClient);

        ComputePlatformService computePlatformService = ComputePlatformTestFixtures.nonByocComputePlatformService();

        byocValidationService = new ByocValidationService(
                clusterRepository,
                icmsConfigurationProperties,
                instanceServiceHelper,
                instanceLifecycleHelper,
                computePlatformService
        );

        destinationCreator = new DestinationCreator(
                byocValidationService,
                icmsConfigurationProperties,
                byocConfigurationProperties,
                computePlatformService);

        NoOpInstanceDestinationProvider instanceDestinationProvider =
                new NoOpInstanceDestinationProvider();

        ByocRequestDestinationProvider byocRequestDestinationProvider =
                new ByocRequestDestinationProvider(
                        byocValidationService,
                        nvcaClusterRepository,
                        destinationCreator,
                        computePlatformService);

        requestDestinationProvider = new RequestDestinationProvider(
                instanceDestinationProvider,
                byocRequestDestinationProvider,
                clusterTargetingHelper,
                unifiedErrorReporter,
                computePlatformService);

        createInstanceService = new CreateInstanceService(
                byocCreateService,
                instanceLifecycleService,
                byocValidationService,
                icmsConfigurationProperties,
                requestDestinationProvider,
                clusterTargetingHelper,
                telemetryEventClient,
                reservationProcessor,
                computePlatformService);

        lenient().when(awsConfigurationProperties.getQueuePerInstanceNameFormat()).thenReturn("sqs_%s.fifo");
    }

    /**
     * Tests the flow when targeting is enabled (backend is null).
     * This scenario occurs when a user wants to target specific clusters or regions.
     * The test verifies that:
     * 1. The request is processed through the targeted path
     * 2. Helm chart validation is performed
     * 3. NVCT validation is performed
     * 4. The request is processed by ByocCreateService
     */
    @Test
    void processInstanceRequest_WhenTargetingEnabled_ShouldProcessTargetedRequest() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
            "instance-type",
            ResourceProvider.BYOC.toString(),
            DUMMY_GPU,
            DUMMY_BYOC_NCA_ID,
            true
        );
        Map<String, Object> auditProps = new HashMap<>();
        CreateSpotInstancesResponse expectedResponse = new CreateSpotInstancesResponse();

        // Setup common mocks for targeted request
        setupTargetedRequestMocks(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME,
                                DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_GPU, "instance-type");

        // Setup ByocCreateService mock
        setupByocCreateServiceMock(instanceRequest, auditProps, expectedResponse);

        CloudHealthEntity cloudHealth = createCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 5, 5);
        Map<String, CloudHealthEntity> healthMap = new HashMap<>();
        healthMap.put(DUMMY_CLUSTER_ID, cloudHealth);
        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(healthMap);

        mockNonByocReservationProcessorWithNoOp();

        // Act
        CreateSpotInstancesResponse response = createInstanceService.processInstanceRequest(
            TEST_CUSTOMER,
            instanceRequest,
            auditProps
        );

        // Assert
        assertSuccessfulRequestProcessing(response, expectedResponse, instanceRequest, auditProps);
        verifyTargetedRequestProcessing(DUMMY_CLUSTER_ID);
    }

    /**
     * Tests the flow when targeting is disabled (backend is set) and the request is for a BYOC provider (AWS).
     * This scenario occurs when a user wants to use the default routing logic.
     * The test verifies that:
     * 1. The request is processed through the non-targeted path
     * 2. Helm chart validation is performed
     * 3. NVCT validation is performed
     * 4. The request is processed by ByocCreateService with the correct destination
     */
    @Test
    void processInstanceRequest_WhenTargetingDisabled_ShouldProcessNonTargetedRequest() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
                DUMMY_NON_BYOC_INSTANCE_TYPE,
                DUMMY_BYOC_CLUSTER_GROUP_NAME  ,  // Match the cluster group name
                DUMMY_GPU,
                DUMMY_BYOC_NCA_ID,
            false
        );
        Map<String, Object> auditProps = new HashMap<>();
        CreateSpotInstancesResponse expectedResponse = new CreateSpotInstancesResponse();

        // Setup common mocks for non-targeted request
        ClustersByAuthorizedAccountsEntity cluster = createClusterForByoc(DUMMY_BYOC_NCA_ID, DUMMY_BYOC_CLUSTER_GROUP_NAME, PLATFORM_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID)).thenReturn(List.of(cluster));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD)).thenReturn(List.of());

        ClusterEntity clusterEntity = toClusterEntity(cluster);
        clusterEntity.setClusterStatus(ClusterStatusEnum.READY);
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(clusterEntity));

        // Setup ByocCreateService mock
        setupByocCreateServiceMock(instanceRequest, auditProps, expectedResponse);

        CloudHealthEntity cloudHealth = createCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 5, 5);
        Map<String, CloudHealthEntity> healthMap = new HashMap<>();
        healthMap.put(DUMMY_CLUSTER_ID, cloudHealth);
        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(healthMap);

        mockNonByocReservationProcessorWithNoOp();

        // Act
        CreateSpotInstancesResponse response = createInstanceService.processInstanceRequest(
            TEST_CUSTOMER,
            instanceRequest,
            auditProps
        );

        // Assert
        assertSuccessfulRequestProcessing(response, expectedResponse, instanceRequest, auditProps);
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID);
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
    }

    /**
     * Tests the targeted instance request flow with valid NCA ID and GPU.
     * This scenario occurs when a user wants to target specific clusters with valid parameters.
     * The test verifies that:
     * 1. The request is processed with valid NCA ID and GPU
     * 2. The correct destinations are retrieved
     * 3. The request is processed by ByocCreateService
     * 4. The Non BYOC destination set is empty when no Non BYOC clusters are present
     */
    @Test
    void processTargetedInstanceRequest_WithValidParameters_ShouldProcessRequest() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
            "instance-type",
            null,
            DUMMY_GPU,
            TEST_NCA_ID,
            true
        );
        Map<String, Object> auditProps = new HashMap<>();
        CreateSpotInstancesResponse expectedResponse = new CreateSpotInstancesResponse();

        // Setup common mocks for targeted request
        setupTargetedRequestMocks(TEST_NCA_ID, "cluster-id", DUMMY_BYOC_CLUSTER_NAME  ,
                                  DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_GPU, "instance-type");

        // Mock the dependencies that RequestDestinationProvider.getAllTargetedDestinations() uses
        ClusterByGroupIdAndIdEntity clusterEntity = getDummyClusterGroup(
                DUMMY_BYOC_CLUSTER_GROUP_NAME,
            ClusterStatusEnum.READY,
            "cluster-id",
            DUMMY_BYOC_CLUSTER_NAME  ,
            Set.of(TEST_NCA_ID),
            TEST_NCA_ID,
            Set.of(GpuV5Udt.builder()
                .name(DUMMY_GPU)
                .instanceTypes(Set.of(InstanceTypeV5Udt.builder()
                    .name("instance-type")
                    .value("instance-type-value")
                    .gpuCount(1)
                    .build()))
                .build()),
            "region",
            Set.of(),
            true,
            true
        );
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(TEST_NCA_ID))
            .thenReturn(Set.of(clusterEntity));
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD))
            .thenReturn(Set.of());

        // Mock cloud health
        CloudHealthEntity cloudHealth = createCloudHealthEntity("cluster-id", DUMMY_GPU, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 5, 5);
        Map<String, CloudHealthEntity> healthMap = new HashMap<>();
        healthMap.put("cluster-id", cloudHealth);

        when(byocCreateService.processCreateRequest(
            eq(TEST_CUSTOMER),
            any(Set.class),
            eq(instanceRequest),
            eq(auditProps)
        )).thenReturn(expectedResponse);
        mockNonByocReservationProcessorWithNoOp();

        // Act
        CreateSpotInstancesResponse response = createInstanceService.processTargetedInstanceRequest(
            TEST_CUSTOMER, instanceRequest, auditProps, healthMap);

        // Assert
        assertEquals(expectedResponse, response);

        // Verify that ByocCreateService was called with the correct parameters
        verify(byocCreateService).processCreateRequest(
            eq(TEST_CUSTOMER),
            any(Set.class),
            eq(instanceRequest),
            eq(auditProps)
        );
    }

    /**
     * Tests the targeted instance request flow with missing NCA ID.
     * This scenario occurs when a user tries to target specific clusters without providing an NCA ID.
     * The test verifies that:
     * 1. The request fails validation when NCA ID is missing
     * 2. A IcmsBadRequestException is thrown
     */
    @Test
    void processInstanceRequest_WithMissingNcaId_ShouldThrowException() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
            "instance-type",
            "backend",
            DUMMY_GPU,
            null,
            true
        );
        Map<String, Object> auditProps = new HashMap<>();


        // Act & Assert
        assertThrows(IcmsBadRequestException.class, () ->
            createInstanceService.processInstanceRequest(TEST_CUSTOMER, instanceRequest, auditProps)
        );
    }

    /**
     * Tests the targeted instance request flow with missing GPU.
     * This scenario occurs when a user tries to target specific clusters without providing a GPU.
     * The test verifies that:
     * 1. The request fails validation when GPU is missing
     * 2. A IcmsBadRequestException is thrown
     */
    @Test
    void processTargetedInstanceRequest_WithMissingGpu_ShouldThrowException() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
            "instance-type",
            "backend",
            null,
            TEST_NCA_ID,
            true
        );
        Map<String, Object> auditProps = new HashMap<>();

        // Act & Assert
        assertThrows(IcmsBadRequestException.class, () ->
            createInstanceService.processInstanceRequest(TEST_CUSTOMER, instanceRequest, auditProps)
        );
    }

    /**
     * Tests the extraction of Non BYOC destinations when input is empty.
     * This scenario occurs when no destinations are provided.
     * The test verifies that:
     * 1. An empty set is returned
     * 2. No processing is performed
     */
    @Test
    void keepNonByocAsSingleDestinationPerRegion_WithEmptyInput_ShouldReturnEmptySet() {
        // Arrange
        Set<RequestInstanceDestination> destinations = new HashSet<>();

        // Act
        Set<RequestInstanceDestination> result = createInstanceService.keepNonByocAsSingleDestinationPerRegion(
            destinations);

        // Assert
        assertTrue(result.isEmpty());
    }

    /**
     * Tests {@code keepNonByocAsSingleDestinationPerRegion} when the input has no Non BYOC clusters
     * (only a BYOC/AWS destination). Only Non BYOC destinations are collapsed to one per region;
     * BYOC destinations pass through unchanged.
     * The test verifies that the single BYOC destination is retained (returned set has size 1).
     */
    @Test
    void keepNonByocAsSingleDestinationPerRegion_WithNoNonByocClusters_ShouldReturnEmptySet() {
        // Arrange
        Set<RequestInstanceDestination> destinations = new HashSet<>();
        RequestInstanceDestination awsDestination = createDestination(CloudProvider.AWS);
        destinations.add(awsDestination);

        // Act
        Set<RequestInstanceDestination> result = createInstanceService.keepNonByocAsSingleDestinationPerRegion(
            destinations);

        // Assert
        assertEquals(1, result.size());
    }

    /**
     * Tests the extraction of Non BYOC destinations with single region.
     * This scenario occurs when all Non BYOC clusters are in the same region.
     * The test verifies that:
     * 1. Only one destination per region is returned
     * 2. The correct region is preserved
     */
    @Test
    void keepNonByocAsSingleDestinationPerRegion_WithSingleRegion_ShouldReturnOneDestination() {
        // Arrange
        Set<RequestInstanceDestination> destinations = new HashSet<>();

        // Add two Non BYOC destinations in the same region
        RequestInstanceDestination nonByocDestination1 = createDestination(CloudProvider.OCI);
        nonByocDestination1.setClusterGroupName(PLATFORM_CLUSTER_GROUP_NAME);
        nonByocDestination1.setRegion("region1");
        destinations.add(nonByocDestination1);

        RequestInstanceDestination nonByocDestination2 = createDestination(CloudProvider.OCI);
        nonByocDestination2.setClusterGroupName(PLATFORM_CLUSTER_GROUP_NAME);
        nonByocDestination2.setRegion("region1");
        destinations.add(nonByocDestination2);

        // Act
        Set<RequestInstanceDestination> result = createInstanceService.keepNonByocAsSingleDestinationPerRegion(
            destinations);

        // Assert
        assertEquals(1, result.size());
        RequestInstanceDestination resultDestination = result.iterator().next();
        assertEquals("region1", resultDestination.getRegion());
    }

    /**
     * Tests the extraction of Non BYOC destinations with multiple regions.
     * This scenario occurs when Non BYOC clusters are spread across different regions.
     * The test verifies that:
     * 1. One destination per region is returned
     * 2. All regions are represented
     * 3. The correct regions are preserved
     */
    @Test
    void extractNonByocAsSingleDestinationPerRegion_WithMultipleRegions_ShouldReturnOneDestinationPerRegion() {
        // Arrange
        Set<RequestInstanceDestination> destinations = new HashSet<>();

        // Add Non BYOC destinations in different regions
        RequestInstanceDestination nonByocDestination1 = createDestination(CloudProvider.OCI);
        nonByocDestination1.setRegion("region1");
        nonByocDestination1.setClusterGroupName(PLATFORM_CLUSTER_GROUP_NAME);
        destinations.add(nonByocDestination1);

        RequestInstanceDestination nonByocDestination2 = createDestination(CloudProvider.OCI);
        nonByocDestination2.setRegion("region2");
        nonByocDestination2.setClusterGroupName(PLATFORM_CLUSTER_GROUP_NAME);
        destinations.add(nonByocDestination2);

        // Act
        Set<RequestInstanceDestination> result = createInstanceService.keepNonByocAsSingleDestinationPerRegion(
            destinations);

        // Assert
        assertEquals(2, result.size());
        Set<String> regions = result.stream()
            .map(RequestInstanceDestination::getRegion)
            .collect(Collectors.toSet());
        assertEquals(Set.of("region1", "region2"), regions);
    }

    /**
     * Tests the extraction of Non BYOC destinations with mixed cloud providers.
     * This scenario occurs when there are both Non BYOC and BYOC destinations.
     * The test verifies that:
     * 1. Only Non BYOC destinations are processed
     * 2. One destination per region is returned for Non BYOC
     * 3. BYOC destinations are ignored
     */
    @Test
    void extractNonByocAsSingleDestinationPerRegion_WithMixedCloudProviders_ShouldReturnNonByocDestinationsOnly() {
        // Arrange

        Set<RequestInstanceDestination> destinations = new HashSet<>();

        // Add AWS destination
        RequestInstanceDestination awsDestination = createDestination(CloudProvider.AWS);
        destinations.add(awsDestination);

        // Add Non BYOC destinations in different regions
        RequestInstanceDestination nonByocDestination1 = createDestination(CloudProvider.OCI);
        nonByocDestination1.setRegion("region1");
        nonByocDestination1.setClusterGroupName(PLATFORM_CLUSTER_GROUP_NAME);
        destinations.add(nonByocDestination1);

        RequestInstanceDestination nonByocDestination2 = createDestination(CloudProvider.OCI);
        nonByocDestination2.setRegion("region2");
        nonByocDestination2.setClusterGroupName(PLATFORM_CLUSTER_GROUP_NAME);
        destinations.add(nonByocDestination2);

        // Act
        Set<RequestInstanceDestination> result = createInstanceService.keepNonByocAsSingleDestinationPerRegion(
            destinations);

        // Assert
        assertEquals(3, result.size());
    }

    //////////////////////////////////////////////////////
    // Tests from original ByocCreateServiceTest
    ////////////////////////////////////////////////////////

    @Test
    void processInstanceRequest_ncaIdNotProvided_throwsException() {

        // Prepare
        doNothing().when(instanceValidationService).validationForNvct(any());

        // Act
        IcmsBadRequestException exception = assertThrows(IcmsBadRequestException.class,
                                                        () -> createInstanceService.processInstanceRequest(
                                                                DUMMY_CUSTOMER_ID,
                                                                getNonTargetingInstanceRequestSchema(
                                                                        DUMMY_BYOC_INSTANCE_TYPE,
                                                                        "dummy_cluster_group",
                                                                        "A100_80GB",
                                                                        null), Map.of()));

        // Assert
        Assertions.assertEquals("LaunchSpecification.NcaId can't be empty",
                                exception.getBody().getDetail());

        // Verify
        verify(instanceValidationService).validationForNvct(any());
    }


    @Test
    void processInstanceRequest_gpuNotProvided_throwsException() {

        // Prepare
        doNothing().when(instanceValidationService).validationForNvct(any());

        // Act
        IcmsBadRequestException exception = assertThrows(IcmsBadRequestException.class,
                                                        () -> createInstanceService.processInstanceRequest(
                                                                DUMMY_CUSTOMER_ID,
                                                                getNonTargetingInstanceRequestSchema(
                                                                        DUMMY_BYOC_INSTANCE_TYPE,
                                                                        "dummy_cluster_group", null,
                                                                        null), Map.of()));

        // Assert
        Assertions.assertEquals("LaunchSpecification.NcaId can't be empty",
                                exception.getBody().getDetail());

        // Verify
        verify(instanceValidationService).validationForNvct(any());
        verifyNoInteractions(instanceRequestV2Repository);
    }



    @Test
    void processInstanceRequest_withBackendNotFound_success() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        doNothing().when(instanceValidationService).validationForNvct(any());

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_NON_BYOC_NCA_ID)).thenReturn(List.of());
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD)).thenReturn(List.of());

        SpotInstanceRequestSchema instanceRequestSchema = getNonTargetingInstanceRequestSchema(
                DUMMY_NON_BYOC_INSTANCE_TYPE,
                clusterGroupName, "DUMMY_GPU_4",
                DUMMY_NON_BYOC_NCA_ID);

        //Act
        IcmsHttpUnifiedErrorException exception = assertThrows(IcmsHttpUnifiedErrorException.class,
                                                               () -> createInstanceService.processInstanceRequest(
                                                                       DUMMY_CUSTOMER_ID,
                                                                       instanceRequestSchema,
                                                                       Map.of()));

        // Assert
        assertUnifiedErrorException(exception, NVCF_CUSTOMER_NO_ACCESS_TO_READY_CLUSTER, instanceRequestSchema, DUMMY_NON_BYOC_NCA_ID);

        // Assert
        verify(nvcaClusterRepository, times(1)).getAllClustersInAuthorizedAccount(DUMMY_NON_BYOC_NCA_ID);
        verify(nvcaClusterRepository, times(1)).getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);
        verifyNoMoreInteractions(clusterRepository);
        verify(instanceValidationService).validationForNvct(any());
        verifyNoInteractions(instanceRequestV2Repository);
    }


    @Test
    void processInstanceRequest_withInstanceTypeNotFound_success() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.AWS.toString();
        doNothing().when(instanceValidationService).validationForNvct(any());

        ClustersByAuthorizedAccountsEntity cluster = createClusterForByoc(DUMMY_OCI_NCA_ID, clusterGroupName, PLATFORM_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_OCI_NCA_ID)).thenReturn(List.of(cluster));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD)).thenReturn(List.of());

        ClusterEntity clusterEntity = toClusterEntity(cluster);
        clusterEntity.setClusterStatus(ClusterStatusEnum.READY);
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(clusterEntity));

        SpotInstanceRequestSchema instanceRequestSchema = getNonTargetingInstanceRequestSchema(
                "incorrect-instance-type",
                clusterGroupName, DUMMY_GPU,
                DUMMY_OCI_NCA_ID);

        //Act

        IcmsHttpUnifiedErrorException exception = assertThrows(IcmsHttpUnifiedErrorException.class,
                                                               () -> createInstanceService.processInstanceRequest(
                                                                       DUMMY_CUSTOMER_ID,
                                                                       instanceRequestSchema,
                                                                       Map.of()));

        // Assert
        assertUnifiedErrorException(exception, NVCF_CUSTOMER_NO_ACCESS_TO_READY_CLUSTER, instanceRequestSchema, DUMMY_OCI_NCA_ID);

        // Verify
        verify(nvcaClusterRepository, times(1)).getAllClustersInAuthorizedAccount(DUMMY_OCI_NCA_ID);
        verify(nvcaClusterRepository, times(1)).getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verifyNoMoreInteractions(clusterRepository);
        verify(instanceValidationService).validationForNvct(any());
        verifyNoInteractions(instanceRequestV2Repository);
    }


    @Test
    void processInstanceRequest_cloudProviderMappingNotFound_throwsException() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();

        ClustersByAuthorizedAccountsEntity cluster = createClusterForByoc(DUMMY_BYOC_NCA_ID, clusterGroupName, PLATFORM_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID, DUMMY_GPU_NAME, DUMMY_BYOC_INSTANCE_TYPE);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID)).thenReturn(List.of(cluster));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD)).thenReturn(List.of());

        ClusterEntity clusterEntity = toClusterEntity(cluster);
        clusterEntity.setClusterStatus(ClusterStatusEnum.READY);
        clusterEntity.setClusterProvider(null);
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(clusterEntity));
        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> createInstanceService.processInstanceRequest(
                                                                    DUMMY_CUSTOMER_ID,
                                                                    getNonTargetingInstanceRequestSchema(
                                                                            DUMMY_BYOC_INSTANCE_TYPE,
                                                                            clusterGroupName,
                                                                            DUMMY_GPU_NAME,
                                                                            DUMMY_BYOC_NCA_ID),
                                                                    new HashMap<>()
                                                            ));

        // Assert
        assertEquals("Failed to find cloudProvider for null clusterProvider",
                     exception.getBody().getDetail());

        // Verify
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID);
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);

        verifyNoInteractions(instanceRequestV2Repository);
    }

    @Test
    void processInstanceRequest_validByocRequest_returnsSuccess() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName,
                DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        ClustersByAuthorizedAccountsEntity cluster = createClusterForByoc(DUMMY_BYOC_NCA_ID, clusterGroupName, PLATFORM_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID, DUMMY_GPU_NAME, DUMMY_BYOC_INSTANCE_TYPE);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID)).thenReturn(List.of(cluster));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD)).thenReturn(List.of());

        ClusterEntity clusterEntity = toClusterEntity(cluster);
        clusterEntity.setClusterStatus(ClusterStatusEnum.READY);
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(clusterEntity));

        when(byocCreateService.processCreateRequest(anyString(), any(), any(), any())).thenReturn(new CreateSpotInstancesResponse(UUID.randomUUID()));

        CloudHealthEntity cloudHealth = createCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU_NAME, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 5, 5);
        Map<String, CloudHealthEntity> healthMap = new HashMap<>();
        healthMap.put(DUMMY_CLUSTER_ID, cloudHealth);
        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(healthMap);
        mockNonByocReservationProcessorWithNoOp();

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
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID);
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);
    }


    @Test
    void processInstanceRequest_withTaskCreationQueue_validByocRequest_returnsSuccess() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();

        SpotInstanceRequestSchema instanceRequest = getTargetingInstanceRequestSchemaForTask(
                DUMMY_BYOC_INSTANCE_TYPE,
                DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        when(instanceServiceHelper.isNatsEnabled()).thenReturn(false);
        when(instanceServiceHelper.isTaskClusterCreationQueuesAllowed(Boolean.TRUE)).thenReturn(true);
        doNothing().when(instanceValidationService).validationForNvct(instanceRequest);

        CloudHealthEntity cloudHealth = createCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU_NAME, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 0, 10);

        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(DUMMY_CLUSTER_ID, cloudHealth));

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


        when(byocCreateService.processCreateRequest(anyString(), any(), any(), any())).thenReturn(new CreateSpotInstancesResponse(UUID.randomUUID()));
        mockNonByocReservationProcessorWithNoOp();

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

        verify(clusterRepository, times(2)).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
    }

    @Test
    void processInstanceRequest_validByocRequestWithHelmUtilsEnabled_returnsSuccess() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName,
                DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);
        instanceRequest.setHelmChart("https://abc.def.zyx.com/zyx/charts/name-dummy-v12.3.4.tgz");

        ClustersByAuthorizedAccountsEntity cluster = createClusterForByoc(DUMMY_BYOC_NCA_ID, clusterGroupName, DUMMY_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID, DUMMY_GPU_NAME, DUMMY_BYOC_INSTANCE_TYPE);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID)).thenReturn(List.of(cluster));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD)).thenReturn(List.of());

        ClusterEntity clusterEntity = toClusterEntity(cluster);
        clusterEntity.setClusterStatus(ClusterStatusEnum.READY);
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(clusterEntity));

        when(byocCreateService.processCreateRequest(anyString(), any(), any(), any())).thenReturn(new CreateSpotInstancesResponse(UUID.randomUUID()));

        CloudHealthEntity cloudHealth = createCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU_NAME, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 5, 5);
        Map<String, CloudHealthEntity> healthMap = new HashMap<>();
        healthMap.put(DUMMY_CLUSTER_ID, cloudHealth);
        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(healthMap);
        mockNonByocReservationProcessorWithNoOp();

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

    }

    @Test
    void processInstanceRequest_validNvcaRequestAndClusterNotReady_throwsException() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName,
                DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        ClustersByAuthorizedAccountsEntity cluster = createClusterForByoc(DUMMY_BYOC_NCA_ID, clusterGroupName, DUMMY_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID, DUMMY_GPU_NAME, DUMMY_BYOC_INSTANCE_TYPE);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID)).thenReturn(List.of(cluster));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD)).thenReturn(List.of());


        // Mocking NVCA call for NOT_READY cluster
        ClusterEntity clusterEntity = toClusterEntity(cluster);
        clusterEntity.setClusterStatus(ClusterStatusEnum.NOT_READY);
        clusterEntity.setClusterProvider(ClusterProviderEnum.GDN);
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(clusterEntity));

        // Act
        IcmsHttpUnifiedErrorException exception = assertThrows(IcmsHttpUnifiedErrorException.class, () -> {
            createInstanceService.processInstanceRequest(
                    DUMMY_CUSTOMER_ID,
                    instanceRequest,
                    new HashMap<>());
        });

        // Assert
        assertUnifiedErrorException(exception, NVCF_CUSTOMER_NO_ACCESS_TO_READY_CLUSTER, instanceRequest, instanceRequest.getNcaId());

        // Verify

        verify(nvcaClusterRepository, times(1)).getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID);
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository, times(1)).getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);

        verify(instanceValidationService).validationForNvct(any());
        verifyNoInteractions(instanceRequestV2Repository);
    }

    @Test
    void processInstanceRequest_validClusterInfoNotFoundInByocAndNvca_throwsException() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName, DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        doNothing().when(instanceValidationService).validationForNvct(any());
        // Mocking NVCA call
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(new ArrayList<>());
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD))
                .thenReturn(new ArrayList<>());

        // Act
        IcmsHttpUnifiedErrorException exception = assertThrows(IcmsHttpUnifiedErrorException.class, () ->
                createInstanceService.processInstanceRequest(
                        DUMMY_CUSTOMER_ID,
                        instanceRequest,
                        new HashMap<>()
                ));

        // Assert
        assertUnifiedErrorException(exception, NVCF_CUSTOMER_NO_ACCESS_TO_READY_CLUSTER, instanceRequest, instanceRequest.getNcaId());

        // Verify
        verify(nvcaClusterRepository, times(1)).getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID);
        verify(nvcaClusterRepository, times(1)).getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);
        verify(clusterRepository, times(0)).getAllClustersInAGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(instanceValidationService).validationForNvct(any());
    }

    @Test
    void processInstanceRequest_NvcaRequestCreationQueueNotExists_throwsException() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getNonTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                clusterGroupName, DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

        ClustersByAuthorizedAccountsEntity dummyResp =
                getDummyClustersByAuthorizedAccountResp(clusterGroupName, DUMMY_CLUSTER_GROUP_ID,
                                                        DUMMY_CLUSTER_ID,
                                                        DUMMY_BYOC_NCA_ID, DUMMY_BYOC_INSTANCE_TYPE,
                                                        DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                        DUMMY_GPU_NAME, 8,
                                                        DUMMY_BYOC_AUTHORIZED_NCA_ID);
        dummyResp.setCreationQueues(new HashMap<>());
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(new ArrayList<>(Arrays.asList(dummyResp)));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD))
                .thenReturn(new ArrayList<>());

        // Act
        PreConditionFailedException exception =
                assertThrows(PreConditionFailedException.class, () ->
                        createInstanceService.processInstanceRequest(
                                DUMMY_CUSTOMER_ID,
                                instanceRequest,
                                new HashMap<>()
                        ));

        // Assert
        assertEquals("For dummy_gpu GPU GDN backend is under reconfiguration",
                     exception.getBody().getDetail());

        // Verify
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID);
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);
    }

    @Test
    void processInstanceRequest_validNvcaRequest_returnsSuccess_2() {

        // Prepare
        var clusterGroupName = ClusterProviderEnum.GDN.toString();
        var instanceRequest = getTargetingInstanceRequestSchema(
                DUMMY_BYOC_INSTANCE_TYPE,
                DUMMY_GPU_NAME,
                DUMMY_BYOC_NCA_ID);

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
                                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME  ,  // Match the cluster name in createInstanceRequestSchema
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
                                DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME  ,  // Match the cluster name in createInstanceRequestSchema
                                Set.of(DUMMY_BYOC_AUTHORIZED_NCA_ID), DUMMY_BYOC_NCA_ID,
                                Set.of(getDummyGpuV5(DUMMY_BYOC_INSTANCE_TYPE,
                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE, 8,
                                                     DUMMY_GPU_NAME)), "region_1",
                                Set.of("attribute_1", "attribute_3", "attribute_4"),
                                true, true)));

        when(clusterTargetingHelper.getAllClusterHealthInMap())
                .thenReturn(Map.of(DUMMY_CLUSTER_ID,
                                   getDummyCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU_NAME,
                                                             CloudHealthStatus.HEALTHY,
                                                             ResourceProvider.BYOC, 10, 0,
                                                             10)));

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(getDummyClusterEntity(clusterGroupName)));


        when(byocCreateService.processCreateRequest(anyString(), any(), any(), any())).thenReturn(new CreateSpotInstancesResponse(UUID.randomUUID()));
        mockNonByocReservationProcessorWithNoOp();

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

    }



    // Helper methods

    private SpotInstanceRequestSchema getNonTargetingInstanceRequestSchema(
            String instanceType,
            String backend,
            String gpu, String ncaId) {

        return SpotInstanceRequestSchema.builder()
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .instanceCount(1)
                .instanceType(instanceType)
                .backend(backend)
                .containerImage(DUMMY_CONTAINER_IMAGE)
                .environment(DUMMY_ENVIRONMENT_VALUE)
                .gpu(gpu)
                .ncaId(ncaId)
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .cacheSize(DUMMY_CACHE_SIZE)
                .ownerNcaId(OWNER_NCA_ID)
                .functionType(FunctionType.STREAMING)
                .telemetries(BASE64_ENCODED_TELEMETRIES)
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .build();
    }




    private ClusterGroupsByAuthorizedAccountsEntity getDummyClusterGroupResponse(
            String clusterGroupName, String ncaId, String instanceType, String instanceTypeValue,
            String gpuName, int gpuCount) {
        return ClusterGroupsByAuthorizedAccountsEntity.builder()
                .key(ClusterGroupsByAuthorizedAccountsKey.builder()
                             .clusterGroupId(DUMMY_CLUSTER_GROUP_ID)
                             .clusterGroupName(clusterGroupName)
                             .ncaIdKey(ncaId)
                             .build())
                .ncaId(ncaId)
                .authorizedNcaIds(Set.of("authorized_id_1"))
                .creationQueueUrl(DUMMY_BYOC_CREATION_QUEUE_URL)
                .gpus(Set.of(GpuUdt.builder()
                                     .name(gpuName)
                                     .instanceTypes(Set.of(InstanceTypeUdt.builder()
                                                                   .name(instanceType)
                                                                   .value(instanceTypeValue)
                                                                   .gpuCount(gpuCount)
                                                                   .build()))
                                     .build()))
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

    /**
     * Creates a ClusterEntity for testing purposes.
     * @param clusterId The cluster ID
     * @param clusterName The cluster name
     * @param clusterGroupId The cluster group ID
     * @param clusterGroupName The cluster group name
     * @param status The cluster status
     * @param provider The cluster provider
     * @return ClusterEntity configured for testing
     */
    private ClusterEntity createClusterEntity(String clusterId, String clusterName, String clusterGroupId, String clusterGroupName, ClusterStatusEnum status, ClusterProviderEnum provider) {
        return ClusterEntity.builder()
                .clusterId(clusterId)
                .clusterName(clusterName)
                .clusterGroupId(clusterGroupId)
                .clusterGroupName(clusterGroupName)
                .clusterStatus(status)
                .clusterProvider(provider)
                .allowClusterTargeting(true)
                .clusterKeyId(clusterId)
                .build();
    }

    private void assertUnifiedErrorException(@NotNull IcmsHttpUnifiedErrorException exception,
                                             @NotNull IcmsUnifiedError icmsUnifiedError,
                                             @NotNull SpotInstanceRequestSchema instanceRequestSchema,
                                             String ncaId) {
        assertEquals(icmsUnifiedError, exception.unifiedError());

        Assertions.assertNull(exception.unifiedErrorData().getRequestId());
        Assertions.assertNull(exception.unifiedErrorData().getInstanceId());

        assertEquals(instanceRequestSchema.getFunctionId().toString(), exception.unifiedErrorData().getFunctionId());
        assertEquals(instanceRequestSchema.getFunctionVersionId().toString(), exception.unifiedErrorData().getFunctionVersionId());
        Assertions.assertNull(exception.unifiedErrorData().getTaskId());

        assertEquals(instanceRequestSchema.getDeploymentId().toString(), exception.unifiedErrorData().getDeploymentId());
        assertEquals(instanceRequestSchema.getGpuSpecificationId().toString(), exception.unifiedErrorData().getGpuSpecificationId());
        assertEquals(ncaId, exception.unifiedErrorData().getNcaId());

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

    private SpotInstanceRequestSchema getTargetingInstanceRequestSchema(
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
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .cacheSize(DUMMY_CACHE_SIZE)
                .ownerNcaId(OWNER_NCA_ID)
                .functionType(FunctionType.STREAMING)
                .clusters(Set.of(DUMMY_BYOC_CLUSTER_NAME, "cluster_name_2"))
                .attributes(Set.of("attribute_1", "attribute_2"))
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .build();
    }

    private SpotInstanceRequestSchema createInstanceRequestSchema(
            String instanceType,
            String backend,
            String gpu,
            String ncaId,
            boolean isTargeting) {
        SpotInstanceRequestSchema request = new SpotInstanceRequestSchema();
        request.setAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES);
        request.setInstanceCount(1);
        request.setInstanceType(instanceType);
        request.setBackend(isTargeting ? null : backend);
        request.setContainerImage("dummy-container-image");
        request.setEnvironment("dummy-environment");
        request.setGpu(gpu);
        request.setNcaId(ncaId);
        request.setFunctionId(FUNCTION_ID);
        request.setFunctionVersionId(FUNCTION_VERSION_ID);
        request.setCacheSize(5000000000L);
        request.setOwnerNcaId(OWNER_NCA_ID);
        request.setFunctionType(FunctionType.STREAMING);
        request.setTelemetries("base64-encoded-telemetries");
        request.setDeploymentId(UUID.randomUUID());
        request.setGpuSpecificationId(UUID.randomUUID());

        if (isTargeting) {
            request.setClusters(Set.of(DUMMY_BYOC_CLUSTER_NAME  , "cluster_name_2"));
            request.setAttributes(Set.of("cAttribute_1", "cAttribute_2"));
        }

        return request;
    }

    /**
     * Sets up common mocks for targeted request testing
     */
    private void setupTargetedRequestMocks(String ncaId, String clusterId, String clusterName,
                                         String clusterGroupName, String gpuName, String instanceType) {
        // Mock cluster targeting helper to return a ready cluster
        ClusterByGroupIdAndIdEntity clusterEntity = getDummyClusterGroup(
            clusterGroupName,
            ClusterStatusEnum.READY,
            clusterId,
            clusterName,
            Set.of(ncaId),
            ncaId,
            Set.of(GpuV5Udt.builder()
                .name(gpuName)
                .instanceTypes(Set.of(InstanceTypeV5Udt.builder()
                    .name(instanceType)
                    .value(instanceType + "-value")
                    .gpuCount(1)
                    .build()))
                .build()),
            "region",
            Set.of(),
            true,
            true
        );
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ncaId))
            .thenReturn(Set.of(clusterEntity));
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD))
            .thenReturn(Set.of());

        // Create a healthy cloud health entity with available GPU capacity
        CloudHealthEntity cloudHealth = createCloudHealthEntity(clusterId, gpuName, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 5, 5);

        // Mock dependencies of ByocValidationService
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(false);

        // Mock cluster repository for ByocValidationService
        when(clusterRepository.getClusterInfoByClusterId(clusterId, false))
            .thenReturn(Optional.of(createClusterEntity(
                clusterId,
                clusterName,
                clusterGroupName,
                clusterGroupName,
                ClusterStatusEnum.READY,
                ClusterProviderEnum.AWS
            )));
    }


    /**
     * Sets up common mocks for ByocCreateService
     */
    private void setupByocCreateServiceMock(SpotInstanceRequestSchema instanceRequest,
                                          Map<String, Object> auditProps, 
                                          CreateSpotInstancesResponse expectedResponse) {
        when(byocCreateService.processCreateRequest(
                eq(TEST_CUSTOMER),
                any(Set.class),
                eq(instanceRequest),
                eq(auditProps)
            )).thenReturn(expectedResponse);
    }

    /**
     * Performs common assertions for successful request processing
     */
    private void assertSuccessfulRequestProcessing(CreateSpotInstancesResponse response, 
                                                 CreateSpotInstancesResponse expectedResponse,
                                                 SpotInstanceRequestSchema instanceRequest,
                                                 Map<String, Object> auditProps) {
        assertEquals(expectedResponse, response);
        verify(byocCreateService).processCreateRequest(
            eq(TEST_CUSTOMER),
            any(Set.class),
            eq(instanceRequest),
            eq(auditProps)
        );
    }

    /**
     * Performs common verification for targeted request processing
     */
    private void verifyTargetedRequestProcessing(String clusterId) {
        verify(clusterRepository).getClusterInfoByClusterId(clusterId, false);
    }


    /**
     * Sets up common mocks for Non BYOC request testing
     */
    private void setupNonByocRequestMocks(SpotInstanceRequestSchema instanceRequest,
                                    CreateSpotInstancesResponse expectedResponse,
                                    Map<String, Object> auditProps) {
        when(icmsConfigurationProperties.isGpuSupported(instanceRequest.getGpu())).thenReturn(true);
        when(icmsConfigurationProperties.isInstanceTypeSupported(instanceRequest.getInstanceType())).thenReturn(true);
        when(instanceLifecycleHelper.getGlobalCreationQueueUrlForNonByoc(instanceRequest.getGpu(), false)).thenReturn("dummy-queue-url");
        when(instanceLifecycleService.requestNonByocInstances(
                eq(TEST_CUSTOMER),
                any(),
                any(),
                any()
            )).thenReturn(expectedResponse);
    }

    /**
     * Performs common assertions for successful Non BYOC request processing
     */
    private void assertSuccessfulNonByocRequestProcessing(CreateSpotInstancesResponse response,
                                                     CreateSpotInstancesResponse expectedResponse,
                                                     SpotInstanceRequestSchema instanceRequest,
                                                     Map<String, Object> auditProps) {
        verify(icmsConfigurationProperties).isInstanceTypeSupported(instanceRequest.getInstanceType());
        verify(icmsConfigurationProperties).isGpuSupported(instanceRequest.getGpu());
        verify(instanceLifecycleHelper).getGlobalCreationQueueUrlForNonByoc(instanceRequest.getGpu(), false);
        assertEquals(expectedResponse, response);
        verify(instanceLifecycleService).requestNonByocInstances(
            eq(TEST_CUSTOMER),
            any(),
            any(),
            any()
        );
    }

    /**
     * Tests that createNonByocOrBartNonTargetedInstanceDestination correctly sets cluster group name on the destination.
     * This test verifies that:
     * 1. The method accepts a custom cluster group name through the ClusterGroupsByAuthorizedAccountsEntity
     * 2. The resulting RequestInstanceDestination has the correct cluster group name set
     * 3. Other properties like GPU name are correctly preserved
     * 4. The destination is not marked as reserved (since it's non-targeted)
     * 5. Validates the data mapping from cluster entity to destination object
     */
    @Test
    void createNonByocOrBartNonTargetedInstanceDestination_WithCustomClusterGroupName_CreatesDestinationWithCorrectName() {
        // Setup
        ClustersByAuthorizedAccountsEntity cluster = createClusterForByoc(DUMMY_BYOC_NCA_ID, CUSTOM_GROUP, CUSTOM_GROUP, DUMMY_CLUSTER_ID);

        DestinationClusterData destinationClusterData = new DestinationClusterData(cluster, Map.of(DUMMY_GPU, "queueUrl"));

        InstanceTypeUdt instanceType = createInstanceTypeUdt();
        InstanceTypeV5Udt instanceTypeV5Udt = NvcaConverter.toInstanceTypeV5(instanceType);

        // Execute
        RequestInstanceDestination result = destinationCreator.createDestination(
                destinationClusterData,
                instanceTypeV5Udt,
                CloudProvider.AWS,
                DUMMY_GPU,
                "queueUrl", 1);

        // Verify
        assertNotNull(result);
        assertEquals(CUSTOM_GROUP, result.getClusterGroupName());
        assertEquals(DUMMY_GPU, result.getGpuName());
        assertFalse(result.isReserved());
    }

    /**
     * Tests the targeted instance request flow when no destinations are found.
     * This scenario occurs when the request destination provider returns an empty set.
     * The test verifies that:
     * 1. A IcmsConflictException is thrown with appropriate error message
     * 2. The error message includes the GPU, instance type, and NCA ID
     */
    @Test
    void processTargetedInstanceRequest_WithEmptyDestinations_ShouldThrowSisConflictException() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
            "instance-type",
            null,
            DUMMY_GPU,
            TEST_NCA_ID,
            true
        );
        Map<String, Object> auditProps = new HashMap<>();

        // Mock empty destinations by making clusterTargetingHelper return no ready clusters
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(TEST_NCA_ID))
            .thenReturn(new HashSet<>());
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD))
            .thenReturn(new HashSet<>());

        // Act & Assert
        IcmsBadRequestException exception = assertThrows(IcmsBadRequestException.class, () ->
            createInstanceService.processTargetedInstanceRequest(TEST_CUSTOMER, instanceRequest, auditProps, new HashMap<>())
        );

        assertTrue(exception.getMessage().contains(DUMMY_GPU));
        assertTrue(exception.getMessage().contains("instance-type"));
    }

    /**
     * Tests the targeted instance request flow when destinations are filtered out due to capacity.
     * This scenario occurs when all destinations are removed during capacity filtering.
     * The test verifies that:
     * 1. A IcmsConflictException is thrown when no destinations remain after filtering
     * 2. The capacity filtering logic is executed
     */
    @Test
    void processTargetedInstanceRequest_WhenAllDestinationsFilteredByCapacity_ShouldThrowSisConflictException() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
            "instance-type",
            null,
            DUMMY_GPU,
            TEST_NCA_ID,
            true
        );
        Map<String, Object> auditProps = new HashMap<>();

        // Create a cluster that will be filtered out due to unhealthy status
        ClusterByGroupIdAndIdEntity clusterEntity = getDummyClusterGroup(
            "cluster-group",
            ClusterStatusEnum.READY,
            "cluster-id",
            "cluster-name",
            Set.of(TEST_NCA_ID),
            TEST_NCA_ID,
            Set.of(GpuV5Udt.builder()
                .name(DUMMY_GPU)
                .instanceTypes(Set.of(InstanceTypeV5Udt.builder()
                    .name("instance-type")
                    .value("instance-type-value")
                    .gpuCount(1)
                    .build()))
                .build()),
            "region",
            Set.of(),
            true,
            true
        );

        when(clusterRepository.getClusterInfoByClusterId("cluster-id", false))
                .thenReturn(Optional.of(createClusterEntity(
                        "cluster-id",
                        "cluster-name",
                        "cluster-group",
                        "cluster-group",
                        ClusterStatusEnum.READY,
                        ClusterProviderEnum.AWS
                )));
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(TEST_NCA_ID))
            .thenReturn(Set.of(clusterEntity));
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD))
            .thenReturn(Set.of());

        // Mock cloud health with unhealthy status to trigger filtering
        CloudHealthEntity cloudHealth = createCloudHealthEntity("cluster-id", DUMMY_GPU, 
            CloudHealthStatus.UNHEALTHY, ResourceProvider.BYOC, 10, 5, 5);
        Map<String, CloudHealthEntity> healthMap = new HashMap<>();
        healthMap.put("cluster-id", cloudHealth);

        // Act & Assert
        IcmsBadRequestException exception = assertThrows(IcmsBadRequestException.class, () ->
            createInstanceService.processTargetedInstanceRequest(TEST_CUSTOMER, instanceRequest, auditProps, healthMap)
        );

        assertTrue(exception.getMessage().contains(DUMMY_GPU));
        assertTrue(exception.getMessage().contains("instance-type"));
    }

    @Test
    void processTargetedInstanceRequest_restrictedGpuOnComputePlatform_forDisallowedNca_throwsBadRequest() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
                "instance-type", null, DUMMY_GPU, TEST_NCA_ID, true);
        Map<String, Object> auditProps = new HashMap<>();

        // Compute-platform (OCI) cluster carrying the requested GPU.
        ClusterByGroupIdAndIdEntity clusterEntity = getDummyClusterGroup(
                DUMMY_BYOC_CLUSTER_GROUP_NAME,
                ClusterStatusEnum.READY,
                "cluster-id",
                DUMMY_BYOC_CLUSTER_NAME,
                Set.of(TEST_NCA_ID),
                TEST_NCA_ID,
                Set.of(GpuV5Udt.builder()
                        .name(DUMMY_GPU)
                        .instanceTypes(Set.of(InstanceTypeV5Udt.builder()
                                .name("instance-type")
                                .value("instance-type-value")
                                .gpuCount(1)
                                .build()))
                        .build()),
                "region",
                Set.of(),
                true,
                true);
        clusterEntity.setClusterProvider(ClusterProviderEnum.OCI);

        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(TEST_NCA_ID))
                .thenReturn(Set.of(clusterEntity));
        when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ClusterRepository.WILDCARD))
                .thenReturn(Set.of());
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(false);
        when(clusterRepository.getClusterInfoByClusterId("cluster-id", false))
                .thenReturn(Optional.of(createClusterEntity(
                        "cluster-id",
                        DUMMY_BYOC_CLUSTER_NAME,
                        DUMMY_BYOC_CLUSTER_GROUP_NAME,
                        DUMMY_BYOC_CLUSTER_GROUP_NAME,
                        ClusterStatusEnum.READY,
                        ClusterProviderEnum.OCI)));

        // Restrict this GPU: the caller's NCA is NOT in the allowlist.
        when(icmsConfigurationProperties.isNcaAllowedForGpu(DUMMY_GPU, TEST_NCA_ID)).thenReturn(false);

        // Act & Assert
        IcmsBadRequestException exception = assertThrows(IcmsBadRequestException.class, () ->
                createInstanceService.processTargetedInstanceRequest(
                        TEST_CUSTOMER, instanceRequest, auditProps, new HashMap<>()));

        assertTrue(exception.getMessage().contains(DUMMY_GPU));
        assertTrue(exception.getMessage().contains("instance-type"));
    }

    private void mockNonByocReservationProcessorWithNoOp() {
        when(reservationProcessor.filterDestinationBasedOnReservation(
                Mockito.anySet(),
                any(SpotInstanceRequestSchema.class),
                Mockito.anyMap()
        )).thenAnswer(invocation -> {
            Set<RequestInstanceDestination> destinations = invocation.getArgument(0);
            return new HashSet<>(destinations);
        });
    }

    private CloudHealthEntity createCloudHealthEntity(String clusterId, String gpuName, CloudHealthStatus status,
                                                      ResourceProvider resourceProvider, int capacity, int allocated, int available) {
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        CloudHealthKey key = new CloudHealthKey(resourceProvider, clusterId);
        cloudHealth.setKey(key);
        cloudHealth.setStatus(status);

        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        GpuCapacity gpuCapacity = new GpuCapacity();
        gpuCapacity.setCapacity(capacity);
        gpuCapacity.setAllocated(allocated);
        gpuCapacity.setAvailable(available);
        gpuUsage.put(gpuName, gpuCapacity);

        cloudHealth.setGpuUsage(gpuUsage);
        return cloudHealth;
    }

    // filterDestinationBasedOnNonByocReservation TESTS //
    /**
     * Tests that ReservationProcessor can be mocked to throw an exception.
     * This test verifies that:
     * 1. The processor can be configured to throw exceptions
     * 2. The exception is properly propagated
     * 3. The service handles processor exceptions gracefully
     */
    @Test
    void processTargetedInstanceRequest_WhenNonByocReservationProcessorThrowsException_ShouldPropagateException() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
            "instance-type",
            ResourceProvider.BYOC.toString(),
            DUMMY_GPU,
            DUMMY_BYOC_NCA_ID,
            true
        );
        Map<String, Object> auditProps = new HashMap<>();

        // Setup common mocks for targeted request
        setupTargetedRequestMocks(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME,
                                DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_GPU, "instance-type");

        CloudHealthEntity cloudHealth = createCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 5, 5);
        Map<String, CloudHealthEntity> healthMap = new HashMap<>();
        healthMap.put(DUMMY_CLUSTER_ID, cloudHealth);
        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(healthMap);

        // Mock ReservationProcessor to throw an exception
        IcmsConflictException expectedException = new IcmsConflictException("Reserved capacity fully utilized");
        when(reservationProcessor.filterDestinationBasedOnReservation(
            any(Set.class),
            any(SpotInstanceRequestSchema.class),
            any(Map.class)
        )).thenThrow(expectedException);

        // Act & Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class, () ->
            createInstanceService.processInstanceRequest(TEST_CUSTOMER, instanceRequest, auditProps)
        );

        assertEquals("Reserved capacity fully utilized", exception.getBody().getDetail());
        verify(reservationProcessor).filterDestinationBasedOnReservation(
            any(Set.class),
            eq(instanceRequest),
            eq(healthMap)
        );
    }

    /**
     * Tests that ReservationProcessor can be mocked to return RESERVED destinations only.
     * This test verifies that:
     * 1. The processor can be configured to return RESERVED destinations
     * 2. The service handles RESERVED destinations correctly
     * 3. The modified destinations are used in subsequent processing
     */
    @Test
    void processTargetedInstanceRequest_ReservedDestinationsFiltered_ShouldUseModifiedDestinations() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
            "instance-type",
            ResourceProvider.BYOC.toString(),
            DUMMY_GPU,
            DUMMY_BYOC_NCA_ID,
            true
        );
        Map<String, Object> auditProps = new HashMap<>();
        CreateSpotInstancesResponse expectedResponse = new CreateSpotInstancesResponse();

        // Setup common mocks for targeted request
        setupTargetedRequestMocks(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME,
                                DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_GPU, "instance-type");

        // Setup ByocCreateService mock
        setupByocCreateServiceMock(instanceRequest, auditProps, expectedResponse);

        CloudHealthEntity cloudHealth = createCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 5, 5);
        Map<String, CloudHealthEntity> healthMap = new HashMap<>();
        healthMap.put(DUMMY_CLUSTER_ID, cloudHealth);
        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(healthMap);

        // Mock ReservationProcessor to return RESERVED destinations only
        when(reservationProcessor.filterDestinationBasedOnReservation(
            any(Set.class),
            any(SpotInstanceRequestSchema.class),
            any(Map.class)
        )).thenAnswer(invocation -> {
            Set<RequestInstanceDestination> originalDestinations = invocation.getArgument(0);

            // Create RESERVED destinations based on original destinations
            Set<RequestInstanceDestination> reservedDestinations = new HashSet<>();
            for (RequestInstanceDestination original : originalDestinations) {
                RequestInstanceDestination reservedDestination = new RequestInstanceDestination(original);
                reservedDestination.setReservationId(UUID.randomUUID());
                reservedDestination.setCapacityType(CapacityType.RESERVED);
                reservedDestination.setInstanceBatchCount(1);
                reservedDestination.setCreationQueueUrl("reserved-queue-" + original.getClusterId());
                reservedDestinations.add(reservedDestination);
            }
            return reservedDestinations;
        });

        // Act
        CreateSpotInstancesResponse response = createInstanceService.processInstanceRequest(
            TEST_CUSTOMER,
            instanceRequest,
            auditProps
        );

        // Assert
        assertEquals(expectedResponse, response);

        // Verify that ReservationProcessor was called
        verify(reservationProcessor).filterDestinationBasedOnReservation(
            any(Set.class),
            eq(instanceRequest),
            eq(healthMap)
        );
    }

    /**
     * Tests that ReservationProcessor can be mocked to return RESERVED_BACKUP destinations only.
     * This test verifies that:
     * 1. The processor can be configured to return RESERVED_BACKUP destinations
     * 2. The service handles RESERVED_BACKUP destinations correctly
     * 3. The modified destinations are used in subsequent processing
     */
    @Test
    void processTargetedInstanceRequest_WhenNonByocReservationProcessorReturnsReservedBackupDestinations_ShouldUseModifiedDestinations() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
            "instance-type",
            ResourceProvider.BYOC.toString(),
            DUMMY_GPU,
            DUMMY_BYOC_NCA_ID,
            true
        );
        Map<String, Object> auditProps = new HashMap<>();
        CreateSpotInstancesResponse expectedResponse = new CreateSpotInstancesResponse();

        // Setup common mocks for targeted request
        setupTargetedRequestMocks(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME,
                                DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_GPU, "instance-type");

        // Setup ByocCreateService mock
        setupByocCreateServiceMock(instanceRequest, auditProps, expectedResponse);

        CloudHealthEntity cloudHealth = createCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 5, 5);
        Map<String, CloudHealthEntity> healthMap = new HashMap<>();
        healthMap.put(DUMMY_CLUSTER_ID, cloudHealth);
        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(healthMap);

        // Mock ReservationProcessor to return RESERVED_BACKUP destinations only
        when(reservationProcessor.filterDestinationBasedOnReservation(
            any(Set.class),
            any(SpotInstanceRequestSchema.class),
            any(Map.class)
        )).thenAnswer(invocation -> {
            Set<RequestInstanceDestination> originalDestinations = invocation.getArgument(0);

            // Create RESERVED_BACKUP destinations based on original destinations
            Set<RequestInstanceDestination> reservedBackupDestinations = new HashSet<>();
            for (RequestInstanceDestination original : originalDestinations) {
                RequestInstanceDestination reservedBackupDestination = new RequestInstanceDestination(original);
                reservedBackupDestination.setReservationId(UUID.randomUUID());
                reservedBackupDestination.setCapacityType(CapacityType.RESERVED_BACKUP);
                reservedBackupDestination.setInstanceBatchCount(1);
                reservedBackupDestinations.add(reservedBackupDestination);
            }

            return reservedBackupDestinations;
        });

        // Act
        CreateSpotInstancesResponse response = createInstanceService.processInstanceRequest(
            TEST_CUSTOMER,
            instanceRequest,
            auditProps
        );

        // Assert
        assertEquals(expectedResponse, response);

        // Verify that ReservationProcessor was called
        verify(reservationProcessor).filterDestinationBasedOnReservation(
            any(Set.class),
            eq(instanceRequest),
            eq(healthMap)
        );
    }

    /**
     * Tests that ReservationProcessor can be mocked to return a mix of RESERVED and RESERVED_BACKUP destinations.
     * This test verifies that:
     * 1. The processor can be configured to return both RESERVED and RESERVED_BACKUP destinations
     * 2. The service handles mixed capacity types correctly
     * 3. The modified destinations are used in subsequent processing
     */
    @Test
    void processTargetedInstanceRequest_ReservedAndReservedBackupDestinationsFiltered_ShouldUseModifiedDestinations() {
        // Arrange
        SpotInstanceRequestSchema instanceRequest = createInstanceRequestSchema(
            "instance-type",
            ResourceProvider.BYOC.toString(),
            DUMMY_GPU,
            DUMMY_BYOC_NCA_ID,
            true
        );
        Map<String, Object> auditProps = new HashMap<>();
        CreateSpotInstancesResponse expectedResponse = new CreateSpotInstancesResponse();

        // Setup common mocks for targeted request
        setupTargetedRequestMocks(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID, DUMMY_BYOC_CLUSTER_NAME,
                                DUMMY_BYOC_CLUSTER_GROUP_NAME, DUMMY_GPU, "instance-type");

        // Setup ByocCreateService mock
        setupByocCreateServiceMock(instanceRequest, auditProps, expectedResponse);

        CloudHealthEntity cloudHealth = createCloudHealthEntity(DUMMY_CLUSTER_ID, DUMMY_GPU, CloudHealthStatus.HEALTHY, ResourceProvider.BYOC, 10, 5, 5);
        Map<String, CloudHealthEntity> healthMap = new HashMap<>();
        healthMap.put(DUMMY_CLUSTER_ID, cloudHealth);
        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(healthMap);

        // Mock ReservationProcessor to return mix of RESERVED and RESERVED_BACKUP destinations
        when(reservationProcessor.filterDestinationBasedOnReservation(
            any(Set.class),
            any(SpotInstanceRequestSchema.class),
            any(Map.class)
        )).thenAnswer(invocation -> {
            Set<RequestInstanceDestination> originalDestinations = invocation.getArgument(0);

            // Create mixed destinations: one RESERVED and one RESERVED_BACKUP
            Set<RequestInstanceDestination> mixedDestinations = new HashSet<>();
            
            // Create RESERVED destination
            RequestInstanceDestination reservedDestination = new RequestInstanceDestination(originalDestinations.iterator().next());
            reservedDestination.setReservationId(UUID.randomUUID());
            reservedDestination.setCapacityType(CapacityType.RESERVED);
            reservedDestination.setInstanceBatchCount(1);
            reservedDestination.setCreationQueueUrl("reserved-queue-" + reservedDestination.getClusterId());
            mixedDestinations.add(reservedDestination);
            
            // Create RESERVED_BACKUP destination
            RequestInstanceDestination reservedBackupDestination = new RequestInstanceDestination(originalDestinations.iterator().next());
            reservedBackupDestination.setReservationId(UUID.randomUUID());
            reservedBackupDestination.setCapacityType(CapacityType.RESERVED_BACKUP);
            reservedBackupDestination.setInstanceBatchCount(1);
            mixedDestinations.add(reservedBackupDestination);

            return mixedDestinations;
        });

        // Act
        CreateSpotInstancesResponse response = createInstanceService.processInstanceRequest(
            TEST_CUSTOMER,
            instanceRequest,
            auditProps
        );

        // Assert
        assertEquals(expectedResponse, response);

        // Verify that ReservationProcessor was called
        verify(reservationProcessor).filterDestinationBasedOnReservation(
            any(Set.class),
            eq(instanceRequest),
            eq(healthMap)
        );
    }
}
