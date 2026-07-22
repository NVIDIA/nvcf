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

import static com.nvidia.nvct.util.NvctConstants.DEFAULT_CONTAINER_ENV;
import static com.nvidia.nvct.util.NvctConstants.MAX_BUFFER_LIMIT;
import static org.apache.commons.lang3.StringUtils.EMPTY;
import static org.apache.commons.lang3.StringUtils.isBlank;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.collect.Lists;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvct.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvct.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientIcmsProperties;
import com.nvidia.nvct.persistence.task.entity.GpuSpecUdt;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.rest.task.dto.InstanceUsageTypeEnum;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.icms.IcmsStubService.DescribeInstancesResponse;
import com.nvidia.nvct.service.icms.IcmsStubService.DescribeInstancesResponse.Instance;
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest;
import com.nvidia.nvct.service.registry.RegistryArtifactValidationService;
import com.nvidia.nvct.service.registry.RegistryCredentialService;
import com.nvidia.nvct.service.telemetry.TelemetryService;
import com.nvidia.nvct.service.token.TokenService;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils.ManagedHttpResources;
import jakarta.validation.constraints.NotNull;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Base64;
import java.util.Collection;
import java.util.Comparator;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.event.EventListener;
import org.springframework.data.util.Pair;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

@Slf4j
@Service
@RefreshScope
public class IcmsClient {

    private static final String MESG_NO_REQUEST_ID_FROM_ICMS =
            "Task id '%s': No request-id returned from ICMS";
    private static final String MESG_INSTANCE_TYPE_NOT_AVAILABLE =
            "Task id '%s': Instance-type not available for Backend '%s' GPU '%s'";
    private static final String MESG_MISSING_DELETABLE_INSTANCES =
            "Task id '{}': No deletable instances";
    private static final String MESG_MISSING_IDS_OF_EXTRA_INSTANCES =
            "Task id '{}': No instance ids to delete from full list '{}'";
    private static final String MESG_DELETING_EXTRA_INSTANCES =
            "Task id '{}': Deleting extra instances '{}' from full list '{}'";
    private static final String MESG_DELETED_EXTRA_INSTANCES =
            "Task id '{}': Deleted extra instances '{}' from full list '{}'";
    private static final String MESG_INVALID_CACHE_HANDLE =
            "Task id '%s': Empty cache handle.";
    private static final String MSEG_INSTANCE_NOT_FOUND = "Instance id '%s' not found";
    private static final String MESG_ERROR_RESPONSE_STATUS =
            "Upstream ICMS responded with status code '%d' - %s";
    private static final String MESG_FETCH_CLUSTERS =
            "Account '{}': Fetching Clusters from ICMS for instance type '{}'";
    private static final String MESG_REMOTE_CONFIG_REFRESH =
            "Remote config refresh observed: nvct.sidecars.init-container = {}";
    private static final String MESG_NO_ICMS_WORKLOAD =
            "Account '{}': No ICMS workload found for taskId '{}', treating terminate as idempotent";

    private static final int BATCH_SIZE = 32;
    public static final String CLIENT_REGISTRATION_ID = "icms";

    private final IcmsStubService icmsStubService;
    private final String selfFqdn;
    private final String globalFqdnGrpc;
    private final URI tracingUrl;
    private final AccountService accountService;
    private final String tracingAccessToken;
    private final String initContainer;
    private final String otelContainer;
    private final String utilsContainer;
    private final String essAgentContainer;
    private final String essFqdn;
    private final String otelCollectorContainer;
    private final TokenService tokenService;
    private final TelemetryService telemetryService;
    private final RegistryArtifactValidationService registryArtifactValidationService;
    private final RegistryCredentialService registryCredentialService;
    private final LoadingCache<IcmsCacheKey, List<IcmsStubService.ClusterResponse>> clustersCache;

    private record IcmsCacheKey(String ncaId, InstanceUsageTypeEnum instanceTypeUsage) {
    }

