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
package com.nvidia.icms.util;

import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toGpusV4;

import com.amazonaws.services.sqs.model.QueueAttributeName;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.cfg.DateTimeFeature;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.icms.factory.InstanceEntityFactory;
import com.nvidia.icms.inbound.rest.converters.NvcaRequestSchemaToUdtConverter;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.FunctionType;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.inbound.rest.model.byoc.AwsQueueAccessInfo;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.Gpu;
import com.nvidia.icms.inbound.rest.model.byoc.GpuRequestSchema;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceType;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeRequestSchema;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.inbound.rest.model.cluster.ClusterHeartbeatRequest;
import com.nvidia.icms.inbound.rest.model.cluster.ClusterHeartbeatRequest.ClusterCapacityStats;
import com.nvidia.icms.inbound.rest.model.cluster.ClusterRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationResponse;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterRegion;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterSource;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse.GpuResponseSchema;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse.InstanceTypeResponseSchema;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaAccessCreds;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatRequest.NvcaClusterCapacityStats;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationResponse;
import com.nvidia.icms.inbound.rest.model.nvca.SisConfig;
import com.nvidia.icms.inbound.rest.model.nvca.VaultConfig;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterHealthEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV4Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV3Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.service.internal.InternalInstanceServiceHelper;

import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.SneakyThrows;
import org.springframework.core.io.ClassPathResource;


public class TestUtil {

    public static final String DUMMY_CUSTOMER_1 = "dummy_customer_1";

    public static final String DUMMY_CLUSTER_ID = "cluster_id";

    public static final String DUMMY_LONG_CLUSTER_ID = "nvssa-cluster_id_111111111111111111111111111111111111";

    public static final String DUMMY_LONG_CLUSTER_ID_TRUNCATED = "111111111111111111111111111111111111";

    public static final String DUMMY_CLUSTER_GROUP_ID = "cluster_group";

    public static final String DUMMY_CUSTOMER_ID = "dummy_customer_id";

    public static final String DUMMY_REQUEST_ID = "dummy_request_id";

    public static final String DUMMY_MESSAGE_BATCH_ID = "dummy_message_batch_id";

    public static final String DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID =
            "dummy_open_request_having_instances_id";
    public static final String DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID =
            "dummy_open_request_without_having_instances_id";
    public static final String DUMMY_CANCELED_REQUEST_ID = "dummy_canceled_request_id";

    public static final String DUMMY_CLOSED_REQUEST_ID = "dummy_closed_request_id";

    public static final String SPOT_REQUEST_SCOPE = "spot-request";

    public static final String INSTANCE_REQUEST_SCOPE = "instance-request";

    public static final String SPOT_UPDATE_SCOPE = "spot-status-update";

    public static final String INSTANCE_STATUS_UPDATE_SCOPE = "instance-status-update";

    public static final String GPU_USAGE_SCOPE = "gpu-usage";

    public static final String ADMIN_INSTANCE_REQUEST_DESCRIBE_SCOPE = "admin:instance-request:describe";

    public static final String BYOC_REGISTRATION_SCOPE = "byoc_registration";

    public static final String CLUSTER_HEARTBEAT_SCOPE = "cluster_heartbeat";

    public static final String NVCA_CLUSTER_REGISTRATION_SCOPE = "nvca-cluster";

    public static final String NGC_CLUSTER_MANAGEMENT_SCOPE = "cluster-management";

    public static final String NGC_GPU_LISTING_SCOPE = "gpu_listing";
    public static final String NGC_REGION_LISTING_SCOPE = "regions_listing";
    public static final String NGC_CLUSTER_NAME_LISTING_SCOPE = "clusters_listing";
    public static final String ATTRIBUTES_LISTING_SCOPE = "attributes_listing";
    public static final String INSTANCE_TYPES_LISTING_SCOPE = "instance_types";

    public static final String NON_BYOC_CLUSTER_REGISTRATION_SCOPE = "gfn-clusters";

    public static final String CLUSTER_INSTANCES_SCOPE = "cluster-instances";

    public static final String DUMMY_NON_BYOC_INSTANCE_TYPE = "dummy_gpu_4.small";

    public static final String DUMMY_AZURE_INSTANCE_TYPE = "AZURE.GPU.AZURE_GPU_1x";

    public static final String DUMMY_AZURE_INSTANCE_TYPE_VALUE = "AZURE.GPU.AZURE_GPU";

    public static final String DUMMY_AZURE_GPU_NAME ="AZURE_GPU";

    public static final String DUMMY_OCI_INSTANCE_TYPE = "OCI.GPU.OCI_GPU_1x";

    public static final String DUMMY_OCI_INSTANCE_TYPE_VALUE = "OCI.GPU.OCI_GPU";

    public static final String DUMMY_OCI_GPU_NAME ="OCI_GPU";

    public static final String DUMMY_BYOC_INSTANCE_TYPE = "Standard_ND96amsr_A100_v4_1x";

    public static final String DUMMY_BYOC_INSTANCE_TYPE_VALUE = "Standard_ND96amsr_A100_v4";

    public static final String DUMMY_CONTAINER_IMAGE = "dummy_container_image";

    public static final String DUMMY_ENVIRONMENT_VALUE = "YT1iCmM9ZA==";

    public static final String DUMMY_GPU = "DUMMY_GPU";

    public static final String ACTION = "Action";

    public static final String SPOT_STATE_FILTER = "SpotStateFilter";

    public static final String SPOT_INSTANCE_REQUEST_ID = "SpotInstanceRequestId";
    public static final String DUMMY_TOKEN = "dummy_token";

    public static final String DUMMY_SCOPE = "dummy_scope";

    public static final String PUBLIC_SIS_ENDPOINT = "/v1/si";

    public static final String DUMMY_INSTANCE_ID = "dummy_instance_id";

    public static final String DUMMY_RUNNING_INSTANCE_ID = "dummy_running_instance_id";

    public static final String DUMMY_STARTING_INSTANCE_ID = "dummy_starting_instance_Id";

    public static final String DUMMY_TERMINATED_INSTANCE_ID = "dummy_terminated_instance_id";
    public static final String DUMMY_SHUTTING_DOWN_INSTANCE_ID = "dummy_shutting_down_instance_id";
    public static final String DUMMY_ZONE = "dummy_zone";

    public static final String INSTANCE_COUNT_KEY = "InstanceCount";

    public static final String LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY =
            "LaunchSpecification.InstanceType";

    public static final String LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY =
            "LaunchSpecification.ContainerImage";

