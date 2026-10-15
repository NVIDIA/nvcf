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
package com.nvidia.nvct.util;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.delete;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathMatching;
import static com.nvidia.nvct.util.MockIcmsServer.ClusterGroupsResponseState.COMPLETE;
import static com.nvidia.nvct.util.MockIcmsServer.InstancesState.HEALTHY;
import static com.nvidia.nvct.util.TestConstants.L40G;
import static com.nvidia.nvct.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvct.util.TestUtil.readFileAsString;
import static org.springframework.http.HttpHeaders.CONTENT_TYPE;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.common.ListOrSingle;
import com.github.tomakehurst.wiremock.core.WireMockConfiguration;
import com.github.tomakehurst.wiremock.extension.ResponseTransformerV2;
import com.github.tomakehurst.wiremock.extension.responsetemplating.helpers.FormParser;
import com.github.tomakehurst.wiremock.http.Response;
import com.github.tomakehurst.wiremock.matching.EqualToPattern;
import com.github.tomakehurst.wiremock.stubbing.ServeEvent;
import com.nvidia.nvct.rest.task.dto.InstanceUsageTypeEnum;
import com.nvidia.nvct.service.icms.IcmsStubService;
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse;
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest;
import java.net.URL;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collection;
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

    public enum InstancesState {
        HEALTHY,
        UNHEALTHY,
        NO_CAPACITY
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

    public static final String GPU_INSTANCETYPE = "g6.full";
    public static final String GPU_NAME = "GFN_T10";

    @Data
    @Builder(toBuilder = true)
    public static class IcmsRequestContext {
        String icmsRequestId;
        InstancesState instanceState;
        String createTime;
    }

    private static final String KEY_CONTEXT = "Context";

    @Getter
    private static WireMockServer mockIcmsServer;

    private static JsonMapper jsonMapper;

    @Getter
    private static long cacheSize;

    @Getter
    private static String capturedHelmValidationPolicy;

    @SneakyThrows
    public static void start(URL url, JsonMapper jsonMapper) {
        var contexts = List.of(IcmsRequestContext.builder().instanceState(HEALTHY).build());
        start(url, jsonMapper, contexts);
    }

    @SneakyThrows
    public static void start(
            URL url,
            JsonMapper jsonMapper,
            List<IcmsRequestContext> contexts) {
        start(url, jsonMapper, contexts, COMPLETE);
    }

    @SneakyThrows
    public static void start(
            URL url,
            JsonMapper jsonMapper,
            List<IcmsRequestContext> contexts,
            ClusterGroupsResponseState clusterGroupsResponseState) {
        stop();
        MockIcmsServer.jsonMapper = jsonMapper;
        var instanceRequestExtension = new RequestInstancesResponseTransformer();
        var getInstancesByTaskIdExtension = new GetInstancesByTaskIdResponseTransformer();
        var config = WireMockConfiguration.options()
                .port(url.getPort())
                .extensions(instanceRequestExtension,
                            getInstancesByTaskIdExtension);
        mockIcmsServer = new WireMockServer(config);
        mockIcmsServer.stubFor(post(urlPathEqualTo("/v1/si"))
                                      .withQueryParam("Action",
                                                      new EqualToPattern("RequestInstances"))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withTransformers(instanceRequestExtension.getName())
                                                          .withHeader(CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                                          .withBody("{}")));
        mockIcmsServer.stubFor(get(urlPathMatching("/v1/si/accounts/.*/deployments/.*/instances"))
                                      .withQueryParam("IncludeTerminated",
                                                      new EqualToPattern("false"))
                                      .withQueryParam("UseConciseName",
                                                      new EqualToPattern("true"))
                                      .withQueryParam("ExpiredAckedInstances",
                                                      new EqualToPattern("false"))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withTransformer(
                                                                  getInstancesByTaskIdExtension.getName(),
                                                                  KEY_CONTEXT,
                                                                  contexts)
                                                          .withHeader(CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                                          .withBody("{}")));
        mockIcmsServer.stubFor(get(urlPathMatching("/v1/si/accounts/.*/deployments/.*/instances"))
                                      .withQueryParam("IncludeTerminated",
                                                      new EqualToPattern("true"))
                                      .withQueryParam("UseConciseName",
                                                      new EqualToPattern("true"))
                                      .withQueryParam("ExpiredAckedInstances",
                                                      new EqualToPattern("true"))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withTransformer(
                                                                  getInstancesByTaskIdExtension.getName(),
                                                                  KEY_CONTEXT,
                                                                  contexts)
                                                          .withHeader(CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                                          .withBody("{}")));
        mockIcmsServer.stubFor(delete(urlPathEqualTo("/v1/si"))
                                      .withQueryParam("Action",
                                                      new EqualToPattern("TerminateInstances"))
                                      .willReturn(aResponse().withStatus(200)));
        mockIcmsServer.stubFor(delete(urlPathMatching("/v1/si/accounts/.*/workloads/.*"))
                                      .willReturn(aResponse().withStatus(200)));
        addStubForGetClusterGroupsEndpoint(mockIcmsServer, clusterGroupsResponseState);
        addStubForGetClustersEndpoint(mockIcmsServer);
        mockIcmsServer.start();
    }

    public static void stop() {
        if (mockIcmsServer != null) {
            mockIcmsServer.stop();
        }
    }

    public static void stubTerminateInstanceNotFound() {
        mockIcmsServer.stubFor(
                delete(urlPathMatching("/v1/si/accounts/.*/workloads/.*"))
                        .atPriority(1)
                        .willReturn(aResponse().withStatus(404)
                                        .withHeader(CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                        .withBody("{\"detail\": \"Workload not found\"}")));
    }

    private static void addStubForGetClustersEndpoint(
            WireMockServer mockIcmsServer) {
        var body = readFileAsString("fixtures/icms/clusters/complete-response.json");
        mockIcmsServer.stubFor(get(urlPathMatching("/v1/si/accounts/.*/clusters"))
                                      .withQueryParam("includeAuthorizedClusters",
                                                      new EqualToPattern("true"))
                                      .withQueryParam("includeGfnInAuthorizedClusters",
                                                      new EqualToPattern("true"))
                                      .withQueryParam(
                                              "instanceTypeUsage",
                                              equalTo(InstanceUsageTypeEnum.DEFAULT.toString())
                                                      .or(equalTo(InstanceUsageTypeEnum.CONTAINER.toString())))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(body)));
    }

    private static void addStubForGetClusterGroupsEndpoint(
            WireMockServer mockIcmsServer,
            ClusterGroupsResponseState responseState) {
        var body = switch (responseState) {
            case COMPLETE -> readFileAsString("fixtures/icms/cluster-groups/complete-response.json");
            case EMPTY_BODY -> "{}";
            case NO_BODY, WITHOUT_ERROR_BODY_500 -> null;
            case MISSING_CLUSTER_GROUP -> readFileAsString(
                    "fixtures/icms/cluster-groups/missing-gfn-cluster-group-response.json");
            case MISSING_GPUS -> readFileAsString(
                    "fixtures/icms/cluster-groups/missing-gfn-gpus-response.json");
            case MISSING_GPU -> readFileAsString("fixtures/icms/cluster-groups/missing-t10-gpu-response.json");
            case MISSING_INSTANCE_TYPES -> readFileAsString(
                    "fixtures/icms/cluster-groups/missing-t10-instance-types-response.json");
            case MISSING_INSTANCE_TYPE_DEFAULT -> readFileAsString(
                    "fixtures/icms/cluster-groups/missing-t10-instance-type-default-response.json");
            case WITH_ERROR_BODY_400 -> "{\"error\": \"pretend bad deployment spec\"}";
        };
        var status = switch (responseState) {
            case WITH_ERROR_BODY_400 -> 400;
            case WITHOUT_ERROR_BODY_500 -> 500;
            default -> 200;
        };
        mockIcmsServer.stubFor(get(urlPathMatching("/v1/si/accounts/.*/clusterGroups"))
                                      .withQueryParam(
                                              "instanceTypeUsage",
                                              equalTo(InstanceUsageTypeEnum.DEFAULT.toString())
                                                      .or(equalTo(InstanceUsageTypeEnum.CONTAINER.toString())))
                                      .willReturn(aResponse().withStatus(status)
                                                          .withHeader(CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                                          .withBody(body)));
        mockIcmsServer.stubFor(get(urlPathEqualTo("/v1/si"))
                                      .withQueryParam("Action",
                                                      new EqualToPattern("DescribeInstances"))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody("""
                                                                            {
                                                                              "Instances": [
                                                                                {
                                                                                  "ImageId": "<string image id>",
                                                                                  "ContainerImage": "<container image from the launch command>",
                                                                                  "InstanceId": "local-instance",
                                                                                  "InstanceIps": [
                                                                                    "127.0.0.1",
                                                                                    "127.0.0.2"
                                                                                  ],
                                                                                  "CloudProvider": "GFN | OCI | AZURE | AWS | GCP",
                                                                                  "InstanceType": "<instance type>",
                                                                                  "Placement": {
                                                                                    "AvailabilityZone": "np-lax02"
                                                                                  },
                                                                                  "State": {
                                                                                    "Code": 0,
                                                                                    "Name": "running"
                                                                                  },
                                                                                  "HealthInfo": {
                                                                                    "ErrorLog": "<string last 20 lines of logs from the pod that is failed>"
                                                                                  },
                                                                                  "LaunchRequestId": "<launch request id>"
                                                                                }
                                                                              ]
                                                                            }""")));

    }

    public static class RequestInstancesResponseTransformer implements ResponseTransformerV2 {

        @Override
        public Response transform(Response response, ServeEvent serveEvent) {
            var request = serveEvent.getRequest();
            var data = FormParser.parse(request.getBodyAsString(), true);
            cacheSize = Long.parseLong(
                    data.getOrDefault("LaunchSpecification.CacheSize", ListOrSingle.of("0"))
                            .getFirst());
            capturedHelmValidationPolicy =
                    data.containsKey("LaunchSpecification.HelmValidationPolicy")
                            ? data.get("LaunchSpecification.HelmValidationPolicy").getFirst()
                            : null;
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
            return "request-instances";
        }
    }

    public static class DescribeInstanceRequestsResponseTransformer
            implements ResponseTransformerV2 {

        private static final String RAW_HEALTHY_INSTANCE_JSON =
                readFileAsString("fixtures/icms/raw-healthy-instance.json");
        private static final String RAW_UNHEALTHY_INSTANCE_JSON =
                readFileAsString("fixtures/icms/raw-unhealthy-instance.json");
        private static final String RAW_NO_CAPACITY_INSTANCE_JSON =
                readFileAsString("fixtures/icms/raw-no-capacity-instance.json");

        @Override
        public Response transform(Response response, ServeEvent serveEvent) {
            var request = serveEvent.getRequest();
            var parameters = serveEvent.getTransformerParameters();
            var icmsRequestIds = request.queryParameter("InstanceRequestId").values();
            var rawContexts = (List<IcmsRequestContext>) parameters.get(KEY_CONTEXT);
            List<IcmsRequestContext> contexts = new ArrayList<>();
            for (int i = 0; i < icmsRequestIds.size(); i++) {
                var index = i % rawContexts.size();
                var icmsRequestId = icmsRequestIds.get(i);
                var ctx = rawContexts.get(index).toBuilder().icmsRequestId(icmsRequestId).build();
                contexts.add(ctx);
            }
            var body = getTransformedResponseBody(contexts);
            return Response.Builder.like(response).body(body).build();
        }

        @Override
        public boolean applyGlobally() {
            return false;
        }

        @Override
        public String getName() {
            return "describe-instance-requests";
        }

        @SneakyThrows
        private static String getTransformedResponseBody(
                List<IcmsRequestContext> contexts) {
            var instanceRequests = contexts.stream()
                    .map(DescribeInstanceRequestsResponseTransformer::getInstanceRequests)
                    .flatMap(Collection::stream)
                    .toList();
            var responseBody = GetInstancesResponse.builder()
                    .instanceRequests(instanceRequests).build();
            return jsonMapper.writeValueAsString(responseBody);
        }

        @SneakyThrows
        private static List<InstanceRequest> getInstanceRequests(
                IcmsRequestContext context) {
            List<InstanceRequest> instances;
            try {
                instances = switch (context.getInstanceState()) {
                    case HEALTHY -> List.of(
                            jsonMapper.readValue(RAW_HEALTHY_INSTANCE_JSON.formatted(
                                                           UUID.randomUUID(), // instance-id
                                                           GPU_INSTANCETYPE,
                                                           GPU_NAME,
                                                           context.getIcmsRequestId()),
                                                   InstanceRequest.class));
                    case UNHEALTHY -> List.of(
                            jsonMapper.readValue(RAW_UNHEALTHY_INSTANCE_JSON.formatted(
                                                           UUID.randomUUID(),  // instance-id
                                                           GPU_INSTANCETYPE,
                                                           GPU_NAME,
                                                           context.getIcmsRequestId(),
                                                           GPU_NAME),
                                                   InstanceRequest.class));
                    case NO_CAPACITY -> List.of(
                            jsonMapper.readValue(RAW_NO_CAPACITY_INSTANCE_JSON.formatted(
                                                           UUID.randomUUID(),  // instance-id
                                                           L40G_INSTANCE_TYPE,
                                                           L40G,
                                                           context.getIcmsRequestId(),
                                                           L40G),
                                                   InstanceRequest.class));
                };
            } catch (Exception ex) {
                log.error("Context Status: '{}'; ICMS Request Id: '{}', Exception: '{}'",
                          context.getInstanceState(), context.getIcmsRequestId(),
                          ex.getMessage());
                throw ex;
            }

            log.info("Context Status: '{}'", context.getInstanceState());
            log.info("Instance1: '{}'", instances.getFirst());
            return instances;
        }
    }

    public static class GetInstancesByTaskIdResponseTransformer
            implements ResponseTransformerV2 {

        @Override
        public Response transform(Response response, ServeEvent serveEvent) {
            var parameters = serveEvent.getTransformerParameters();
            var rawContexts = (List<IcmsRequestContext>) parameters.get(KEY_CONTEXT);
            var contexts = rawContexts.stream()
                    .map(context -> {
                        var icmsRequestId = StringUtils.isBlank(context.getIcmsRequestId())
                                ? UUID.randomUUID().toString()
                                : context.getIcmsRequestId();
                        return context.toBuilder().icmsRequestId(icmsRequestId).build();
                    })
                    .toList();
            var body = getTransformedResponseBody(contexts);
            return Response.Builder.like(response).body(body).build();
        }

        @Override
        public boolean applyGlobally() {
            return false;
        }

        @Override
        public String getName() {
            return "get-instances-by-task-id";
        }

        @SneakyThrows
        private static String getTransformedResponseBody(
                List<IcmsRequestContext> contexts) {
            var instances = contexts.stream()
                    .map(GetInstancesByTaskIdResponseTransformer::getInstances)
                    .flatMap(Collection::stream)
                    .toList();
            var responseBody = IcmsStubService.Instances.builder()
                    .Instances(instances).build();
            return jsonMapper.writeValueAsString(responseBody);
        }

        @SneakyThrows
        private static List<IcmsStubService.Instance> getInstances(
                IcmsRequestContext context) {
            return DescribeInstanceRequestsResponseTransformer.getInstanceRequests(context)
                    .stream()
                    .map(instance -> GetInstancesByTaskIdResponseTransformer.toInstance(
                            instance, context))
                    .toList();
        }

        private static IcmsStubService.Instance toInstance(
                InstanceRequest instanceRequest,
                IcmsRequestContext context) {
            return IcmsStubService.Instance.builder()
                    .createTime(context.getCreateTime() != null ?
                                        Instant.parse(context.getCreateTime()) : Instant.now())
                    .containerImage(instanceRequest.getLaunchSpecification().getContainerImage())
                    .instanceId(instanceRequest.getInstanceId())
                    .cloudProvider(instanceRequest.getCloudProvider())
                    .instanceType(instanceRequest.getLaunchSpecification().getInstanceType())
                    .placement(InstanceRequest.Placement.builder()
                                       .availabilityZone(instanceRequest.getLaunchedAvailabilityZone())
                                       .build())
                    .state(instanceRequest.getInstanceState())
                    .healthInfo(instanceRequest.getHealthInfo())
                    .launchRequestId(instanceRequest.getInstanceRequestId().toString())
                    .build();
        }
    }
}
