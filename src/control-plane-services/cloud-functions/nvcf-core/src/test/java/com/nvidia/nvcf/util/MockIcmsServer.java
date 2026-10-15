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
package com.nvidia.nvcf.util;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.delete;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.put;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathMatching;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.COMPLETE;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.WITHOUT_ERROR_BODY_500;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.WITH_ERROR_BODY_400;
import static com.nvidia.nvcf.util.MockIcmsServer.InstancesHealthState.HEALTHY;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID_2;
import static com.nvidia.nvcf.util.TestUtil.readFileAsString;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.common.ListOrSingle;
import com.github.tomakehurst.wiremock.core.WireMockConfiguration;
import com.github.tomakehurst.wiremock.extension.ResponseTransformerV2;
import com.github.tomakehurst.wiremock.extension.responsetemplating.helpers.FormParser;
import com.github.tomakehurst.wiremock.http.Request;
import com.github.tomakehurst.wiremock.http.Response;
import com.github.tomakehurst.wiremock.matching.EqualToPattern;
import com.github.tomakehurst.wiremock.stubbing.ServeEvent;
import com.nvidia.nvcf.icms.client.IcmsStubService;
import com.nvidia.nvcf.icms.client.IcmsStubService.GetInstancesResponse.InstanceRequest;
import com.nvidia.nvcf.icms.client.IcmsStubService.Instance;
import java.time.Instant;
import java.util.ArrayList;
import java.util.EnumSet;
import java.util.List;
import java.util.UUID;
import lombok.Builder;
import lombok.Data;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.HttpHeaders;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@UtilityClass
public class MockIcmsServer {

    public enum TestGpu {
        GFN_T10("g6.full"),
        GFN_L40G("gl40g_1.br25_2xlarge");

        @Getter
        private final String instanceType;

        TestGpu(String instanceType) {
            this.instanceType = instanceType;
        }
    }

    public enum InstancesHealthState {
        HEALTHY,
        UNHEALTHY,
        MIXED
    }

    public enum ClusterGroupsResponseState {
        COMPLETE,
        EMPTY_BODY,
        NO_BODY,
        MISSING_CLUSTER_GROUP,
        MISSING_GPUS,
        MISSING_GPU,
        MISSING_INSTANCE_TYPES,
        MISSING_INSTANCE_TYPE_DEFAULT,
        WITH_ERROR_BODY_400,
        WITHOUT_ERROR_BODY_500
    }

    public enum InstanceState {
        RUNNING("running"),
        STARTING("starting"),
        SHUTTING_DOWN("shutting-down"),
        TERMINATED("terminated");

        @Getter
        private String name;

        InstanceState(String name) {
            this.name = name;
        }
    }

    @Data
    @Builder
    public static class IcmsRequestHealthContext {
        TestGpu gpu;
        InstancesHealthState instanceHealthState;
    }

    @Data
    @Builder(toBuilder = true)
    public static class IcmsInstancesContext {
        UUID gpuSpecId;
        int instanceCount;
        InstanceState instanceState;
        String instanceType;
        String availabilityZone;
    }

    private record IcmsResponseContext(
            List<IcmsRequestHealthContext> healthContexts,
            List<IcmsInstancesContext> instancesContexts) {
    }

    private static final String KEY_RESPONSE_CONTEXT = "ResponseContext";

    @Getter
    private static WireMockServer mockIcmsServer;

    private static JsonMapper jsonMapper;

    @Getter
    private static long cacheSize;

    @SneakyThrows
    public static void start(int port, JsonMapper jsonMapper) {
        var healthContexts = List.of(
                IcmsRequestHealthContext.builder().gpu(TestGpu.GFN_T10)
                        .instanceHealthState(HEALTHY).build(),
                IcmsRequestHealthContext.builder().gpu(TestGpu.GFN_L40G)
                        .instanceHealthState(HEALTHY).build()
        );
        start(port, jsonMapper, healthContexts);
    }

    @SneakyThrows
    public static void start(
            int port,
            JsonMapper jsonMapper,
            List<IcmsRequestHealthContext> healthContexts) {
        start(port, jsonMapper, healthContexts, List.of(), COMPLETE, true);
    }