    public static final String LAUNCH_SPECIFICATION_HELM_CHART_KEY =
            "LaunchSpecification.HelmChart";
    public static final String DUMMY_COLLECTOR_ID = "dummy_collector_id";

    public static final String DUMMY_BATCH_ID = "dummy_batch_id";

    public static final String DUMMY_ENV = "local";

    public static final String INSTANCE_TYPE_KEY = "instanceType";

    public static final String DUMMY_ENCODED_VALUE = "ZHVtbXktdmFsdWU=";

    public static final String DUMMY_ENCODED_INFERENCE_CONTAINER_ENV =
            "W3sia2V5IjoiSU5GRVJFTkNFX0NPTlRBSU5FUiIsInZhbHVlIjoiZHVtbXlfdXJsIn1d";

    public static final String DUMMY_ENCODED_EMPTY_LIST_INFERENCE_CONTAINER_ENV = "W10=";

    public static final String DUMMY_ENCODED_NULL_INFERENCE_CONTAINER_ENV = "bnVsbA==";

    public static final String DUMMY_BYOC_TERMINATION_QUEUE_URL = "termination_queue_url";

    public static final String DUMMY_BYOC_CREATION_QUEUE_URL = "creation_queue_url";


    public static final String DUMMY_BYOC_CLUSTER_NAME = "cluster-name";

    public static final String DUMMY_LONG_BYOC_CLUSTER_NAME = "cluster-name-aaaaaaaaaaaaaaaaaaaa";

    public static final String DUMMY_BYOC_CLUSTER_GROUP_NAME = "dummy_group_name";

    public static final String DUMMY_LONG_BYOC_CLUSTER_GROUP_NAME = "dummy_long_group_name_aaaaaaaaaaa";
    public static final String DUMMY_CLUSTER_DESCRIPTION = "dummy_description";

    public static final String DUMMY_LONG_CLUSTER_DESCRIPTION = "dummy_long_description_aaaaaaaaaa";

    public static final String DUMMY_NVCA_VERSION = "1.0.0";

    public static final String DUMMY_AUTH_CLIENT_ID = "dummy_client_id";

    public static final String DUMMY_REGION = "dummy_oci_region";

    public static final Set<String> DUMMY_ATTRIBUTES = Set.of("attr1", "attr2");

    public static final Set<String> DUMMY_CAPABILITIES = Set.of("cap1", "cap2");

    public static final Set<String> DUMMY_CUSTOM_ATTRIBUTES = Set.of("cattr1", "cattr2");

    public static final String DUMMY_NODE_NAME = "dummy_node_name";

    public static final String DUMMY_TERMINATION_CAUSE = "dummy_termination_cause";

    public static final String DUMMY_GPU_NAME = "dummy_gpu";

    public static final String DUMMY_CLOUD_PROVIDER = "dummy_cloud_provider";

    public static final String DUMMY_FAILED_CONTAINER_LOG = "dummy_failed_container_log";

    public static final String DUMMY_NON_BYOC_NCA_ID = "dummy_nonbyoc_nca_id";

    public static final String DUMMY_ERROR_SOURCE = "dummy_error_source";

    public static final String DUMMY_OCI_NCA_ID = "dummy_oci_nca_id";

    public static final String DUMMY_BYOC_NCA_ID = "dummy_byoc_nca_id";

    public static final String DUMMY_NON_BYOC_ZONE_NAME = "dummy_nonbyoc-zone";

    public static final String DUMMY_BYOC_AUTHORIZED_NCA_ID = "dummy_byoc_authorized_nca_id";

    public static final String DUMMY_ARTIFACT_URL = "dummy_artifact_url";

    public static final String DUMMY_CACHE_HANDLE = "dummy_cache_handle";

    public static final String DUMMY_MODEL_DATA_MOUNT_NAME = "dummy_model_data_mount_name";

    public static final String DUMMY_PVC_NAME = "dummy_pvc_name";

    public static final long DUMMY_CACHE_SIZE = 1073741824L;

    public static final String DUMMY_CREATION_QUEUE_URL = "dummy_creation_queue_url";

    public static final String DUMMY_CLUSTER_CREATION_QUEUE_URL = "dummy_cluster_creation_queue_url";

    public static final String DUMMY_SNS_TOPIC_NAME = "gdn-instance-termination-%s.fifo";

    public static final String DUMMY_SECRET_ASSERTION_TOKEN = "dummy_assertion_token_value";

    public static final String DUMMY_FUNCTION_ID = "dummy_function_id";

    public static final String DUMMY_FUNCTION_VERSION_ID = "dummy_function_version_id";

    // These dummy values are from local and test env where tracing is disabled, DON'T modify them
    public static final String DUMMY_TRACE_PARENT = "00-00000000000000000000000000000000-0000000000000000-00";
    public static final Map<String, String> DUMMY_TRACE_STATE = new HashMap<>();

    public static final String BASE64_ENCODED_TELEMETRIES = "ew0KICAgICJsb2dzVGVsZW1ldHJ5IjoNCiAgICAgICB7DQogICAgICAgInByb3RvY29sIjogImh0dHAiLA0KICAgICAgICJwcm92aWRlciI6ICJHUkFGQU5BIiwNCiAgICAgICAiZW5kcG9pbnQiOiAiZW5kcG9pbnQiLA0KICAgICAgICJuYW1lIjogInRlbGVtZXRyeS1mb28iDQogICAgfSwNCiAgICAibWV0cmljc1RlbGVtZXRyeSI6IHsNCiAgICAgICAicHJvdG9jb2wiOiAiaHR0cCIsDQogICAgICAgInByb3ZpZGVyIjogIkdSQUZBTkEiLA0KICAgICAgICJlbmRwb2ludCI6ICJlbmRwb2ludCIsDQogICAgICAgIm5hbWUiOiAidGVsZW1ldHJ5LWJheiINCiAgICB9LA0KICAgICJ0cmFjZXNUZWxlbWV0cnkiOiB7DQogICAgICAgInByb3RvY29sIjogImh0dHAiLA0KICAgICAgICJwcm92aWRlciI6ICJHUkFGQU5BIiwNCiAgICAgICAiZW5kcG9pbnQiOiAiZW5kcG9pbnQiLA0KICAgICAgICJuYW1lIjogInRlbGVtZXRyeS1iYXIiDQogICAgfQ0KICB9";

    public static final String DUMMY_FUNCTION_NAME = "dummy_function_name";

    public static final String DUMMY_TASK_NAME = "dummy_task_name";

