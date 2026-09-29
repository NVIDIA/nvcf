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
package com.nvidia.nvcf.service.reval;

import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvcf.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientRevalProperties;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.registry.dto.K8sSecretsDto;
import com.nvidia.nvcf.service.registry.RegistryArtifactValidationService;
import com.nvidia.nvcf.service.registry.RegistryCredentialFunctionService;
import com.nvidia.nvcf.service.reval.RevalStubService.RevalValidateRequest;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils.ManagedHttpResources;
import java.util.Optional;
import java.util.Set;
import java.util.function.Predicate;
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
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;


@Slf4j
@Service
@RefreshScope
public class RevalClient {
    public static final String CLIENT_REGISTRATION_ID = "reval";

    private static final String MESG_REVAL_ERROR =
            "Internal error during validation. Reval Status %s";
    private static final String MESG_REVAL_INTERNAL_ERRORS = "ReVal internal errors: {}";
    private static final String MESG_HELM_CHART_VALIDATION_ERRORS =
            "Helm chart validation error(s) from NVCF ReVal service: %s";
    private static final String MESG_UNKNOWN_VALIDATION_ERROR =
            "Helm chart validation failure. Unknown error.";

    private final RevalStubService revalStubService;
    private final Predicate<String> isEnabled;
    private final RevalMetrics metrics;
    private final RegistryArtifactValidationService registryArtifactValidationService;
    private final RegistryCredentialFunctionService registryCredentialFunctionService;
    private final JsonMapper jsonMapper;

    // We could have used OAuth2AuthorizedClientManager and relied on Spring Security to
    // pick up the configuration properties using ClientRegistrationRepository directly. However,
    // the client-secret value held in the ClientRegistrationRepository does not get refreshed when
    // client-secret is rotated. Addressing these issues requires introducing a refreshable
    // ClientRegistrationRepository that wasn't clean. Instead, we will keep it simple and use
    // the tried and tested approach of using @Value and @RefreshScope annotations and wire
    // things up ourselves.
    public RevalClient(
            @Value("${nvcf.reval.base-url}") String baseUrl,
            @Value("${spring.security.oauth2.client.registration.reval.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.reval.client-secret}") String clientSecret,
            @Value("${spring.security.oauth2.client.registration.reval.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.reval.token-uri}") String tokenUri,
            @Value("${nvcf.reval.enabled:true}") boolean enabled,
            @Value("${nvcf.reval.enabled-accounts:}") Set<String> enabledAccounts,
            Optional<StaticClientRevalProperties> staticClientRevalProperties,
            RevalMetrics metrics,
            RegistryArtifactValidationService registryArtifactValidationService,
            RegistryCredentialFunctionService registryCredentialFunctionService,
            JsonMapper jsonMapper,
            WebClient.Builder webClientBuilder,
            ManagedHttpResources revalHttpResources) {
        var webClient = webClientBuilder
                .baseUrl(baseUrl)
                .clientConnector(revalHttpResources.connector())
                .filter(NvcfOAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(oauthFilter(staticClientRevalProperties, webClientBuilder,
                                    clientId, clientSecret, scope, tokenUri))
                .build();
        this.revalStubService = HttpServiceProxyFactory
                .builderFor(WebClientAdapter.create(webClient))
                .build()
                .createClient(RevalStubService.class);

        if (CollectionUtils.isEmpty(enabledAccounts)) {
            this.isEnabled = x -> enabled;
        } else {
            this.isEnabled = ncaId -> enabled && enabledAccounts.contains(ncaId);
        }
        this.metrics = metrics;
        this.registryArtifactValidationService = registryArtifactValidationService;
        this.registryCredentialFunctionService = registryCredentialFunctionService;
        this.jsonMapper = jsonMapper;
    }

    private static ExchangeFilterFunction oauthFilter(
            Optional<StaticClientRevalProperties> staticClientRevalProperties,
            WebClient.Builder webClientBuilder,
            String clientId,
            String clientSecret,
            String scope,
            String tokenUri) {
        return staticClientRevalProperties
                .map(p -> (ExchangeFilterFunction)
                        new FixedBearerExchangeFilterFunction(p::getToken))
                .orElseGet(() -> NvcfOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    public String validate(
            String ncaId,
            FunctionEntity function,
            GpuSpecificationDto deploymentInfo) {
        if (!isEnabled.test(ncaId)) {
            return StringUtils.EMPTY;
        }

        var helmChart = function.getHelmChart();
        var helmChartServiceName = function.getHelmChartServiceName();
        var imageRegistryAuthConfig = validateAndGetContainerRegistryImagePullSecrets(function);
        var helmRegistryAuthConfig = validateAndGetHelmRegistryImagePullSecrets(function);
        var request = RevalValidateRequest
                .builder()
                .helmChart(helmChart)
                .helmChartServiceName(helmChartServiceName)
                .instanceType(deploymentInfo.instanceType())
                .gpu(deploymentInfo.gpu())
                .configuration(deploymentInfo.configuration())
                .imageRegistryAuthConfig(
                        (ObjectNode) jsonMapper.valueToTree(imageRegistryAuthConfig))
                .helmRegistryAuthConfig(
                        (ObjectNode) jsonMapper.valueToTree(helmRegistryAuthConfig))
                .build();

        var response = this.revalStubService.validate(request);
        metrics.incrementRequestCounter(ncaId, response.getStatusCode());

        if (response.getStatusCode().is2xxSuccessful() && response.hasBody()) {
            var body = response.getBody();
            if (!CollectionUtils.isEmpty(body.getInternalErrors())) {
                var internalErrors = String.join(", ", body.getInternalErrors());
                log.warn(MESG_REVAL_INTERNAL_ERRORS, internalErrors);
            }

            if (body.isValid()) {
                return StringUtils.EMPTY;
            }

            String errors;
            if (CollectionUtils.isEmpty(body.getValidationErrors())) {
                errors = MESG_UNKNOWN_VALIDATION_ERROR;
            } else {
                errors = String.join(", ", body.getValidationErrors());
            }

            var msg = String.format(MESG_HELM_CHART_VALIDATION_ERRORS, errors);
            log.error(msg);
            return msg;
        }

        var msg = String.format(MESG_REVAL_ERROR, response.getStatusCode());
        log.error(msg);

        if (response.getStatusCode().is4xxClientError()) {
            throw new IllegalStateException(msg);
        }

        throw new UpstreamException(msg);
    }

    private K8sSecretsDto validateAndGetContainerRegistryImagePullSecrets(FunctionEntity function) {
        registryArtifactValidationService.validateContainerRegistryCredentialsExist(function);
        return registryCredentialFunctionService.getContainerRegistryImagePullSecrets(function);
    }

    private K8sSecretsDto validateAndGetHelmRegistryImagePullSecrets(FunctionEntity function) {
        registryArtifactValidationService.validateHelmRegistryCredentialsExist(function);
        return registryCredentialFunctionService.getHelmRegistryImagePullSecrets(function);
    }
}
