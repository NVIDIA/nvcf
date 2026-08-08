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
package com.nvidia.nvcf.icms.client;

import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_CONTAINER_ENV;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_ENDPOINT;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_PORT;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_PROTOCOL;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_TIMEOUT;
import static java.util.Objects.requireNonNullElse;
import static org.apache.commons.lang3.StringUtils.defaultIfBlank;
import static org.apache.commons.lang3.StringUtils.isBlank;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.google.common.collect.Lists;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.registries.service.registry.client.ngc.NgcRegistryUtils;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientIcmsProperties;
import com.nvidia.nvcf.icms.client.IcmsStubService.ClusterGroupsResponse.ClusterGroup;
import com.nvidia.nvcf.icms.client.IcmsStubService.ClusterGroupsResponse.ClusterGroup.Gpu;
import com.nvidia.nvcf.icms.client.IcmsStubService.ClusterGroupsResponse.ClusterGroup.Gpu.InstanceType;
import com.nvidia.nvcf.icms.client.IcmsStubService.DescribeInstancesResponse;
import com.nvidia.nvcf.icms.client.IcmsStubService.Instance;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.Protocol;
import com.nvidia.nvcf.rest.function.deployment.dto.InstanceUsageTypeEnum;
import com.nvidia.nvcf.rest.registry.dto.ProvisionedByEnum;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import com.nvidia.nvcf.service.registry.RegistryArtifactValidationService;
import com.nvidia.nvcf.service.registry.RegistryCredentialEssService;
import com.nvidia.nvcf.service.registry.RegistryCredentialFunctionService;
import com.nvidia.nvcf.service.registry.RegistryCredentialLookupService;
import com.nvidia.nvcf.service.telemetry.TelemetryService;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils.ManagedHttpResources;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Positive;
import jakarta.validation.constraints.PositiveOrZero;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Base64;
import java.util.Collection;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
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
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
@RefreshScope
public class IcmsClient {
    public static final int BATCH_SIZE = 32;
    public static final String CLIENT_REGISTRATION_ID = "icms";

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
            "Account '%s': Missing cluster-groups in successful ICMS response";
    private static final String MESG_MISSING_INSTANCE_TYPES_RESPONSE =
            "Account '%s': Missing instance-types response from ICMS";
    private static final String MESG_MISSING_CLUSTERS_RESPONSE =
            "Account '%s': Missing clusters response from ICMS";
    private static final String MESG_NO_REQUEST_ID_FROM_ICMS =
            "Function id '%s', version '%s': No request-id returned from ICMS";
    private static final String MESG_INSTANCE_TYPE_NOT_AVAILABLE =
            "Function id '%s', version '%s': Instance-type not available for Backend '%s' GPU '%s'";
    private static final String MSEG_INSTANCE_NOT_FOUND = "Instance id '%s' not found";
    private static final String MSEG_DEPLOYMENT_NOT_FOUND =
            "Instances for nca id '%s' and deployment '%s' not found";
    private static final String MESG_DEFAULT_INSTANCE_TYPE_DETAILS =
            "ClusterGroup: '{}', GPU: '{}', Default InstanceType: '{}'";
    private static final String MESG_FETCH_CLUSTER_GROUPS =
            "Account '{}': Fetching Cluster Groups from ICMS";
    private static final String MESG_FETCH_INSTANCE_TYPES =
            "Account '{}': Fetching Instance Types from ICMS";
    private static final String MESG_FETCH_CLUSTERS =
            "Account '{}': Fetching Clusters from ICMS";
    private static final String MESG_REMOTE_CONFIG_REFRESH =
            "Remote config refresh observed: nvcf.sidecars.init-container = %s";

    private static final int MAX_BUFFER_LIMIT = 10 * 1024 * 1024; // 10 MB
    private static final int DEFAULT_INFERENCE_PORT = 8000;
    private static final String DEFAULT_HELM_CHART_SERVICE_NAME = "ENTRYPOINT";
    private static final URI DUMMY_ARTIFACT_URI = URI.create("dummy");
    private static final String MSG_NO_INSTANCES_FOUND =
            "Account '{}', deploymentId '{}': no instances found in ICMS; " +
                    "returning empty list";