    public static final String DUMMY_NCA_ID_ACCOUNT_NAME = "dummy_account_name";

    public static final String DUMMY_NV_IDP_ID = "dummy_idp_id";

    @SneakyThrows
    public static byte[] readFileAsBytes(String pathToFile) {
        try (var in = new ClassPathResource(pathToFile).getInputStream()) {
            return in.readAllBytes();
        }
    }

    @SneakyThrows
    public static String readFileAsString(String pathToFile) {
        return new String(readFileAsBytes(pathToFile));
    }

    public static ObjectMapper getObjectMapperInstance() {
        return JsonMapper.builder()
                .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                .build();
    }

    /**
     * Instance request entity will have only request-id information
     * If the request-state is open then it may/may not have instances associated with it
     * This depends up on InstanceV2Entity associated to given request-id
     */
    public static InstanceRequestV2Entity getDummyInstanceRequestEntity(
            SpotInstanceRequestState state,
            SpotRequestStatusCode instanceRequestStatusCode,
            String requestId,
            Instant dummyInstant,
            ResourceProvider resourceProvider) {
        // By default, we will set instance type of Non BYOC
        String instanceType = DUMMY_NON_BYOC_INSTANCE_TYPE;
        String backend = CloudProvider.OCI.toString();
        String ncaId = DUMMY_NON_BYOC_NCA_ID;
        if (resourceProvider == ResourceProvider.BYOC) {
            instanceType = DUMMY_BYOC_INSTANCE_TYPE;
            backend = CloudProvider.AZURE.toString();
            ncaId = DUMMY_BYOC_NCA_ID;
        }

        InstanceRequestV2Entity instanceRequestEntity = new InstanceRequestV2Entity();
        instanceRequestEntity.setAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES);
        instanceRequestEntity.setCustomer(DUMMY_CUSTOMER_ID);
        instanceRequestEntity.setRequestId(requestId);
        instanceRequestEntity.setCreateTimeuuid(TimeUtils.getUuidFromTimeStamp(dummyInstant));