    // We could have used OAuth2AuthorizedClientManager and relied on Spring Security to
    // pick up the configuration properties using ClientRegistrationRepository directly. However,
    // the client-secret value held in the ClientRegistrationRepository does not get refreshed when
    // client-secret is rotated. Addressing these issues requires introducing a refreshable
    // ClientRegistrationRepository that wasn't clean. Instead, we will keep it simple and use
    // the tried and tested approach of using @Value and @RefreshScope annotations and wire
    // things up ourselves.
    public IcmsClient(
            @Value("${nvct.icms.base-url}") String baseUrl,
            @Value("${nvct.sidecars.tracing-key}") String tracingAccessToken,
            @Value("${nvct.sidecars.init-container}") String initContainer,
            @Value("${nvct.sidecars.otel-container}") String otelContainer,
            @Value("${nvct.sidecars.utils-container}") String utilsContainer,
            @Value("${nvct.sidecars.ess-agent-container}") String essAgentContainer,
            @Value("${nvct.ess.base-url}") String essFqdn,
            @Value("${nvct.sidecars.otel-collector-container}") String otelCollectorContainer,
            @Value("${nvct.fqdn}") String selfFqdn,
            @Value("${nvct.global-fqdn-grpc}") String globalFqdnGrpc,
            @Value("${spring.security.oauth2.client.registration.icms.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.icms.client-secret}")
            String clientSecret,
            @Value("${spring.security.oauth2.client.registration.icms.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.icms.token-uri}") String tokenUri,
            @Value("${management.opentelemetry.tracing.export.otlp.endpoint}") String otlpTracingEndpoint,
            AccountService accountService,
            TokenService tokenService,
            TelemetryService telemetryService,
            RegistryArtifactValidationService registryArtifactValidationService,
            RegistryCredentialService registryCredentialService,
            Optional<StaticClientIcmsProperties> staticClientIcmsProperties,
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            ManagedHttpResources icmsHttpResources) {
        this.tracingAccessToken = tracingAccessToken;
        this.initContainer = initContainer;
        this.otelContainer = otelContainer;
        this.utilsContainer = utilsContainer;
        this.essAgentContainer = essAgentContainer;
        this.essFqdn = essFqdn;
        this.otelCollectorContainer = otelCollectorContainer;
        this.selfFqdn = selfFqdn;
        this.globalFqdnGrpc = globalFqdnGrpc;
        this.accountService = accountService;
        this.tokenService = tokenService;
        this.telemetryService = telemetryService;
        this.registryArtifactValidationService = registryArtifactValidationService;
        this.registryCredentialService = registryCredentialService;

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
        this.tracingUrl = URI.create(otlpTracingEndpoint);
        this.clustersCache = Caffeine.newBuilder()
                .maximumSize(64)
                .expireAfterWrite(Duration.ofMinutes(15))
                .scheduler(Scheduler.systemScheduler())
                .build(this::fetchClusters);
    }