    private final LoadingCache<IcmsCacheKey, List<ClusterGroup>> clusterGroupCache;
    private final LoadingCache<IcmsCacheKey, Map<String, Set<IcmsStubService.InstanceTypeDetails>>>
            instanceTypesCache;
    private final LoadingCache<String, List<IcmsStubService.ClusterResponse>>
            ncaIdToClusterResponseCache;
    private final IcmsStubService service;
    private final String selfFqdn;
    private final String globalFqdnGrpc;
    private final URI tracingUrl;
    private final String tracingAccessToken;
    private final Optional<String> workerNKeySeed;
    private final String inferenceContainer;
    private final String initContainer;
    private final String otelContainer;
    private final String goUtilsContainer;
    private final String nicllsContainer;
    private final String essAgentContainer;
    private final String otelCollectorContainer;
    private final String llmCredentialManagerImage;
    private final String llmRouterClientImage;
    private final String llmRequestRouterAddress;
    private final JsonMapper jsonMapper;
    private final GrpcTokenService grpcTokenService;
    private final FunctionMapperService functionMapperService;
    private final NatsProperties natsProperties;
    private final TelemetryService telemetryService;
    private final RegistryArtifactValidationService registryArtifactValidationService;
    private final RegistryCredentialEssService registryCredentialEssService;
    private final RegistryCredentialLookupService registryCredentialLookupService;
    private final RegistryCredentialFunctionService registryCredentialFunctionService;
    private final boolean sendLegacyCredProps;
    private final String sidecarImagePullSecret;  // Base64 encoded in username:password format
    private final String essFqdn;

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
            @Value("${nvcf.icms.base-url}") String baseUrl,
            @Value("${spring.security.oauth2.client.registration.icms.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.icms.client-secret}") String clientSecret,
            @Value("${spring.security.oauth2.client.registration.icms.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.icms.token-uri}") String tokenUri,
            @Value("${nvcf.sidecars.tracing-key}") String tracingAccessToken,
            @Value("${kv.worker-nkey-seed:#{null}}") Optional<String> workerNKeySeed,
            @Value("${nvcf.sidecars.inference-container}") String inferenceContainer,
            @Value("${nvcf.sidecars.init-container}") String initContainer,
            @Value("${nvcf.sidecars.otel-container}") String otelContainer,
            @Value("${nvcf.sidecars.utils-container-image.go}") String goUtilsContainer,
            @Value("${nvcf.sidecars.niclls-container}") String nicllsContainer,
            @Value("${nvcf.sidecars.ess-agent-container}") String essAgentContainer,
            @Value("${nvcf.sidecars.otel-collector-container}") String otelCollectorContainer,
            @Value("${nvcf.sidecars.llm-credential-manager-image}") String llmCredentialManagerImage,
            @Value("${nvcf.sidecars.llm-router-client-image}") String llmRouterClientImage,
            @Value("${nvcf.llm-request-router.worker-address}") String llmRequestRouterAddress,
            @Value("${nvcf.fqdn}") String selfFqdn,
            @Value("${nvcf.global-fqdn-grpc}") String globalFqdnGrpc,
            @Value("${management.opentelemetry.tracing.export.otlp.endpoint}") URI tracingUrl,
            // ESS_FQDN advertised to the LLM worker is the worker-facing ESS address,
            // which differs from EssClient's in-cluster base-url when the compute plane
            // is external. Defaults to nvcf.ess.base-url (colocated); split deployments
            // override nvcf.ess.worker-base-url with the public gateway address.
            @Value("${nvcf.ess.worker-base-url:${nvcf.ess.base-url}}") String essFqdn,
            @Value("${nvcf.registries.send-legacy-credential-props:false}")
            boolean sendLegacyCredProps,
            @Value("${nvcf.sidecars.image-pull-secret}") String sidecarImagePullSecret,
            Optional<StaticClientIcmsProperties> staticClientIcmsProperties,
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            ManagedHttpResources icmsHttpResources,
            JsonMapper jsonMapper,
            GrpcTokenService grpcTokenService,
            FunctionMapperService functionMapperService,
            NatsProperties natsProperties,
            TelemetryService telemetryService,
            RegistryArtifactValidationService registryArtifactValidationService,
            RegistryCredentialEssService registryCredentialEssService,
            RegistryCredentialLookupService registryCredentialLookupService,
            RegistryCredentialFunctionService registryCredentialFunctionService) {
        this.tracingAccessToken = tracingAccessToken;
        this.workerNKeySeed = workerNKeySeed;
        this.inferenceContainer = inferenceContainer;
        this.initContainer = initContainer;
        this.otelContainer = otelContainer;
        this.goUtilsContainer = goUtilsContainer;
        this.nicllsContainer = nicllsContainer;
        this.essAgentContainer = essAgentContainer;
        this.otelCollectorContainer = otelCollectorContainer;
        this.llmCredentialManagerImage = llmCredentialManagerImage;
        this.llmRouterClientImage = llmRouterClientImage;
        this.llmRequestRouterAddress = llmRequestRouterAddress;
        this.selfFqdn = selfFqdn;
        this.globalFqdnGrpc = globalFqdnGrpc;
        this.jsonMapper = jsonMapper;
        this.grpcTokenService = grpcTokenService;
        this.functionMapperService = functionMapperService;
        this.telemetryService = telemetryService;
        this.essFqdn = essFqdn;
        this.sendLegacyCredProps = sendLegacyCredProps;
        this.sidecarImagePullSecret = sidecarImagePullSecret;

        var webClient = webClientBuilder
                .baseUrl(baseUrl)
                .clientConnector(icmsHttpResources.connector())
                .codecs(configurer -> configurer.defaultCodecs()
                        .maxInMemorySize(MAX_BUFFER_LIMIT))
                .filter(NvcfOAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(oauthFilter(staticClientIcmsProperties, webClientBuilder,
                                    clientId, clientSecret, scope, tokenUri))
                .filter(NvcfOAuth2ClientUtils.getResponseFilterProcessor("ICMS"))
                .build();
        this.service = HttpServiceProxyFactory
                .builderFor(WebClientAdapter.create(webClient))
                .build()
                .createClient(IcmsStubService.class);
        this.tracingUrl = tracingUrl;
        this.clusterGroupCache = Caffeine.newBuilder()
                .maximumSize(64)
                .expireAfterWrite(Duration.ofMinutes(15))
                .scheduler(Scheduler.systemScheduler())
                .build(this::fetchClusterGroups);
        this.ncaIdToClusterResponseCache = Caffeine.newBuilder()
                .maximumSize(64)
                .expireAfterWrite(Duration.ofMinutes(15))
                .scheduler(Scheduler.systemScheduler())
                .build(this::fetchClustersByNcaId);
        this.instanceTypesCache = Caffeine.newBuilder()
                .maximumSize(128)
                .expireAfterWrite(Duration.ofHours(1))
                .scheduler(Scheduler.systemScheduler())
                .build(this::fetchInstanceTypes);
        this.natsProperties = natsProperties;
        this.registryArtifactValidationService = registryArtifactValidationService;
        this.registryCredentialEssService = registryCredentialEssService;
        this.registryCredentialLookupService = registryCredentialLookupService;
        this.registryCredentialFunctionService = registryCredentialFunctionService;
    }

    // Temporary verification hook; remove after remote config support is complete.
    @EventListener(RefreshScopeRefreshedEvent.class)
    public void logRemoteConfigRefresh() {
        log.info(MESG_REMOTE_CONFIG_REFRESH.formatted(initContainer));
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
                .orElseGet(() -> NvcfOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    /**
     * @param function the function that the instance being created will run
     * @return list of ICMS request ids and count of instances associated with that request
     */
    public UUID createInstance(
            FunctionEntity function,
            UUID deploymentId,
            GpuSpecificationEntity gpuSpec,
            @Positive int count,
            @PositiveOrZero long artifactSize,
            String cacheHandle,
            String secretsAssertionToken) {
        var telemetries = base64EncodeTelemetryDetails(function);
        var env = getEnvironment(function, gpuSpec, secretsAssertionToken);
        var helmValidationPolicy = getHelmValidationPolicy(gpuSpec);
        return scheduleSingleInstanceType(function, deploymentId, env, count, gpuSpec,
                                          artifactSize, telemetries, cacheHandle,
                                          helmValidationPolicy);
    }

    private String base64EncodeTelemetryDetails(FunctionEntity function) {
        var telemetriesUdt = function.getTelemetries();
        if (telemetriesUdt == null) {
            return StringUtils.EMPTY;
        }

        var ncaId = function.getNcaId();
        return telemetryService.base64Encode(ncaId, telemetriesUdt);
    }

    private UUID scheduleSingleInstanceType(
            FunctionEntity function,
            UUID deploymentId,
            String env,
            int count,
            GpuSpecificationEntity gpuSpec,
            long cacheSize,
            String telemetries,
            String cacheHandle,
            String helmValidationPolicy) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var functionName = function.getFunctionName();
        var backend = gpuSpec.getBackend();
        var ncaId = function.getNcaId();
        var gpu = gpuSpec.getGpu();
        var instanceType = gpuSpec.getInstanceType();
        var helmChart = function.getHelmChart();
        var ownerNcaId = function.getNcaId();
        var clusters = gpuSpec.getClusters();
        var regions = gpuSpec.getRegions();
        var attributes = gpuSpec.getAttributes();
        var gpuSpecificationId = gpuSpec.getKey().getGpuSpecificationId();

        if (isBlank(instanceType)) {
            var mesg = MESG_INSTANCE_TYPE_NOT_AVAILABLE
                    .formatted(functionId, versionId, backend, gpu);
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        var availabilityZones = gpuSpec.getAvailabilityZones();
        String rawAvailabilityZones = null;
        if ((availabilityZones != null) && !availabilityZones.isEmpty()) {
            rawAvailabilityZones = String.join(",", availabilityZones);
        }
        URI artifactUrl = null;
        if (cacheSize != 0) {
            artifactUrl = DUMMY_ARTIFACT_URI;
        }

        // cofiguration should be provided only for helm functions,
        // for container-based it should be null
        var configuration = isNotBlank(function.getHelmChart()) ? gpuSpec.getConfiguration() : null;
        var models = getFunctionModels(function);
        var response = service.createInstance(backend, gpu, instanceType, ncaId,
                                              rawAvailabilityZones, count,
                                              getInferenceContainer(function), helmChart, env,
                                              models,
                                              artifactUrl,
                                              cacheSize != 0,
                                              cacheHandle,
                                              configuration,
                                              cacheSize != 0 ? cacheSize : null,
                                              deploymentId,
                                              gpuSpecificationId,
                                              functionId,
                                              versionId,
                                              ownerNcaId,
                                              functionName,
                                              clusters,
                                              regions,
                                              attributes,
                                              function.getFunctionType(),
                                              telemetries,
                                              helmValidationPolicy);
        if (response == null || response.getRequestId() == null) {
            throw new UpstreamException(
                    MESG_NO_REQUEST_ID_FROM_ICMS.formatted(functionId, versionId));
        }
        return response.getRequestId();
    }

    private String getEnvironment(
            FunctionEntity function,
            GpuSpecificationEntity gpuSpec,
            String secretsAssertionToken) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var nvcfWorkerToken = grpcTokenService.issueToken(functionId, versionId, TokenType.WORKER);
        var containerArgs = function.getContainerArgs();
        var args = isNotBlank(containerArgs) ? containerArgs : StringUtils.EMPTY;
        var containerEnv = function.getContainerEnvironment();
        var cenv = isNotBlank(containerEnv) ? containerEnv : DEFAULT_CONTAINER_ENV;
        var inferencePort = function.getInferencePort() != null ?
                function.getInferencePort() : DEFAULT_INFERENCE_PORT;
        var maxRequestConcurrency = requireNonNullElse(gpuSpec.getMaxRequestConcurrency(), 1);
        var helmChartServiceName = getHelmChartServiceName(function);
        var functionSecretsPresent = function.hasSecrets();
        var containerRegistryCredentialsEncoded =
                validateAndGetContainerRegistryImagePullSecrets(function);
        var helmRegistryCredentialsEncoded =
                validateAndGetHelmRegistryImagePullSecrets(function);
        var sidecarRegistryCredentialEncoded = registryCredentialFunctionService
                .getBase64EncodedSidecarRegistryImagePullSecret(function);
        var envStream = Stream.of(
                Pair.of("NVCF_WORKER_TOKEN", nvcfWorkerToken),
                Pair.of("NVCF_FQDN", selfFqdn),
                Pair.of("NVCF_FQDN_GRPC", globalFqdnGrpc),
                Pair.of("NVCF_FQDN_NATS", natsProperties.getWorkerUrl()),
                Pair.of("INFERENCE_CONTAINER", getInferenceContainer(function)),
                Pair.of("INFERENCE_CONTAINER_ARGS", args),
                Pair.of("INFERENCE_CONTAINER_ENV", cenv),
                Pair.of("INFERENCE_HEALTH_ENDPOINT", getHealthUri(function)),
                Pair.of("INFERENCE_HEALTH_PROTOCOL", getHealthProtocol(function)),
                Pair.of("INFERENCE_HEALTH_TIMEOUT", getHealthTimeout(function)),
                Pair.of("INFERENCE_HEALTH_PORT", getHealthPort(function)),
                Pair.of("INFERENCE_HEALTH_EXPECTED_RESPONSE_CODE",
                        getHealthExpectedCode(function)),
                Pair.of("INFERENCE_URL", function.getInferenceUrl()),
                Pair.of("INFERENCE_PORT", inferencePort),
                Pair.of("INFERENCE_PROTOCOL", getInferenceProtocol(function)),
                Pair.of("OTEL_EXPORTER_OTLP_ENDPOINT", tracingUrl.toString()),
                Pair.of("TRACING_ACCESS_TOKEN", tracingAccessToken),
                Pair.of("INIT_CONTAINER", initContainer),
                Pair.of("OTEL_CONTAINER", otelContainer),
                Pair.of("UTILS_CONTAINER", getUtilsContainer(function)),
                Pair.of("NCA_ID", function.getNcaId()),
                Pair.of("FUNCTION_ID", function.getFunctionId()),
                Pair.of("FUNCTION_VERSION_ID", function.getFunctionVersionId()),
                Pair.of("FUNCTION_NAME", function.getFunctionName()),
                Pair.of("FUNCTION_TAGS", getFunctionTags(function)),
                Pair.of("MAX_REQUEST_CONCURRENCY", maxRequestConcurrency),
                Pair.of("HELM_CHART_INFERENCE_SERVICE_NAME", helmChartServiceName),
                Pair.of("SECRETS_ASSERTION_TOKEN", secretsAssertionToken),
                Pair.of("ESS_AGENT_CONTAINER", essAgentContainer),
                Pair.of("BYOO_OTEL_COLLECTOR_CONTAINER", otelCollectorContainer),
                Pair.of("FUNCTION_SECRETS_PRESENT", functionSecretsPresent),
                Pair.of("CONTAINER_REGISTRIES_CREDENTIALS", containerRegistryCredentialsEncoded),
                Pair.of("HELM_REGISTRIES_CREDENTIALS", helmRegistryCredentialsEncoded),
                Pair.of("SIDECAR_REGISTRY_CREDENTIAL", sidecarRegistryCredentialEncoded),
                Pair.of("ESS_FQDN", essFqdn));
        if (function.getFunctionType() == FunctionType.LLM) {
            envStream = Stream.concat(envStream, Stream.of(
                    Pair.of("LLM_CREDENTIAL_MANAGER_IMAGE", llmCredentialManagerImage),
                    Pair.of("LLM_ROUTER_CLIENT_IMAGE", llmRouterClientImage),
                    Pair.of("LLM_REQUEST_ROUTER_ADDRESS", llmRequestRouterAddress)));
        }
        if (function.getFunctionType() == FunctionType.STREAMING) {
            envStream = Stream.concat(envStream,
                                      Stream.of(Pair.of("NICLLS_CONTAINER", nicllsContainer)));
        }
        if (workerNKeySeed.isPresent()) { // using isPresent because envStream is a non-final var
            envStream = Stream.concat(envStream,
                                      Stream.of(Pair.of("NKEY_SEED", workerNKeySeed.get())));
        }

        // ### Keep the option to send legacy properties in case we need to troubleshoot.
        //     By default, sendLegacyCredProps is false. When sending legacy properties, we are
        //     no longer using the cred values from the DB. Instead, we are using creds from
        //     ESS and Vault as values of the legacy properties. This allows us the ability to
        //     keep moving forward without being held hostage due to issues on the Worker side.
        if (sendLegacyCredProps) {
            var containerRegistryCred = getSystemContainerRegistrySecret(function.getNcaId());
            var sidecarRegistryCredential = NgcRegistryUtils.getApiKey(sidecarImagePullSecret);
            var legacyCredPropsStream =
                    Stream.of(Pair.of("INFERENCE_CONTAINER_CREDENTIAL", containerRegistryCred),
                              Pair.of("SIDECAR_CREDENTIAL", sidecarRegistryCredential));
            envStream = Stream.concat(envStream, legacyCredPropsStream);
        }

        var env = envStream
                .map(pair -> pair.getFirst() + "=" + pair.getSecond())
                .collect(Collectors.joining("\n"));
        return Base64.getEncoder().encodeToString(env.getBytes(StandardCharsets.UTF_8));
    }

    private String getFunctionModels(FunctionEntity function) {
        try {
            var modelsJson = jsonMapper.writeValueAsString(
                    functionMapperService.toFunctionModels(function.getModelSpecs()));
            return Base64.getEncoder().encodeToString(modelsJson.getBytes(StandardCharsets.UTF_8));
        } catch (JacksonException jex) {
            throw new IllegalStateException("Failed to serialize function models for ICMS", jex);
        }
    }

    private String getSystemContainerRegistrySecret(String ncaId) {
        var systemContainerRegistryCred = registryCredentialLookupService
                .getRegistryCredentialDtos(ncaId,
                                           Set.of(ArtifactTypeEnum.CONTAINER),
                                           Set.of(ProvisionedByEnum.SYSTEM))
                .getFirst();   // Exactly one system provisioned container reg cred should be there.
        var secretDto = registryCredentialEssService
                .getRegistryCredentialSecret(ncaId,
                                             systemContainerRegistryCred.registryCredentialId())
                .orElseThrow(() -> new IllegalStateException("Missing system container registry"));
        var hostname = systemContainerRegistryCred.registryHostname();

        if (hostname.endsWith("nvcr.io")) { // NGC
            return NgcRegistryUtils.getApiKey(secretDto.value().asString());
        }

        return new String(Base64.getDecoder().decode(secretDto.value().asString()));
    }

    // First try to get from HealthInfo.uri, if empty then DEFAULT_HEALTH_ENDPOINT.
    // There are still entries in the functions_v3 table DB with empty string in
    // health_udt's uri field.
    private static String getHealthUri(FunctionEntity function) {
        String metadataHealthUri = "";
        if (function.getHealth() != null) {
            metadataHealthUri = function.getHealth().getUri();
        }
        return defaultIfBlank(metadataHealthUri, DEFAULT_HEALTH_ENDPOINT.toString());
    }

    private static String getFunctionTags(FunctionEntity function) {
        var tags = function.getTags();
        if (tags == null || tags.isEmpty()) {
            return "";
        }
        return String.join(",", tags);
    }

    private static String getHelmChartServiceName(FunctionEntity function) {
        var helmChart = function.getHelmChart();
        var helmChartServiceName = function.getHelmChartServiceName();

        if (isNotBlank(helmChart)) {
            return defaultIfBlank(helmChartServiceName, DEFAULT_HELM_CHART_SERVICE_NAME);
        }
        return StringUtils.EMPTY;
    }

    private String getInferenceContainer(FunctionEntity function) {
        var containerImage = function.getContainerImage();
        return isNotBlank(containerImage) ? containerImage : this.inferenceContainer;
    }

    private String getUtilsContainer(FunctionEntity function) {
        var utilsContainerImage = function.getUtilsContainerImage();
        if (isBlank(utilsContainerImage) || GO.equals(utilsContainerImage)) {
            return this.goUtilsContainer;
        }
        return utilsContainerImage;
    }

    private static String getInferenceProtocol(FunctionEntity function) {
        return switch (function.getHealth().getProtocol()) {
            case GRPC -> "GRPC";
            case HTTP -> "REST";
        };
    }

    private static int getHealthPort(FunctionEntity functionEntity) {
        if (functionEntity != null && functionEntity.getHealth() != null) {
            return functionEntity.getHealth().getPort();
        } else if (functionEntity.getInferencePort() != null) {
            return functionEntity.getInferencePort();
        }
        return DEFAULT_HEALTH_PORT;
    }

    private static int getHealthExpectedCode(FunctionEntity function) {
        if (function != null && function.getHealth() != null) {
            return function.getHealth().getExpectedStatusCode();
        }
        return DEFAULT_HEALTH_EXPECTED_STATUS_CODE;
    }

    private static Duration getHealthTimeout(FunctionEntity function) {
        if (function != null && function.getHealth() != null) {
            return function.getHealth().getTimeout();
        }
        return DEFAULT_HEALTH_TIMEOUT;
    }

    private static Protocol getHealthProtocol(FunctionEntity function) {
        if (function != null && function.getHealth() != null) {
            return function.getHealth().getProtocol();
        }
        return DEFAULT_HEALTH_PROTOCOL;
    }

    public void deleteInstances(List<String> instanceIds) {
        Lists.partition(instanceIds, BATCH_SIZE).forEach(this::deleteInstancesUnBatched);
    }

    public void deleteInstance(String ncaId, String instanceId) {
        service.deleteInstance(ncaId, instanceId);
    }

    public void deleteInstancesByDeploymentId(
            String ncaId,
            UUID deploymentId) {
        service.deleteInstancesByDeploymentId(ncaId, deploymentId);
    }

    private void deleteInstancesUnBatched(List<String> instanceIds) {
        if (instanceIds.isEmpty()) {
            return;
        }
        service.deleteInstances(instanceIds);
    }

    public String getDefaultInstanceType(
            @NotBlank String ncaId,
            @NotBlank String clusterGroupName,
            @NotBlank String gpuName,
            InstanceUsageTypeEnum instanceUsage) {
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

    @VisibleForTesting
    public void clearInstanceTypesCache() {
        instanceTypesCache.invalidateAll();
    }

    public List<ClusterGroup> getClusterGroups(
            String ncaId, InstanceUsageTypeEnum instanceUsage) {
        return clusterGroupCache.get(new IcmsCacheKey(ncaId, instanceUsage));
    }

    public DescribeInstancesResponse.Instance getInstanceById(String instanceId) {
        var response = service.describeInstances(List.of(instanceId));
        return Optional.ofNullable(response)
                .map(DescribeInstancesResponse::getInstances)
                .stream()
                .flatMap(Collection::stream)
                .filter(instance -> instance.getInstanceId().equals(instanceId))
                .findFirst()
                .orElseThrow(
                        () -> new NotFoundException(MSEG_INSTANCE_NOT_FOUND.formatted(instanceId)));
    }

    public Map<String, Set<IcmsStubService.InstanceTypeDetails>> getInstanceTypes(
            String ncaId, InstanceUsageTypeEnum instanceUsage) {
        return instanceTypesCache.get(new IcmsCacheKey(ncaId, instanceUsage));
    }

    public List<IcmsStubService.ClusterResponse> getClustersByNcaId(String ncaId) {
        return ncaIdToClusterResponseCache.get(ncaId);
    }

    public List<Instance> getInstancesByDeploymentId(String ncaId, UUID deploymentId) {
        // by default, we don't need terminated and expired instances
        return getInstancesByDeploymentId(ncaId, deploymentId, false, false);
    }

    public List<Instance> getInstancesByDeploymentId(String ncaId, UUID deploymentId,
                                                     boolean includeTerminated,
                                                     boolean includeExpiredAckedInstances) {
        var response = getInstancesByDeploymentIdRemote(ncaId, deploymentId, includeTerminated,
                                                        includeExpiredAckedInstances);
        return Optional.ofNullable(response).map(IcmsStubService.Instances::getInstances)
                .orElseThrow(() -> new NotFoundException(
                        MSEG_DEPLOYMENT_NOT_FOUND.formatted(ncaId, deploymentId)));
    }

    private IcmsStubService.Instances getInstancesByDeploymentIdRemote(
            String ncaId,
            UUID deploymentId,
            boolean includeTerminated,
            boolean includeExpiredAckedInstances) {
        try {
            return service.getInstancesByDeploymentId(
                    ncaId, deploymentId, includeTerminated, true, includeExpiredAckedInstances);
        } catch (NotFoundException ex) {
            log.info(MSG_NO_INSTANCES_FOUND, ncaId, deploymentId);
            return IcmsStubService.Instances.builder().Instances(List.of()).build();
        }
    }

    private List<ClusterGroup> fetchClusterGroups(IcmsCacheKey cacheKey) {
        log.info(MESG_FETCH_CLUSTER_GROUPS, cacheKey.ncaId());
        var response = service.getClusterGroups(cacheKey.ncaId(), cacheKey.instanceTypeUsage());
        if (response == null || CollectionUtils.isEmpty(response.getClusterGroups())) {
            var mesg = MESG_MISSING_CLUSTER_GROUPS.formatted(cacheKey.ncaId());
            log.error(mesg);
            throw new UpstreamException(mesg);
        }
        return response.getClusterGroups();
    }

    private Map<String, Set<IcmsStubService.InstanceTypeDetails>> fetchInstanceTypes(
            IcmsCacheKey cacheKey) {
        log.info(MESG_FETCH_INSTANCE_TYPES, cacheKey.ncaId());
        var response = service.getInstanceTypes(cacheKey.ncaId(), cacheKey.instanceTypeUsage());
        if (response == null) {
            var mesg = MESG_MISSING_INSTANCE_TYPES_RESPONSE.formatted(cacheKey.ncaId());
            log.error(mesg);
            throw new UpstreamException(mesg);
        }
        return response;
    }

    private List<IcmsStubService.ClusterResponse> fetchClustersByNcaId(String ncaId) {
        log.info(MESG_FETCH_CLUSTERS, ncaId);
        // always query DEFAULT to pick up both single and multi instance types
        var response = service.getClusters(ncaId, InstanceUsageTypeEnum.DEFAULT);
        if (response == null) {
            var mesg = MESG_MISSING_CLUSTERS_RESPONSE.formatted(ncaId);
            log.error(mesg);
            throw new UpstreamException(mesg);
        }
        return response;
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

    private String validateAndGetContainerRegistryImagePullSecrets(FunctionEntity function) {
        registryArtifactValidationService.validateContainerRegistryCredentialsExist(function);
        return registryCredentialFunctionService
                .getBase64EncodedContainerRegistryImagePullSecrets(function);
    }

    private String validateAndGetHelmRegistryImagePullSecrets(FunctionEntity function) {
        registryArtifactValidationService.validateHelmRegistryCredentialsExist(function);
        return registryCredentialFunctionService
                .getBase64EncodedHelmRegistryImagePullSecrets(function);
    }

    private static String getHelmValidationPolicy(GpuSpecificationEntity gpuSpec) {
        var policy = gpuSpec.getHelmValidationPolicy();
        if (StringUtils.isNotBlank(policy)) {
            return Base64.getEncoder().encodeToString(policy.getBytes(StandardCharsets.UTF_8));
        }
        return null;
    }
}
