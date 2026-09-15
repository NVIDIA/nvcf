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
package com.nvidia.nvct.service.icms;

import static com.nvidia.nvct.util.NvctConstants.MAX_BUFFER_LIMIT;
import static org.apache.commons.lang3.StringUtils.isBlank;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvct.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvct.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientIcmsProperties;
import com.nvidia.nvct.rest.task.dto.InstanceUsageTypeEnum;
import com.nvidia.nvct.service.icms.IcmsStubService.ClusterGroupsResponse;
import com.nvidia.nvct.service.icms.IcmsStubService.ClusterGroupsResponse.ClusterGroup;
import com.nvidia.nvct.service.icms.IcmsStubService.ClusterGroupsResponse.ClusterGroup.Gpu;
import com.nvidia.nvct.service.icms.IcmsStubService.ClusterGroupsResponse.ClusterGroup.Gpu.InstanceType;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils.ManagedHttpResources;
import java.time.Duration;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;


@Slf4j
@Service
@RefreshScope
public class IcmsClusterGroupClient {

    private static final String CLIENT_REGISTRATION_ID = "icms";

    private static final String MESG_INVALID_GPU =
            "Cluster Group '%s': Invalid GPU '%s' specified";
    private static final String MESG_INVALID_GET_INSTANCE_TYPE_ARGUMENT =
            "Invalid argument specified for getting default instance type";
    private static final String MESG_INVALID_CLUSTER_GROUP =
            "Invalid Backend or Cluster-Group '%s' specified";
    private static final String MESG_MISSING_GPUS =
            "ClusterGroup '%s': Missing GPUs in ICMS response";
    private static final String MESG_MISSING_INSTANCE_TYPES =
            "ClusterGroup '%s', GPU '%s': Missing instance-types in ICMS response";
    private static final String MESG_MISSING_DEFAULT_INSTANCE_TYPE =
            "Cluster Group '%s', GPU '%s': Missing default instance-type";
    private static final String MESG_MISSING_CLUSTER_GROUPS =
            "Account '%s': Missing cluster-groups in successful ICSM response";
    private static final String MESG_DEFAULT_INSTANCE_TYPE_DETAILS =
            "ClusterGroup: '{}', GPU: '{}', Default InstanceType: '{}'";
    private static final String MESG_FETCH_CLUSTER_GROUPS =
            "Account '{}': Fetching Cluster Groups from ICMS for instance type '{}'";
    private static final String MESG_BLANK_PARAMETER = "'%s' cannot be blank";

    private final IcmsStubService icmsStubService;
    private final LoadingCache<IcmsCacheKey, List<ClusterGroup>> clusterGroupCache;

    private record IcmsCacheKey(String ncaId, InstanceUsageTypeEnum instanceTypeUsage) {
    }