    // Temporary verification hook for NVCF-10266 remote-config rollout (v2).
    // Remove after sign-off — the actuator env source check is the durable contract.
    @EventListener(RefreshScopeRefreshedEvent.class)
    public void logRemoteConfigRefresh() {
        log.info(MESG_REMOTE_CONFIG_REFRESH, initContainer);
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

    /**
     * @param task the task that the instance being created will run
     * @return list of ICMS request ids and count of instances associated with that request
     */
    public UUID createInstance(
            TaskEntity task,
            GpuSpecUdt gpuSpec,
            String artifactCacheHandle,
            @NotNull Long artifactSize) {
        if (StringUtils.isBlank(artifactCacheHandle)) {
            var mesg = MESG_INVALID_CACHE_HANDLE.formatted(task.getTaskId());
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }
        var telemetries = base64EncodeTelemetryDetails(task);
        var env = getEnvironment(task);
        var policy = getHelmValidationPolicy(task);
        return scheduleSingleInstanceType(task,
                                          env,
                                          gpuSpec,
                                          artifactCacheHandle,
                                          artifactSize,
                                          telemetries,
                                          policy);
    }

    public List<IcmsStubService.Instance> getInstancesByTaskId(
            String ncaId,
            UUID deploymentId) {
        try {
            var response = icmsStubService.terminateInstancesByTaskId(
                    ncaId, deploymentId, false, true, false);
            if (response == null || response.getInstances() == null) {
                return List.of();
            }
            return response.getInstances();
        } catch (NotFoundException ex) {
            return List.of();
        }
    }

    // Includes terminated instances and instances whose acknowledgement has expired.
    public List<IcmsStubService.Instance> getAllInstancesByTaskId(
            String ncaId,
            UUID deploymentId) {
        try {
            var response = icmsStubService.terminateInstancesByTaskId(
                    ncaId, deploymentId, true, true, true);
            if (response == null || response.getInstances() == null) {
                return List.of();
            }
            return response.getInstances();
        } catch (NotFoundException ex) {
            return List.of();
        }
    }

    public List<IcmsStubService.ClusterResponse> getClusters(
            String ncaId, InstanceUsageTypeEnum usageType) {
        return clustersCache.get(new IcmsCacheKey(ncaId, usageType));
    }

    public void deleteInstances(List<String> instanceIds) {
        Lists.partition(instanceIds, BATCH_SIZE)
                .stream()
                .forEach(this::deleteInstancesUnBatched);
    }

    public void deleteExtraInstances(
            TaskEntity task, int extraInstancesToDelete,
            List<InstanceRequest> deletableInstances) {
        var taskId = task.getTaskId();

        if (CollectionUtils.isEmpty(deletableInstances)) {
            log.warn(MESG_MISSING_DELETABLE_INSTANCES, taskId);
            return;
        }

        var allDeletableInstanceIds = deletableInstances.stream()
                .filter(instance -> instance.getInstanceId() != null)
                .map(InstanceRequest::getInstanceId)
                .toList();

        // Target oldest instances for deletion. All the instances will be in either "running"
        // or "starting" state.
        var targetInstanceIds = deletableInstances.stream()
                .filter(instance -> instance.getInstanceId() != null)
                .sorted(Comparator.comparing(InstanceRequest::getCreateTime))
                .limit(extraInstancesToDelete)
                .map(InstanceRequest::getInstanceId)
                .toList();
        if (CollectionUtils.isEmpty(targetInstanceIds)) {
            log.warn(MESG_MISSING_IDS_OF_EXTRA_INSTANCES, taskId,
                     allDeletableInstanceIds);
            return;
        }
        log.info(MESG_DELETING_EXTRA_INSTANCES, taskId, targetInstanceIds,
                 allDeletableInstanceIds);
        deleteInstances(targetInstanceIds);
        log.info(MESG_DELETED_EXTRA_INSTANCES, taskId, targetInstanceIds,
                 allDeletableInstanceIds);
    }

    public void terminateInstanceByTaskId(String ncaId, UUID taskId) {
        try {
            icmsStubService.terminateInstancesByTaskId(ncaId, taskId);
        } catch (NotFoundException ex) {
            log.info(MESG_NO_ICMS_WORKLOAD, ncaId, taskId);
        }
    }

    public Instance getInstanceById(String instanceId) {
        var response = icmsStubService.describeInstances(Set.of(instanceId));
        return Optional.ofNullable(response)
                .map(DescribeInstancesResponse::getInstances)
                .stream()
                .flatMap(Collection::stream)
                .filter(instance -> instance.getInstanceId().equals(instanceId))
                .findFirst()
                .orElseThrow(
                        () -> new NotFoundException(MSEG_INSTANCE_NOT_FOUND.formatted(instanceId)));
    }

    private List<IcmsStubService.ClusterResponse> fetchClusters(IcmsCacheKey cacheKey) {
        log.info(MESG_FETCH_CLUSTERS, cacheKey.ncaId(), cacheKey.instanceTypeUsage());
        return icmsStubService.getClusters(cacheKey.ncaId(), cacheKey.instanceTypeUsage());
    }

    private UUID scheduleSingleInstanceType(
            TaskEntity task,
            String env,
            GpuSpecUdt gpuSpec,
            String cacheHandle,
            @NonNull Long cacheSize,
            String telemetries,
            String helmValidationPolicy) {
        var taskId = task.getTaskId();
        var taskName = task.getName();
        var backend = gpuSpec.getBackend();
        var clusters = gpuSpec.getClusters();
        var ncaId = task.getNcaId();
        var gpu = gpuSpec.getGpu();
        var instanceType = gpuSpec.getInstanceType();
        if (isBlank(instanceType)) {
            var mesg = MESG_INSTANCE_TYPE_NOT_AVAILABLE.formatted(taskId, backend, gpu);
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        var ownerNcaId = task.getNcaId();
        var maxRuntimeDuration = getMaxRuntimeDuration(task);
        var maxQueuedDuration = task.getMaxQueuedDuration().toString();
        var terminationGracePeriodDuration =
                task.getTerminalGracePeriodDuration().toString();
        var resultHandlingStrategy = task.getResultHandlingStrategy().toString();
        var instanceCount = 1;
        var configuration = isNotBlank(task.getHelmChart()) ? getConfiguration(gpuSpec) : null;
        var response = icmsStubService.createInstance(backend,
                                                      clusters,
                                                      gpu,
                                                      instanceType,
                                                      ncaId,
                                                      accountService.getAccountName(ncaId),
                                                      instanceCount,
                                                      getTaskContainer(task),
                                                      getHelmChart(task),
                                                      configuration,
                                                      env,
                                                      null,
                                                      cacheSize != 0,
                                                      cacheHandle,
                                                      cacheSize != 0 ? cacheSize : null,
                                                      ownerNcaId,
                                                      taskId,
                                                      taskName,
                                                      maxRuntimeDuration,
                                                      maxQueuedDuration,
                                                      terminationGracePeriodDuration,
                                                      resultHandlingStrategy,
                                                      telemetries,
                                                         // taskId will play as deploymentId and
                                                         // gpuSpecId
                                                      taskId,
                                                      taskId,
                                                      helmValidationPolicy);
        return Optional.of(response)
                .map(IcmsStubService.CreateInstancesResponse::getRequestId)
                .orElseThrow(() -> new UpstreamException(MESG_NO_REQUEST_ID_FROM_ICMS
                                                                 .formatted(taskId)));
    }

    private String getEnvironment(TaskEntity task) {
        var taskId = task.getTaskId();
        var ncaId = task.getNcaId();
        var nvctWorkerToken = tokenService.issueWorkerAccessAssertion(ncaId, taskId);
        var containerArgs = task.getContainerArgs();
        var args = isNotBlank(containerArgs) ? containerArgs : StringUtils.EMPTY;
        var containerEnv = task.getContainerEnvironment();
        var cenv = isNotBlank(containerEnv) ? containerEnv : DEFAULT_CONTAINER_ENV;
        var terminalGracePeriodDuration = task.getTerminalGracePeriodDuration();
        var taskSecretsPresent = task.hasSecrets();
        var secretsAssertionToken = tokenService.issueSecretsAssertion(task);
        var sidecarRegistryCredentialEncoded = registryCredentialService
                .getBase64EncodedSidecarRegistryImagePullSecret(task);
        var containerRegistryCredentialsEncoded =
                validateAndGetContainerRegistryImagePullSecrets(task);
        var helmRegistryCredentialsEncoded =
                validateAndGetHelmRegistryImagePullSecrets(task);
        var env = Stream.of(
                        Pair.of("NCA_ID", task.getNcaId()),
                        Pair.of("ACCOUNT_NAME", accountService.getAccountName(ncaId)),
                        Pair.of("NVCT_WORKER_TOKEN", nvctWorkerToken),
                        Pair.of("NVCT_FQDN", selfFqdn),
                        Pair.of("NVCT_FQDN_GRPC", globalFqdnGrpc),
                        Pair.of("INIT_CONTAINER", initContainer),
                        Pair.of("OTEL_CONTAINER", otelContainer),
                        Pair.of("UTILS_CONTAINER", utilsContainer),
                        Pair.of("ESS_AGENT_CONTAINER", essAgentContainer),
                        Pair.of("ESS_FQDN", essFqdn),
                        Pair.of("OTEL_EXPORTER_OTLP_ENDPOINT", tracingUrl.toString()),
                        Pair.of("TASK_TAGS", getTaskTags(task)),
                        Pair.of("TASK_ID", task.getTaskId()),
                        Pair.of("TASK_NAME", task.getName()),
                        Pair.of("TASK_CONTAINER", getTaskContainer(task)),
                        Pair.of("TASK_CONTAINER_ARGS", args),
                        Pair.of("TASK_CONTAINER_ENV", cenv),
                        Pair.of("TERMINATION_GRACE_PERIOD", terminalGracePeriodDuration),
                        Pair.of("RESULTS_LOCATION", getResultsLocation(task)),
                        Pair.of("TRACING_ACCESS_TOKEN", tracingAccessToken),
                        Pair.of("BYOO_OTEL_COLLECTOR_CONTAINER", otelCollectorContainer),
                        Pair.of("TASK_SECRETS_PRESENT", taskSecretsPresent),
                        Pair.of("CONTAINER_REGISTRIES_CREDENTIALS", containerRegistryCredentialsEncoded),
                        Pair.of("HELM_REGISTRIES_CREDENTIALS", helmRegistryCredentialsEncoded),
                        Pair.of("SECRETS_ASSERTION_TOKEN", secretsAssertionToken),
                        Pair.of("SIDECAR_REGISTRY_CREDENTIAL", sidecarRegistryCredentialEncoded))
                .map(pair -> pair.getFirst() + "=" + pair.getSecond())
                .collect(Collectors.joining("\n"));
        return Base64.getEncoder().encodeToString(env.getBytes(StandardCharsets.UTF_8));
    }


    private static String getTaskTags(TaskEntity task) {
        var tags = task.getTags();
        if (tags == null || tags.isEmpty()) {
            return "";
        }
        return String.join(",", tags);
    }

    private String getTaskContainer(TaskEntity task) {
        var containerImage = task.getContainerImage();
        return isBlank(containerImage) ? EMPTY : containerImage;
    }

    private String getHelmChart(TaskEntity task) {
        var helmChart = task.getHelmChart();
        return isBlank(helmChart) ? EMPTY : helmChart;
    }

    private String getConfiguration(GpuSpecUdt spec) {
        var configuration = spec.getConfiguration();
        return isBlank(configuration) ? EMPTY : configuration;
    }

    private String getResultsLocation(TaskEntity task) {
        var resultsLocation = task.getResultsLocation();
        return isBlank(resultsLocation) ? EMPTY : resultsLocation;
    }

    private String getMaxRuntimeDuration(TaskEntity task) {
        var maxRuntimeDuration = task.getMaxRuntimeDuration();
        return (maxRuntimeDuration != null) ? maxRuntimeDuration.toString() : EMPTY;
    }

    private void deleteInstancesUnBatched(List<String> instanceIds) {
        if (instanceIds.isEmpty()) {
            return;
        }
        icmsStubService.deleteInstances(instanceIds);
    }

    private String base64EncodeTelemetryDetails(TaskEntity entity) {
        var telemetriesUdt = entity.getTelemetries();
        if (telemetriesUdt == null) {
            return StringUtils.EMPTY;
        }

        var ncaId = entity.getNcaId();
        return telemetryService.base64Encode(ncaId, telemetriesUdt);
    }

    private String validateAndGetContainerRegistryImagePullSecrets(TaskEntity task) {
        registryArtifactValidationService.validateContainerRegistryCredentialsExist(task);
        return registryCredentialService
                .getBase64EncodedContainerRegistryImagePullSecrets(task);
    }

    private String validateAndGetHelmRegistryImagePullSecrets(TaskEntity task) {
        registryArtifactValidationService.validateHelmRegistryCredentialsExist(task);
        return registryCredentialService
                .getBase64EncodedHelmRegistryImagePullSecrets(task);
    }

    private static String getHelmValidationPolicy(TaskEntity task) {
        if (task.getGpuSpec() != null) {
            var policy = task.getGpuSpec().getHelmValidationPolicy();
            if (StringUtils.isNotBlank(policy)) {
                return Base64.getEncoder().encodeToString(policy.getBytes(StandardCharsets.UTF_8));
            }
        }

        return null;
    }
}