    @SneakyThrows
    public static void start(
            int port,
            JsonMapper jsonMapper,
            List<IcmsRequestHealthContext> healthContexts,
            ClusterGroupsResponseState clusterGroupsResponseState) {
        start(port, jsonMapper, healthContexts, List.of(), clusterGroupsResponseState, true);
    }

    @SneakyThrows
    public static void start(
            int port,
            List<IcmsInstancesContext> instancesContexts) {
        var healthContexts = List.of(
                IcmsRequestHealthContext.builder().gpu(TestGpu.GFN_T10)
                        .instanceHealthState(HEALTHY).build(),
                IcmsRequestHealthContext.builder().gpu(TestGpu.GFN_L40G)
                        .instanceHealthState(HEALTHY).build()
        );
        start(port, jsonMapper, healthContexts, instancesContexts, COMPLETE, true);
    }

    @SneakyThrows
    public static void start(
            int port,
            JsonMapper jsonMapper,
            List<IcmsRequestHealthContext> healthContexts,
            ClusterGroupsResponseState clusterGroupsResponseState,
            boolean getInstanceTypesEnabled) {
        start(port, jsonMapper, healthContexts, List.of(),
              clusterGroupsResponseState, getInstanceTypesEnabled);
    }

    @SneakyThrows
    public static void start(
            int port,
            JsonMapper jsonMapper,
            List<IcmsRequestHealthContext> healthContexts,
            List<IcmsInstancesContext> instancesContexts,
            ClusterGroupsResponseState clusterGroupsResponseState,
            boolean getInstanceTypesEnabled) {
        MockIcmsServer.jsonMapper = jsonMapper;
        stop();
        var icmsInstanceRequestExtension = new RequestIcmsInstancesResponseTransformer();
        var instancesRequestsResponseTransformer = new InstancesRequestsResponseTransformer();
        var config = WireMockConfiguration.options()
                .port(port)
                .extensions(icmsInstanceRequestExtension, instancesRequestsResponseTransformer);
        mockIcmsServer = new WireMockServer(config);
        mockIcmsServer.stubFor(post(urlPathEqualTo("/v1/si"))
                                .withQueryParam("Action",
                                                new EqualToPattern("RequestInstances"))
                                .willReturn(aResponse().withStatus(200)
                                            .withTransformers(icmsInstanceRequestExtension.getName())
                                            .withHeader(HttpHeaders.CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                            .withBody("{}")));
        mockIcmsServer.stubFor(get(urlPathMatching(
                "/v1/si/accounts/(.+)?/workloads/(.+)?/instances"))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withTransformer(instancesRequestsResponseTransformer.getName(),
                                                                           KEY_RESPONSE_CONTEXT,
                                                                           new IcmsResponseContext(
                                                                                   healthContexts,
                                                                                   instancesContexts))
                                                          .withHeader(HttpHeaders.CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                                          .withBody("{}")));
        mockIcmsServer.stubFor(delete(urlPathEqualTo("/v1/si"))
                                .withQueryParam("Action",
                                                new EqualToPattern("TerminateInstances"))
                                .willReturn(aResponse().withStatus(200)));
        mockIcmsServer.stubFor(delete(urlPathMatching("/v1/si/accounts/(.+)?/instances/(.+)?"))
                                .willReturn(aResponse().withStatus(200)
                                                    .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                APPLICATION_JSON_VALUE)
                                                    .withBody("{\"TerminatingInstances\":[]}")));
        mockIcmsServer.stubFor(delete(urlPathMatching("/v1/si/accounts/(.+)?/workloads/(.+)?"))
                                .willReturn(aResponse().withStatus(200)
                                                    .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                APPLICATION_JSON_VALUE)
                                                    .withBody("{\"TerminatingInstances\":[]}")));
        mockIcmsServer.stubFor(put(urlPathMatching(
                "/v1/si/accounts/(.+)?/functions/(.+)?/versions/(.+)?/workloads/(.+)?/specs/(.+)?/requests/(.+)"))
                                      .willReturn(aResponse().withStatus(200)));
        addStubForGetClusterGroupsEndpoint(mockIcmsServer, clusterGroupsResponseState);
        if (getInstanceTypesEnabled) {
            addStubForGetInstanceTypesEndpoint(mockIcmsServer);
        }
        addStubForGetClustersEndpoint(mockIcmsServer);
        mockIcmsServer.start();
    }

    public static void stop() {
        if (mockIcmsServer != null) {
            mockIcmsServer.stop();
        }
    }

    private static void addStubForGetClusterGroupsEndpoint(
            WireMockServer mockIcmsServer,
            ClusterGroupsResponseState responseState) {
        var body = switch (responseState) {
            case COMPLETE ->
                    readFileAsString("fixtures/icms/cluster-groups/complete-response.json");
            case EMPTY_BODY -> "{}";
            case NO_BODY, WITHOUT_ERROR_BODY_500 -> null;
            case MISSING_CLUSTER_GROUP ->
                    readFileAsString(
                            "fixtures/icms/cluster-groups/missing-gfn-cluster-group-response.json");
            case MISSING_GPUS ->
                    readFileAsString("fixtures/icms/cluster-groups/missing-gfn-gpus-response.json");
            case MISSING_GPU ->
                    readFileAsString("fixtures/icms/cluster-groups/missing-t10-gpu-response.json");
            case MISSING_INSTANCE_TYPES ->
                    readFileAsString(
                            "fixtures/icms/cluster-groups/missing-t10-instance-types-response.json");
            case MISSING_INSTANCE_TYPE_DEFAULT ->
                readFileAsString(
                        "fixtures/icms/cluster-groups/missing-t10-instance-type-default-response.json");
            case WITH_ERROR_BODY_400 -> "{\"error\": \"pretend bad deployment spec\"}";
        };
        var errorSet = EnumSet.of(WITH_ERROR_BODY_400, WITHOUT_ERROR_BODY_500);
        var status = 200;
        if (responseState == WITH_ERROR_BODY_400) {
            status = 400;
        } else if (responseState == WITHOUT_ERROR_BODY_500) {
            status = 500;
        }
        mockIcmsServer.stubFor(get(urlPathMatching("/v1/si/accounts/.*/clusterGroups"))
                                .willReturn(aResponse().withStatus(status)
                                .withHeader(HttpHeaders.CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                .withBody(body)));

    }

    private static void addStubForGetInstanceTypesEndpoint(
            WireMockServer mockIcmsServer) {
        var body = readFileAsString("fixtures/icms/instance-types/complete-response.json");
        mockIcmsServer.stubFor(get(urlPathMatching("/v1/si/accounts/.*/instanceTypes"))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(body)));
    }

    private static void addStubForGetClustersEndpoint(
            WireMockServer mockIcmsServer) {
        var body = readFileAsString("fixtures/icms/clusters/complete-response.json");
        mockIcmsServer.stubFor(get(urlPathMatching("/v1/si/accounts/.*/clusters"))
                                      .withQueryParam("includeAuthorizedClusters",
                                                      new EqualToPattern("true"))
                                      .withQueryParam("includeGfnInAuthorizedClusters",
                                                      new EqualToPattern("true"))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(body)));
    }

    public static class RequestIcmsInstancesResponseTransformer implements ResponseTransformerV2 {

        @Override
        public Response transform(Response response, ServeEvent serveEvent) {
            var request = serveEvent.getRequest();
            var data = FormParser.parse(request.getBodyAsString(), true);
            cacheSize = Long.parseLong(
                    data.getOrDefault("LaunchSpecification.CacheSize", ListOrSingle.of("0"))
                            .getFirst());
            var rawBody = """
                {
                  "requestId": "%s"
                }""";
            var body = rawBody.formatted(UUID.randomUUID());
            return Response.Builder.like(response).body(body).build();
        }

        @Override
        public boolean applyGlobally() {
            return false;
        }

        @Override
        public String getName() {
            return "request-icms-instances";
        }
    }

    public static class InstancesRequestsResponseTransformer implements ResponseTransformerV2 {
        private static final Instant DEFAULT_INSTANCE_CREATE_TIME =
                Instant.parse("2023-07-06T07:17:53.282Z");
        private static final String DEFAULT_CONTAINER_IMAGE =
                "stg.nvcr.io/nv-cf/guineapig-1/ediffi:0.0.3";
        private static final String DEFAULT_AVAILABILITY_ZONE = "NP-LAX-03";

        private static final String RAW_INSTANCE_RUNNING_JSON =
                readFileAsString("fixtures/icms/deployment-instances/raw-instance-running.json");
        private static final String RAW_INSTANCE_STARTING_JSON =
                readFileAsString("fixtures/icms/deployment-instances/raw-instance-starting.json");
        private static final String RAW_INSTANCE_SHUTTING_DOWN_JSON =
                readFileAsString(
                        "fixtures/icms/deployment-instances/raw-instance-shutting-down.json");
        private static final String RAW_INSTANCE_TERMINATED_JSON =
                readFileAsString("fixtures/icms/deployment-instances/raw-instance-terminated.json");

        @Override
        public Response transform(Response response, ServeEvent serveEvent) {
            // Tests are set up with multiple deployment specs. Tests will provide gpuSpecId,
            // number of instances and their status.
            var request = serveEvent.getRequest();
            var parameters = serveEvent.getTransformerParameters();
            var responseContext = (IcmsResponseContext) parameters.get(KEY_RESPONSE_CONTEXT);
            var instancesContexts = responseContext.instancesContexts();
            var body = instancesContexts.isEmpty()
                    ? buildBodyFromHealthContexts(
                            responseContext.healthContexts(), request)
                    : buildBody(instancesContexts, request);
            return Response.Builder.like(response).body(body).build();
        }

        @SneakyThrows
        private static String buildBodyFromHealthContexts(
                List<IcmsRequestHealthContext> healthContexts,
                Request request) {
            if (healthContexts == null || healthContexts.isEmpty()) {
                return jsonMapper.writeValueAsString(
                        IcmsStubService.Instances.builder().Instances(List.of()).build());
            }
            var deploymentId = extractDeploymentId(request);
            var instances = getInstancesFromHealthContext(healthContexts.getFirst(), deploymentId);
            return jsonMapper.writeValueAsString(
                    IcmsStubService.Instances.builder().Instances(instances).build());
        }

        private static List<Instance> getInstancesFromHealthContext(
                IcmsRequestHealthContext context,
                UUID deploymentId) {
            var healthState = context.getInstanceHealthState() == null
                    ? HEALTHY : context.getInstanceHealthState();
            return switch (healthState) {
                case HEALTHY -> List.of(
                        buildInstance(context, deploymentId, InstanceState.RUNNING),
                        buildInstance(context, deploymentId, InstanceState.RUNNING));
                case UNHEALTHY -> List.of(
                        buildInstance(context, deploymentId, InstanceState.TERMINATED),
                        buildInstance(context, deploymentId, InstanceState.TERMINATED));
                case MIXED -> List.of(
                        buildInstance(context, deploymentId, InstanceState.RUNNING),
                        buildInstance(context, deploymentId, InstanceState.TERMINATED));
            };
        }

        private static Instance buildInstance(
                IcmsRequestHealthContext context,
                UUID deploymentId,
                InstanceState state) {
            var gpu = context.getGpu();
            return Instance.builder()
                    .createTime(DEFAULT_INSTANCE_CREATE_TIME)
                    .containerImage(DEFAULT_CONTAINER_IMAGE)
                    .instanceId(UUID.randomUUID().toString())
                    .cloudProvider("GFN")
                    .instanceType(gpu.getInstanceType())
                    .placement(InstanceRequest.Placement.builder()
                                       .availabilityZone(DEFAULT_AVAILABILITY_ZONE)
                                       .build())
                    .state(InstanceRequest.InstanceState.builder()
                                   .code(toStateCode(state))
                                   .name(state.getName())
                                   .build())
                    .healthInfo(toHealthInfo(context, state))
                    .launchRequestId(UUID.randomUUID().toString())
                    .capacityType("RESERVED")
                    .deploymentId(deploymentId)
                    .gpuSpecificationId(toGpuSpecificationId(gpu))
                    .build();
        }

        private static InstanceRequest.HealthInfo toHealthInfo(
                IcmsRequestHealthContext context,
                InstanceState state) {
            if (state == InstanceState.RUNNING || state == InstanceState.STARTING) {
                return null;
            }
            return InstanceRequest.HealthInfo.builder()
                    .errorLog("%s: Inference container\n is failing\n to come up"
                                      .formatted(context.getGpu().name()))
                    .build();
        }

        private static int toStateCode(InstanceState state) {
            return switch (state) {
                case RUNNING -> 16;
                case STARTING -> 0;
                case SHUTTING_DOWN -> 32;
                case TERMINATED -> 48;
            };
        }

        private static UUID toGpuSpecificationId(TestGpu gpu) {
            return switch (gpu) {
                case GFN_T10 -> TEST_GPU_SPEC_ID;
                case GFN_L40G -> TEST_GPU_SPEC_ID_2;
            };
        }

        private static UUID extractDeploymentId(Request request) {
            var path = request.getUrl().split("\\?")[0];
            var start = path.indexOf("/workloads/") + "/workloads/".length();
            var end = path.indexOf("/instances", start);
            return UUID.fromString(path.substring(start, end));
        }

        @SneakyThrows
        private static String buildBody(
                List<IcmsInstancesContext> instancesContexts,
                Request request) {
            List<Instance> instances = new ArrayList<>();
            var deploymentId = extractDeploymentId(request);
            for (IcmsInstancesContext context : instancesContexts) {
                for (int i = 0; i < context.getInstanceCount(); i++) {
                    try {
                        var instanceType = toInstanceType(context);
                        var availabilityZone = StringUtils.defaultIfBlank(
                                context.getAvailabilityZone(), DEFAULT_AVAILABILITY_ZONE);
                        var launchRequestId = UUID.randomUUID();
                        var instance = switch (context.getInstanceState()) {
                            case RUNNING -> jsonMapper.readValue(
                                    RAW_INSTANCE_RUNNING_JSON.formatted(
                                            UUID.randomUUID(),
                                            instanceType,
                                            availabilityZone,
                                            launchRequestId,
                                            deploymentId,
                                            context.getGpuSpecId()),
                                    Instance.class);
                            case STARTING -> jsonMapper.readValue(
                                    RAW_INSTANCE_STARTING_JSON.formatted(
                                            UUID.randomUUID(),
                                            instanceType,
                                            availabilityZone,
                                            launchRequestId,
                                            deploymentId,
                                            context.getGpuSpecId()),
                                    Instance.class);
                            case SHUTTING_DOWN -> jsonMapper.readValue(
                                    RAW_INSTANCE_SHUTTING_DOWN_JSON.formatted(
                                            UUID.randomUUID(),
                                            instanceType,
                                            availabilityZone,
                                            instanceType,
                                            launchRequestId,
                                            deploymentId,
                                            context.getGpuSpecId()),
                                    Instance.class);
                            case TERMINATED -> jsonMapper.readValue(
                                    RAW_INSTANCE_TERMINATED_JSON.formatted(
                                            UUID.randomUUID(),
                                            instanceType,
                                            availabilityZone,
                                            instanceType,
                                            launchRequestId,
                                            deploymentId,
                                            context.getGpuSpecId()),
                                    Instance.class);
                        };
                        instances.add(instance);
                    } catch (Exception ex) {
                        log.error("Instance context gpu spec id: '{}'; number of instances: '{}', "
                                          + "State: '{}', Exception: '{}'",
                                  context.getGpuSpecId(), context.getInstanceCount(),
                                  context.getInstanceState(),
                                  ex.getMessage());
                        throw ex;
                    }
                }
            }
            return jsonMapper.writeValueAsString(
                    IcmsStubService.Instances.builder().Instances(instances).build());
        }

        private static String toInstanceType(IcmsInstancesContext context) {
            if (StringUtils.isNotBlank(context.getInstanceType())) {
                return context.getInstanceType();
            }
            if (TEST_GPU_SPEC_ID_2.equals(context.getGpuSpecId())) {
                return L40G_INSTANCE_TYPE;
            }
            return T10_INSTANCE_TYPE;
        }

        @Override
        public String getName() {
            return "instances-request";
        }

        @Override
        public boolean applyGlobally() {
            return false;
        }
    }
}