        instanceRequestEntity.setRequest(
                GsonCompatMapper.toJson(getDummyClientRequestDataModel(instanceType, ncaId, requestId, backend)));
        instanceRequestEntity.setState(state);
        instanceRequestEntity.setStatusCode(instanceRequestStatusCode.toString());
        switch (instanceRequestStatusCode) {
            case PENDING_FULFILLMENT -> instanceRequestEntity.setStatusMessage(
                    "Instance request status set to pending-fulfillment");
            case PENDING_EVALUATION -> instanceRequestEntity.setStatusMessage(
                    "Instance request status set to pending-evaluation");
            case CANCELED_BEFORE_FULFILLMENT -> instanceRequestEntity.setStatusMessage(
                    "Instance request status set to canceled-before-fulfillment");
        }
        instanceRequestEntity.setStatusUpdateTime(dummyInstant.plusMillis(10000));
        instanceRequestEntity.setResourceProvider(resourceProvider);
        return instanceRequestEntity;
    }

    /**
     * @return InstanceV2Entity will have DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, as this
     * request is open in the instance request entity
     */
    public static InstanceV2Entity getDummyInstanceEntity(
            SpotInstanceInternalState instanceInternalState,
            SpotInstanceRequestState instanceRequestState,
            Instant dummyInstant,
            ResourceProvider resourceProvider) {

        InstanceV2Entity instanceEntity = InstanceV2Entity.getEmptyEntity();
        instanceEntity.setCreateTimeuuid(TimeUtils.getUuidFromTimeStamp(dummyInstant));

        if (instanceInternalState == SpotInstanceInternalState.RUNNING) {
            instanceEntity.setRequestStatusCode(SpotInstanceStatus.FULFILLED);
            instanceEntity.setRequestStatusMessage("Instance request status set to fulfilled");
            instanceEntity.setInstanceId(DUMMY_RUNNING_INSTANCE_ID);

        } else if (instanceInternalState == SpotInstanceInternalState.TERMINATED) {
            instanceEntity.setRequestStatusCode(
                    SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE);
            instanceEntity.setRequestStatusMessage("Instance request status set to closed");
            instanceEntity.setInstanceId(DUMMY_TERMINATED_INSTANCE_ID);
            instanceEntity.setErrorLog(DUMMY_FAILED_CONTAINER_LOG);

        } else if (instanceInternalState == SpotInstanceInternalState.SHUTTING_DOWN) {
            instanceEntity.setRequestStatusCode(SpotInstanceStatus.FULFILLED);
            instanceEntity.setRequestStatusMessage("Instance request status set to fulfilled");
            instanceEntity.setInstanceId(DUMMY_SHUTTING_DOWN_INSTANCE_ID);

        } else if (instanceInternalState == SpotInstanceInternalState.STARTING) {
            instanceEntity.setRequestStatusCode(SpotInstanceStatus.FULFILLED);
            instanceEntity.setRequestStatusMessage("Instance request status set to fulfilled");
            instanceEntity.setInstanceId(DUMMY_STARTING_INSTANCE_ID);
        }

        instanceEntity.setInstanceStateCode(
                SpotInstanceInternalState.getStateCode(instanceInternalState));
        instanceEntity.setInstanceStateName(instanceInternalState);
        instanceEntity.setRequestState(instanceRequestState);

        instanceEntity.setInstanceUpdateTime(dummyInstant);
        instanceEntity.setCustomer(DUMMY_CUSTOMER_ID);
        instanceEntity.setRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        instanceEntity.setZone(DUMMY_ZONE);
        instanceEntity.setRequestStatusUpdateTime(dummyInstant.plusMillis(10000));
        instanceEntity.setResourceProvider(resourceProvider);

        return instanceEntity;
    }

    public static SpotInstanceStatusUpdateRequest getInstanceUpdateRequestForActiveInstance(
            SpotInstanceInternalState state) {

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                new SpotInstanceStatusUpdateRequest();
        instanceStatusUpdateRequest.setRequestState(SpotInstanceRequestState.ACTIVE);
        instanceStatusUpdateRequest.setInstanceState(state);
        instanceStatusUpdateRequest.setAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES);
        instanceStatusUpdateRequest.setStatus(SpotInstanceStatus.FULFILLED);
        instanceStatusUpdateRequest.setImageId(DUMMY_CONTAINER_IMAGE);
        instanceStatusUpdateRequest.setPlacement(
                new SpotInstanceStatusUpdateRequest.InstancePlacement(DUMMY_ZONE));

        return instanceStatusUpdateRequest;
    }

    public static SpotInstanceStatusUpdateRequest getInstanceUpdateRequestForActiveInstanceWithReservationId(
            SpotInstanceInternalState state, UUID reservationId, CapacityType capacityType) {
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest;
        if (state == SpotInstanceInternalState.RUNNING || state == SpotInstanceInternalState.STARTING) {
            instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(state);
        } else {
            instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();
        }

        instanceStatusUpdateRequest.setReservationId(reservationId);
        instanceStatusUpdateRequest.setCapacityType(capacityType);

        return instanceStatusUpdateRequest;
    }

    public static SpotInstanceStatusUpdateRequest getInstanceUpdateRequestForTerminatedState() {

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                new SpotInstanceStatusUpdateRequest();
        instanceStatusUpdateRequest.setRequestState(SpotInstanceRequestState.CLOSED);
        instanceStatusUpdateRequest.setInstanceState(SpotInstanceInternalState.TERMINATED);
        instanceStatusUpdateRequest.setAction(SpotInstanceRequestAction.TERMINATE_INSTANCES);
        instanceStatusUpdateRequest.setStatus(
                SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE);
        instanceStatusUpdateRequest.setImageId(DUMMY_CONTAINER_IMAGE);
        instanceStatusUpdateRequest.setPlacement(
                new SpotInstanceStatusUpdateRequest.InstancePlacement(DUMMY_ZONE));
        instanceStatusUpdateRequest.setTerminationCause(DUMMY_TERMINATION_CAUSE);
        instanceStatusUpdateRequest.setHealthInfo(
                SpotInstanceStatusUpdateRequest.SpotInstanceHeathInfo.builder()
                        .errorLog(DUMMY_FAILED_CONTAINER_LOG)
                        .build());

        return instanceStatusUpdateRequest;
    }

    public static SpotInstanceStatusUpdateRequest getInstanceUpdateRequestForTerminatedStateWithReservationId(
            UUID reservationId, CapacityType capacityType) {

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForTerminatedState();
        instanceStatusUpdateRequest.setReservationId(reservationId);

        return instanceStatusUpdateRequest;
    }

    public static InstanceV2Entity getTerminatedInstance(
            InstanceV2Entity instanceEntity,
            String errorLog) {
        InstanceV2Entity terminatedInstanceEntity = CopyUtil.deepCopy(instanceEntity);
        terminatedInstanceEntity.setInstanceStateCode(SpotInstanceInternalState.getStateCode(
                SpotInstanceInternalState.TERMINATED));
        terminatedInstanceEntity.setInstanceStateName(SpotInstanceInternalState.TERMINATED);
        terminatedInstanceEntity.setRequestStatusCode(SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE);
        terminatedInstanceEntity.setRequestStatusMessage(String.format("Instance status updated to %s",
                                                                 SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE));
        terminatedInstanceEntity.setRequestStatusUpdateTime(Instant.now());
        terminatedInstanceEntity.setInstanceUpdateTime(Instant.now());
        terminatedInstanceEntity.setRequestState(SpotInstanceRequestState.CLOSED);
        terminatedInstanceEntity.setErrorLog(errorLog);
        return terminatedInstanceEntity;
    }

    public static InstanceV2Entity getInstanceEntityForRunningInstance() {
        InstanceV2Entity instanceEntity = InstanceEntityFactory.createDefaultInstanceV2(DUMMY_INSTANCE_ID,
                                                                                                         DUMMY_REQUEST_ID,
                                                                                                         Instant.now().truncatedTo(ChronoUnit.DAYS),
                                                                                                         DUMMY_CUSTOMER_1,
                                                                                                         null);
        instanceEntity.setInstanceUpdateTime(Instant.now().truncatedTo(ChronoUnit.MILLIS));
        instanceEntity.setZone(DUMMY_ZONE);
        instanceEntity.setInstanceStateCode(16);
        instanceEntity.setInstanceStateName(SpotInstanceInternalState.RUNNING);
        instanceEntity.setRequestState(SpotInstanceRequestState.ACTIVE);
        instanceEntity.setRequestStatusCode(SpotInstanceStatus.FULFILLED);
        instanceEntity.setRequestStatusMessage("message");
        instanceEntity.setImageId(DUMMY_CONTAINER_IMAGE);
        instanceEntity.setResourceProvider(ResourceProvider.BYOC);
        return instanceEntity;
    }


    public static String getEnvValueForK8s(String inferenceContainerEnv, String secretAssertionToken) {
        String input = """
                AWS_ACCESS_KEY_ID=ZHVtbXktdmFsdWU=
                AWS_SECRET_ACCESS_KEY=ZHVtbXktdmFsdWU=
                AWS_DEFAULT_REGION=dummy-region
                AWS_REQUEST_QUEUE_URL=https://dummy-url
                RENEWABLE_AWS_ACCESS_KEY_ID==ZHVtbXktdmFsdWU=
                RENEWABLE_AWS_SECRET_ACCESS_KEY==ZHVtbXktdmFsdWU=
                RENEWABLE_AWS_SESSION_TOKEN==ZHVtbXktdmFsdWU=
                RENEWABLE_AWS_ACCESS_KEY_EXPIRATION==ZHVtbXktdmFsdWU=
                NVCF_FQDN=https://dummy-url
                MODELS=ZHVtbXktdmFsdWU=
                INFERENCE_CONTAINER_CREDENTIAL=ZHVtbXktdmFsdWU=
                INFERENCE_CONTAINER=https://dummy-url
                INFERENCE_CONTAINER_ARGS=ZHVtbXktdmFsdWU=
                TRACING_ACCESS_TOKEN=ZHVtbXktdmFsdWU=
                INIT_CONTAINER=https://dummy-url
                UTILS_CONTAINER=https://dummy-url
                FUNCTION_ID=dummy_function_id
                FUNCTION_VERSION_ID=dummy_version_id
                ESS_AGENT_CONTAINER=stg.nvcr.io/nv-cf/nvcf-core/nvcf_worker_init:0.7.0
                SIDECAR_CREDENTIAL=ZHVtbXktdmFsdWU=
                NICLLS_CONTAINER=dummy-niclls"""
                + "\n" + "INFERENCE_CONTAINER_ENV=" + inferenceContainerEnv
                + "\n" + "SECRETS_ASSERTION_TOKEN=" + secretAssertionToken;

        return Base64.getEncoder().encodeToString(input.getBytes());
    }

    public static ClusterEntity getDummyClusterEntity() {
        return ClusterEntity.builder()
                .clusterName(DUMMY_BYOC_CLUSTER_NAME)
                .clusterId("id")
                .ncaId("ncaId")
                .terminationQueueUrl(DUMMY_BYOC_TERMINATION_QUEUE_URL)
                .terminationQueueType("queue_type")
                .clusterDescription("cluster_description")
                .clusterProvider(ClusterProviderEnum.GDN)
                .clusterStatus(ClusterStatusEnum.READY)
                .clusterGroupName("group_name")
                .clusterGroupId("group_id")
                .creationQueueUrl(DUMMY_CREATION_QUEUE_URL)
                .creationQueueType("queue_type")
                .k8sVersion("k8sVersion")
                .registrationTime(Instant.now())
                .gpus(buildGpuUdts())
                .authorizedNcaIds(Set.of("ncaId1", "ncaId2"))
                .requestDump("request")
                .region(ClusterRegion.US_EAST_1.toString())
                .capabilities(Set.of("DynamicGPUDiscovery"))
                .attributes(Set.of("KataRuntimeIsolation"))
                .nvcaVersion("1.0.0")
                .authClientId("dummy_auth_client_id")
                .nvcaLastConnected(Instant.now())
                .gpusV4(Set.of(getDummyGpuV4(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_INSTANCE_TYPE_VALUE, 1,
                                      "AZURE")))
                .gpusV5(NvcaConverter.getGpusV5(Collections.emptySet(), Set.of(
                        getDummyGpuV4(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_INSTANCE_TYPE_VALUE, 1,
                                      "AZURE"))))
                .build();
    }

    public static ClusterEntity getDummyByocClusterEntity() {
        return ClusterEntity.builder()
                .clusterName(DUMMY_BYOC_CLUSTER_NAME)
                .clusterId("id")
                .ncaId("ncaId")
                .terminationQueueUrl(DUMMY_BYOC_TERMINATION_QUEUE_URL)
                .terminationQueueType("queue_type")
                .clusterDescription("cluster_description")
                .clusterProvider(ClusterProviderEnum.GDN)
                .clusterStatus(ClusterStatusEnum.READY)
                .clusterGroupName("AZURE")
                .clusterGroupId("group_id")
                .creationQueueUrl(DUMMY_CREATION_QUEUE_URL)
                .creationQueueType("queue_type")
                .k8sVersion("k8sVersion")
                .registrationTime(Instant.now())
                .gpus(buildGpuUdts())
                .authorizedNcaIds(Set.of("ncaId1", "ncaId2"))
                .requestDump("request")
                .build();
    }

    public static Set<Gpu> buildGpus() {
        Gpu gpu = Gpu.builder().name("gpu").instanceTypes(
                Set.of(InstanceType.builder().name("gpu1").gpuMemory("16G").gpuCount(8)
                               .value("xlarge")
                               .systemMemory("16G").cpuCores(8).description("desc").isDefault(true)
                               .build(),
                       InstanceType.builder().name("gpu2").gpuMemory("16G").gpuCount(8)
                               .value("ylarge")
                               .systemMemory("16G").cpuCores(8).description("desc")
                               .isDefault(false)
                               .build())).build();
        return Set.of(gpu);
    }

    public static Set<GpuUdt> buildGpuUdts() {
        return NvcaRequestSchemaToUdtConverter.toGpuUdts(buildGpus());
    }


    public static Set<GpuUdt> buildUpdatedGpusForCluster() {
        GpuUdt gpu = GpuUdt.builder().name("gpu3").instanceTypes(
                Set.of(InstanceTypeUdt.builder().name("gpu5").gpuMemory("16G").gpuCount(8)
                               .value("xlarger")
                               .systemMemory("16G").cpuCores(8).description("desc").isDefault(true)
                               .build(),
                       InstanceTypeUdt.builder().name("gpu6").gpuMemory("16G").gpuCount(8)
                               .value("ylarger")
                               .systemMemory("16G").cpuCores(8).description("desc")
                               .isDefault(false)
                               .build())).build();
        return Set.of(gpu);
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

    public static ClusterByGroupIdAndIdEntity toClusterByGroupIdAndIdEntity(ClusterEntity entity) {
        return ClusterByGroupIdAndIdEntity.builder()
                .key(ClusterByGroupIdAndIdKey.builder()
                             .clusterId(entity.getClusterId())
                             .clusterGroupId(entity.getClusterGroupId())
                             .build())
                .clusterName(entity.getClusterName())

                .ncaId(entity.getNcaId())
                .terminationQueueUrl(entity.getTerminationQueueUrl())
                .terminationQueueType(entity.getTerminationQueueType())
                .clusterDescription(entity.getClusterDescription())
                .clusterProvider(entity.getClusterProvider())
                .clusterStatus(entity.getClusterStatus())
                .k8sVersion(entity.getK8sVersion())
                .clusterGroupName(entity.getClusterGroupName())
                .gpusV4(entity.getGpusV4())
                .capabilities(entity.getCapabilities())
                .attributes(entity.getAttributes())
                .nvcaVersion(entity.getNvcaVersion())
                .authClientId(entity.getAuthClientId())
                .creationQueues(entity.getCreationQueues())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .requestDump(entity.getRequestDump())
                .build();
    }

    public static ClusterByGroupIdAndIdEntity getClusterByGroupIdAndIdEntity(String clusterId,String clusterName, String clusterGroupId,
                                                                            String clusterGroupName, String ncaId, Set<GpuV5Udt> gpuV5Set) {
        ClusterByGroupIdAndIdEntity entity = ClusterByGroupIdAndIdEntity.builder()
                .key(ClusterByGroupIdAndIdKey.builder()
                             .clusterId(clusterId)
                             .clusterGroupId(clusterGroupId)
                             .build())
                .clusterName(clusterName)
                .ncaId(ncaId)
                .clusterDescription(DUMMY_CLUSTER_DESCRIPTION)
                .clusterProvider(ClusterProviderEnum.GDN)
                .clusterStatus(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .clusterGroupName(clusterGroupName)
                .gpusV5(gpuV5Set)
                .capabilities(DUMMY_CAPABILITIES)
                .attributes(DUMMY_ATTRIBUTES)
                .nvcaVersion(DUMMY_NVCA_VERSION)
                .authorizedNcaIds(Set.of("*"))
                .allowClusterTargeting(Boolean.TRUE)
                .build();

        if (gpuV5Set != null && !gpuV5Set.isEmpty()) {
            entity.setGpusV5(gpuV5Set);
            entity.setGpusV4(toGpusV4(gpuV5Set));

            Map<String, CreationQueueUdt> creationQueueMap = new HashMap<>();
            Map<String, CreationQueueUdt> clusterCreationQueueMap = new HashMap<>();
            Map<String, CreationQueueUdt> taskClusterCreationQueueMap = new HashMap<>();
            for (GpuV5Udt gpu : gpuV5Set) {
                    creationQueueMap.put(gpu.getName(),
                                         getCreationQueue(gpu.getName(), DUMMY_CLUSTER_GROUP_ID));
                    clusterCreationQueueMap.put(gpu.getName(),
                                                getCreationQueue(gpu.getName(), clusterId));
                    taskClusterCreationQueueMap.put(gpu.getName(),
                                                    getCreationQueueForTasks(gpu.getName(),
                                                                             clusterId));
            }
            entity.setCreationQueues(creationQueueMap);
            entity.setClusterCreationQueues(clusterCreationQueueMap);
            entity.setClusterCreationQueuesForTasks(taskClusterCreationQueueMap);
            entity.setTerminationQueueUrl(
                    String.format("q_gdn_spot_byoc_%s.fifo", clusterId));
            entity.setTerminationQueueType(QueueAttributeName.FifoQueue.toString());
        }

        return entity;
    }

    public static ClusterHealthEntity getDummyClusterHealthEntity(String clusterId) {
        return ClusterHealthEntity.builder()
                .healthUpdatedTs(Instant.now())
                .clusterId(clusterId)
                .build();
    }

    public static SpotInstanceRequestSchema getSpotInstanceRequest(
            SpotInstanceRequestAction action,
            String instanceType,
            int instanceCount,
            String containerImage,
            String environment,
            String zone,
            String gpu, String backend) {
        return SpotInstanceRequestSchema.builder()
                .containerImage(containerImage)
                .instanceType(instanceType)
                .gpu(gpu)
                .backend(backend)
                .action(action)
                .availabilityZone(zone)
                .instanceCount(instanceCount)
                .environment(environment)
                .functionType(FunctionType.STREAMING)
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .ncaId(UUID.randomUUID().toString())
                .build();
    }

    public static SpotInstanceStatusUpdateRequest.InstancePlacement getDummyInstancePlacement() {
        return new SpotInstanceStatusUpdateRequest.InstancePlacement(DUMMY_ZONE);
    }

    public static InternalInstanceServiceHelper.InstancePlacementValidationResponse getDummyInstancePlacementValidationResponse(
            SpotInstanceStatusUpdateRequest.InstancePlacement instancePlacement,
            CloudProvider cloudProvider, ResourceProvider resourceProvider) {
        return InternalInstanceServiceHelper.InstancePlacementValidationResponse.builder()
                .instancePlacement(instancePlacement)
                .cloudProvider(cloudProvider)
                .resourceProvider(resourceProvider)
                .build();
    }

    public static NvcaRegistrationResponse getDummyNvcaClusterRegistrationResponse() {

        AwsQueueAccessInfo awsQueueAccessInfo = getDummyAwsQueueAccessInfo();
        Map<String, AwsQueueAccessInfo> creationQueue =
                Map.of("A100", awsQueueAccessInfo, "H100", awsQueueAccessInfo);

        NvcaAccessCreds nvcaAccessCreds = NvcaAccessCreds.builder()
                .creationQueue(creationQueue)
                .terminationQueue(awsQueueAccessInfo)
                .build();
        return NvcaRegistrationResponse.builder()
                .clusterGroupId("dummy_cluster_group_id")
                .clusterId("dummy_cluster_Id")
                .credentials(nvcaAccessCreds)
                .build();
    }

    public static AwsQueueAccessInfo getDummyAwsQueueAccessInfo() {
        return AwsQueueAccessInfo.builder()
                .accessKeyId("dummy_access_key_id")
                .accessKeyId("dummy_access_key")
                .url("dummy_url")
                .queueType("SQS")
                .secretAccessKey("dummy_secret_access_key")
                .sessionToken("dummy_session_token")
                .expiresAt(Instant.now().plus(60, ChronoUnit.MINUTES))
                .build();
    }

    public static NvcaRegistrationRequest getDummyNvcaRegistrationRequest() {

        InstanceTypeRequestSchema instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .name("dummy_instance_type")
                .value("dummy_value")
                .gpuMemory("dummy_gpu_memory")
                .systemMemory("dummy_system_memory")
                .isDefault(true)
                .description("dummy_description")
                .cpuCores(1)
                .driverVersion("dummy_driver_detail")
                .storage("80G")
                .cpuArch("AMD66")
                .os("linux")
                .build();

        GpuRequestSchema gpuRequestSchema = GpuRequestSchema.builder()
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .capacity(1)
                .name(DUMMY_GPU_NAME)
                .build();

        return NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.27.3")
                .nvcaVersion("2.0.1")
                .gpus(Set.of(gpuRequestSchema))
                .build();
    }

    public static ClusterCreationRequest getDummyClusterCreationRequest() {
        return ClusterCreationRequest.builder()
                .clusterName(DUMMY_BYOC_CLUSTER_NAME)
                .clusterGroupName(DUMMY_BYOC_CLUSTER_GROUP_NAME)
                .clusterDescription(DUMMY_CLUSTER_DESCRIPTION)
                .ncaId(DUMMY_BYOC_NCA_ID)
                .authorizedNCAIds(new HashSet<>())
                .capabilities(new HashSet<>())
                .attributes(new HashSet<>())
                .gpus(Set.of(getDummyGpuRequestSchema()))
                .nvcaVersion(DUMMY_NVCA_VERSION)
                .oAuthClientId(DUMMY_AUTH_CLIENT_ID)
                .region("US-EAST-1")
                .cloudProvider(ClusterProviderEnum.GDN)
                .build();
    }

    public static ClusterRegistrationRequest getZoneRegistrationRequest() {
        return ClusterRegistrationRequest.builder()
                .attributes(new HashSet<>())
                .gpus(Set.of(getDummyGpuRequestSchema()))
                .region("US-EAST-1")
                .status(ClusterStatusEnum.READY)
                .build();
    }

    public static NvcaClusterHeartbeatRequest getDummyNvcaClusterHeartbeatRequest() {
        return NvcaClusterHeartbeatRequest.builder()
                .status(CloudHealthStatus.HEALTHY)
                .nvcaAgentVersion("3.0.0")
                .upgradeStatus("SUCCESS")
                .gpuUsage(Map.of("gpu1",
                                 NvcaClusterCapacityStats.builder().capacity(10).allocated(4)
                                         .available(6).build(),
                                 "gpu2",
                                 NvcaClusterCapacityStats.builder().capacity(20).allocated(11)
                                         .available(9).build()))
                .build();
    }

    public static ClusterHeartbeatRequest getDummyClusterHeartbeatRequest() {
        return ClusterHeartbeatRequest.builder()
                .status(CloudHealthStatus.HEALTHY)
                .gpuUsage(Map.of("gpu1",
                                 ClusterCapacityStats.builder().allocated(4)
                                         .available(6).build(),
                                 "gpu2",
                                 ClusterCapacityStats.builder().allocated(11)
                                         .available(9).build()))
                .build();
    }

    public static ClusterCreationResponse getDummyClusterCreationResponse() {
        return ClusterCreationResponse.builder()
                .clusterGroupId("dummy_cluster_group_id")
                .clusterId("dummy_cluster_Id")
                .build();
    }

    public static InstanceTypeUdt getDummyInstanceType() {
        return InstanceTypeUdt.builder()
                .name("dummy_instance_type")
                .value("dummy_value")
                .gpuMemory("dummy_gpu_memory")
                .systemMemory("dummy_system_memory")
                .isDefault(true)
                .description("dummy_description")
                .cpuCores(1)
                .build();
    }

    public static InstanceTypeV3Udt getDummyInstanceTypeV2() {
        return InstanceTypeV3Udt.builder()
                .name("dummy_instance_type")
                .value("dummy_value")
                .gpuMemory("dummy_gpu_memory")
                .systemMemory("dummy_system_memory")
                .isDefault(true)
                .description("dummy_description")
                .cpuCores(1)
                .driverVersion("dummy_driver_detail")
                .storage("80G")
                .cpuArch("AMD66")
                .os("linux")
                .build();
    }

    public static GpuRequestSchema getDummyGpuRequestSchema() {
        return GpuRequestSchema.builder()
                .instanceTypes(Set.of(getDummyInstanceTypeRequestSchema()))
                .capacity(1)
                .name(DUMMY_GPU_NAME)
                .build();
    }

    public static InstanceTypeRequestSchema getDummyInstanceTypeRequestSchema() {
        return InstanceTypeRequestSchema.builder()
                .name("dummy_instance_type")
                .value("dummy_value")
                .gpuMemory("dummy_gpu_memory")
                .systemMemory("dummy_system_memory")
                .isDefault(true)
                .description("dummy_description")
                .cpuCores(1)
                .driverVersion("dummy_driver_detail")
                .storage("80G")
                .cpuArch("AMD66")
                .os("linux")
                .build();
    }

    public static GetClusterResponse getDummyGetClusterResponse(String ncaId, String clusterId) {

        InstanceTypeResponseSchema instanceTypeResponseSchema = InstanceTypeResponseSchema.builder()
                .cpuCores(4)
                .systemMemory("16G")
                .gpuMemory("14G")
                .gpuCount(8)
                .name("BM.GPU.A100-v2.8_8x")
                .description("Eight A100 GPU")
                .isDefault(true)
                .value("BM.GPU.A100-v2.8")
                .cpuArch("AMD64")
                .driverVersion("Linux 64 bit v.353.145.36")
                .storage("80G")
                .build();

        GpuResponseSchema gpuRequestSchema = GpuResponseSchema.builder()
                .capacity(2)
                .instanceTypes(Set.of(instanceTypeResponseSchema))
                .build();

        GpuCapacity gpuCapacity = GpuCapacity.builder()
                .allocated(1)
                .available(1)
                .build();

        return GetClusterResponse.builder()
                .clusterName("dummy_cluster_name")
                .clusterGroupName("dummy_cluster_group_name")
                .clusterDescription("dummy_cluster_description")
                .ncaId(ncaId)
                .authorizedNCAIds(Set.of("nca_id1"))
                .cloudProvider(ClusterProviderEnum.GDN)
                .capabilities(Set.of("CachingSupport"))
                .attributes(Set.of("SOC2Compliant"))
                .gpus(Set.of(gpuRequestSchema))
                .nvcaVersion("2.1.0")
                .ssaClientId("dummy_auth_client_id")
                .oAuthClientId("dummy_auth_client_id")
                .clusterId(clusterId)
                .clusterSource(ClusterSource.NGC_MANAGED.toString())
                .clusterGroupId("dummy_cluster_group_id")
                .status(ClusterStatusEnum.READY.toString())
                .k8sVersion("2.6")
                .gpuUsage(Map.of("A100", gpuCapacity))
                .sisConfig(SisConfig.builder()
                                   .publicKeysetEndpoint("dummy_keyset_endpoint")
                                   .tokenURL("dummy_token_url")
                                   .spotServiceURL("dummy_url")
                                   .build())
                .vaultConfig(VaultConfig.builder().address("dummy_vault_address").build())
                .build();
    }

    public static ClustersByAuthorizedAccountsEntity getDummyClustersByAuthorizedAccountResp(
            String clusterGroupName, String clusterGroupId,
            String clusterId, String ncaId,
            String instanceTypeName,
            String instanceTypeValue,
            String gpuName, int gpuCount, String authorizedNcaId) {

        Map<String, CreationQueueUdt> creationQueueMap = new HashMap<>();
        creationQueueMap.put(gpuName, CreationQueueUdt.builder()
                .url("creation_queue_url")
                .queueType(QueueAttributeName.FifoQueue.toString())
                .build());
        return ClustersByAuthorizedAccountsEntity.builder()
                .key(ClustersByAuthorizedAccountsKey.builder()
                             .ncaIdKey(authorizedNcaId)
                             .clusterId(clusterId)
                             .build())
                .ncaId(ncaId)
                .clusterGroupName(clusterGroupName)
                .clusterGroupId(clusterGroupId)
                .authorizedNcaIds(Set.of(authorizedNcaId))
                .creationQueues(creationQueueMap)
                .gpusV5(Set.of(getDummyGpuV5(instanceTypeName, instanceTypeValue, gpuCount, gpuName)))
                .build();
    }

    public static GpuV4Udt getDummyGpuV4(String name, String value, int gpuCount, String gpuName) {
        var instanceTypeV3Udt = InstanceTypeV3Udt.builder()
                .gpuCount(gpuCount)
                .name(name)
                .value(value)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .build();

        return GpuV4Udt.builder()
                .name(gpuName)
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeV3Udt))
                .build();
    }

    public static GpuV5Udt getDummyGpuV5(String name, String value, int gpuCount, String gpuName) {
        var instanceTypeV5Udt = getDummyInstanceTypeV5(name, value, gpuCount, NodeTypeEnum.SINGLE);

        return GpuV5Udt.builder()
                .name(gpuName)
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeV5Udt))
                .build();
    }

    public static GpuV5Udt getDummyGpuV5(Set<InstanceTypeV5Udt> instanceTypeV5Udts, String gpuName) {
        return GpuV5Udt.builder()
                .name(gpuName)
                .capacity(8)
                .instanceTypes(instanceTypeV5Udts)
                .build();
    }

    public static InstanceTypeV5Udt getDummyInstanceTypeV5(String name, String value, int gpuCount, NodeTypeEnum nodeTypeEnum){
        return InstanceTypeV5Udt.builder()
                .gpuCount(gpuCount)
                .name(name)
                .value(value)
                .description("GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .nodeType(nodeTypeEnum.toString())
                .build();
    }

    public static GpuUdt getDummyGpuFroBart(String name, String value, int gpuCount, String gpuName) {
        if (gpuName == null) {
            return null;
        }
        InstanceTypeUdt instanceType = InstanceTypeUdt.builder()
                .name(name)
                .value(value)
                .gpuCount(gpuCount)
                .build();

        return GpuUdt.builder()
                .instanceTypes(Set.of(instanceType))
                .name(gpuName)
                .build();

    }

    public static ClusterGroupsByAccountEntity getDummyClusterGroupsByAccountEntity(
            String clusterGroupName, String ncaId) {
        return ClusterGroupsByAccountEntity.builder()
                .key(ClusterGroupsByAccountKey.builder()
                             .clusterGroupName(clusterGroupName)
                             .ncaId(ncaId)
                             .build())
                .clusterGroupId(DUMMY_CLUSTER_GROUP_ID)
                .authorizedNcaIds(Set.of())
                .build();
    }

    public static ClientRequestDataModel getDummyClientRequestDataModel(String instanceType,
                                                                        String ncaId,
                                                                        String requestId,
                                                                        String backend) {
        ClientRequestDataModel.LaunchSpecification launchSpecification =
                ClientRequestDataModel.LaunchSpecification.builder()
                        .gpu(DUMMY_GPU)
                        .backend(backend)
                        .instanceType(instanceType)
                        .containerImage(DUMMY_CONTAINER_IMAGE)
                        .ncaId(ncaId)
                        .functionId(DUMMY_FUNCTION_ID)
                        .versionId(DUMMY_FUNCTION_VERSION_ID)
                        .deploymentId(UUID.randomUUID())
                        .gpuSpecificationId(UUID.randomUUID())
                        .build();

        return ClientRequestDataModel.builder()
                .instanceCount(2)
                .sub(DUMMY_CUSTOMER_ID)
                .spotInstanceRequestAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .requestId(requestId)
                .launchSpecification(launchSpecification)
                .build();
    }

    public static ObjectMapper customObjectMapper() {
        return JsonMapper.builder()
                .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                .disable(DateTimeFeature.WRITE_DATES_AS_TIMESTAMPS)
                .build();
    }

    public static List<String> getRandomStringList(int count, String prefix) {
        ArrayList<String> result = new ArrayList<>();

        if (prefix == null) {
            prefix = "";
        }
        for(int i = 0; i < count; i++) {
            result.add(getRandomStringWithPrefix(prefix, 5));
        }

        return result;
    }

    public static String getRandomStringWithPrefix(String prefix, int length) {
        return "RAND_" + prefix + "_" + getRandomString(length);
    }


    public static String getRandomString(int length) {
        return UUID.randomUUID().toString().replace("_", "").substring(0, length);
    }

    public static CloudHealthEntity getDummyCloudHealthEntity(
            String zoneName, String gpuName, CloudHealthStatus cloudHealthStatus,
            ResourceProvider resourceProvider, int available, int allocated, int capacity) {
        return CloudHealthEntity.builder()
                .status(cloudHealthStatus)
                .gpuUsage(Map.of(gpuName,
                                 GpuCapacity.builder().available(available)
                                         .allocated(allocated).capacity(capacity).build()))
                .key(CloudHealthKey.builder()
                             .cloudProvider(resourceProvider)
                             .zone(zoneName)
                             .build()).build();
    }

    public static CreationQueueUdt getCreationQueue(String gpuName, String clusterGroupId) {
        return CreationQueueUdt.builder()
                .url(String.format(
                        "https://sqs.us-west-2.amazonaws.com/123456/q_gdn_spot_byoc_%s_%s.fifo",
                        clusterGroupId, gpuName))
                .queueType(QueueAttributeName.FifoQueue.toString())
                .build();
    }

    public static CreationQueueUdt getCreationQueueForTasks(String gpuName, String clusterId) {
        return CreationQueueUdt.builder()
                .url(String.format(
                        "https://sqs.us-west-2.amazonaws.com/123456/q_gdn_spot_byoc_tasks_%s_%s.fifo",
                        clusterId, gpuName))
                .queueType(QueueAttributeName.FifoQueue.toString())
                .build();
    }

    public static ReservationEntity getDummyReservation(UUID reservationId) {
        return ReservationEntity.builder()
                .reservationId(reservationId)
                .ncaId(DUMMY_NON_BYOC_NCA_ID)
                .clusterId(DUMMY_CLUSTER_ID)
                .gpuType(DUMMY_GPU_NAME)
                .reservedGpuCount(2)
                .availableGpuCount(2.0)
                .startTime(Instant.now().minusSeconds(3600))
                .endTime(Instant.now().plusSeconds(3600))
                .name("test-reservation")
                .build();
    }

    public static ReservationEntity getDummyReservation(String clusterId, String gpuType, double availableGpuCount) {
        Instant now = Instant.now();
        return ReservationEntity.builder()
                .reservationId(UUID.randomUUID())
                .ncaId(DUMMY_NON_BYOC_NCA_ID)
                .clusterId(clusterId)
                .gpuType(gpuType)
                .reservedGpuCount(8)
                .availableGpuCount(availableGpuCount)
                .startTime(now.minusSeconds(3600))
                .endTime(now.plusSeconds(3600))
                .name("Test Reservation")
                .build();
    }

    public static SpotInstanceRequestSchema getDummyInstanceRequestSchema(int instanceCount) {
        SpotInstanceRequestSchema request = new SpotInstanceRequestSchema();
        request.setNcaId(DUMMY_NON_BYOC_NCA_ID);
        request.setGpu(DUMMY_GPU_NAME);
        request.setInstanceCount(instanceCount);
        return request;
    }
}