    // We could have used OAuth2AuthorizedClientManager and relied on Spring Security to
    // pick up the configuration properties using ClientRegistrationRepository directly. However,
    // the client-secret value held in the ClientRegistrationRepository does not get refreshed when
    // client-secret is rotated. Addressing these issues requires introducing a refreshable
    // ClientRegistrationRepository that wasn't clean. Instead, we will keep it simple and use
    // the tried and tested approach of using @Value and @RefreshScope annotations and wire
    // things up ourselves.
    public IcmsClusterGroupClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            ManagedHttpResources icmsHttpResources,
            @Value("${nvct.icms.base-url}") String baseUrl,
            @Value("${spring.security.oauth2.client.registration.icms.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.icms.client-secret}") String clientSecret,
            @Value("${spring.security.oauth2.client.registration.icms.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.icms.token-uri}") String tokenUri,
            Optional<StaticClientIcmsProperties> staticClientIcmsProperties) {

        var authFilter = oauthFilter(staticClientIcmsProperties, webClientBuilder,
                                     clientId, clientSecret, scope, tokenUri);
        var webClient = webClientBuilder
                .baseUrl(baseUrl)
                .codecs(configurer -> configurer.defaultCodecs().maxInMemorySize(MAX_BUFFER_LIMIT))
                .clientConnector(icmsHttpResources.connector())
                .filter(NvctOAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(authFilter)
                .filter(NvctOAuth2ClientUtils.getResponseFilterProcessor("ICMS"))
                .build();
        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.icmsStubService = factory.createClient(IcmsStubService.class);
        this.clusterGroupCache = Caffeine.newBuilder()
                .maximumSize(64)
                .expireAfterWrite(Duration.ofMinutes(15))
                .scheduler(Scheduler.systemScheduler())
                .build(this::fetchClusterGroups);
    }

    private static ExchangeFilterFunction oauthFilter(
            Optional<StaticClientIcmsProperties> staticClientIcmsProperties,
            WebClient.Builder webClientBuilder,
            String clientId,
            String clientSecret,
            String scope,
            String tokenUri) {
        return staticClientIcmsProperties
                .map(p -> (ExchangeFilterFunction)
                        new FixedBearerExchangeFilterFunction(p::getToken))
                .orElseGet(() -> NvctOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    public String getDefaultInstanceType(String ncaId, InstanceUsageTypeEnum instanceUsage,
                                         String clusterGroupName, String gpuName) {
        if (isBlank(ncaId) || isBlank(clusterGroupName) || isBlank(gpuName)) {
            log.error(MESG_INVALID_GET_INSTANCE_TYPE_ARGUMENT);
            throw new IllegalArgumentException(MESG_INVALID_GET_INSTANCE_TYPE_ARGUMENT);
        }

        var clusterGroups = getClusterGroups(ncaId, instanceUsage);
        var targetClusterGroup = targetClusterGroup(ncaId, clusterGroupName, clusterGroups);
        var targetGpu = targetGpu(clusterGroupName, gpuName, targetClusterGroup.getGpus());
        var defaultInstanceType = defaultInstanceType(clusterGroupName, gpuName,
                                                      targetGpu.getInstanceTypes());
        log.info(MESG_DEFAULT_INSTANCE_TYPE_DETAILS,
                 targetClusterGroup.getName(), targetGpu.getName(), defaultInstanceType.getName());
        return defaultInstanceType.getName(); // Return name -- not the value.
    }

    @VisibleForTesting
    public void clearClusterGroupCache() {
        clusterGroupCache.invalidateAll();
    }

    public List<ClusterGroup> getClusterGroups(String ncaId, InstanceUsageTypeEnum instanceUsage) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        return clusterGroupCache.get(new IcmsCacheKey(ncaId, instanceUsage));
    }

    private List<ClusterGroup> fetchClusterGroups(IcmsCacheKey cacheKey) {
        log.info(MESG_FETCH_CLUSTER_GROUPS, cacheKey.ncaId(), cacheKey.instanceTypeUsage());
        var response = icmsStubService.getClusterGroups(
                cacheKey.ncaId(), cacheKey.instanceTypeUsage());

        verifyClusterGroupsResponse(cacheKey.ncaId(), response);
        Objects.requireNonNull(response);
        return response.getClusterGroups();
    }

    private static void verifyClusterGroupsResponse(
            String ncaId,
            ClusterGroupsResponse response) {
        if ((response == null) || CollectionUtils.isEmpty(response.getClusterGroups())) {
            var mesg = MESG_MISSING_CLUSTER_GROUPS.formatted(ncaId);
            log.error(mesg);
            throw new UpstreamException(mesg);
        }
    }

    private static ClusterGroup targetClusterGroup(
            String ncaId,
            String clusterGroupName,
            List<ClusterGroup> clusterGroups) {
        if (CollectionUtils.isEmpty(clusterGroups)) {
            var mesg = MESG_MISSING_CLUSTER_GROUPS.formatted(ncaId);
            log.error(mesg);
            throw new UpstreamException(mesg);
        }

        return clusterGroups.stream()
                .filter(cg -> cg.getName().equals(clusterGroupName))
                .findFirst()
                .orElseThrow(() -> {
                    var mesg = MESG_INVALID_CLUSTER_GROUP.formatted(clusterGroupName);
                    log.error(mesg);
                    return new BadRequestException(mesg);
                });
    }

    private static Gpu targetGpu(String clusterGroupName, String gpuName, List<Gpu> gpus) {
        if (CollectionUtils.isEmpty(gpus)) {
            var mesg = MESG_MISSING_GPUS.formatted(clusterGroupName);
            log.error(mesg);
            throw new UpstreamException(mesg);
        }

        return gpus.stream()
                .filter(gpu -> gpu.getName().equals(gpuName))
                .findFirst()
                .orElseThrow(() -> {
                    var mesg = MESG_INVALID_GPU.formatted(clusterGroupName, gpuName);
                    log.error(mesg);
                    return new BadRequestException(mesg);
                });
    }

    private static InstanceType defaultInstanceType(
            String clusterGroupName,
            String gpuName,
            List<InstanceType> instanceTypes) {
        if (CollectionUtils.isEmpty(instanceTypes)) {
            var mesg = MESG_MISSING_INSTANCE_TYPES.formatted(clusterGroupName, gpuName);
            log.error(mesg);
            throw new UpstreamException(mesg);
        }

        return instanceTypes.stream()
                .filter(InstanceType::isDefaultInstanceType)
                .findFirst()
                .orElseThrow(() -> {
                    var mesg = MESG_MISSING_DEFAULT_INSTANCE_TYPE
                            .formatted(clusterGroupName, gpuName);
                    log.error(mesg);
                    return new UpstreamException(mesg);
                });
    }
}
