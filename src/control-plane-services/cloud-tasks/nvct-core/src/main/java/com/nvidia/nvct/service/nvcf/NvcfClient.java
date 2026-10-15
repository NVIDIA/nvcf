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
package com.nvidia.nvct.service.nvcf;

import static com.nvidia.nvct.util.NvctConstants.MAX_BUFFER_LIMIT;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvct.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvct.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientNvcfProperties;
import com.nvidia.nvct.service.account.dto.AccountDto;
import com.nvidia.nvct.service.client.dto.ClientDto;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils.ManagedHttpResources;
import java.time.Duration;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

@Service
@RefreshScope
@Slf4j
public class NvcfClient {

    private static final String MESG_BLANK_PARAMETER = "'%s' cannot be blank";
    public static final String CLIENT_REGISTRATION_ID = "nvcf";

    private final NvcfStubService nvcfStubService;
    private final LoadingCache<String, AccountDto> cachedNvcfAccounts;
    private final LoadingCache<String, ClientDto> cachedNvcfClients;

    // We could have used OAuth2AuthorizedClientManager and relied on Spring Security to
    // pick up the configuration properties using ClientRegistrationRepository directly. However,
    // the client-secret value held in the ClientRegistrationRepository does not get refreshed when
    // client-secret is rotated. Addressing these issues requires introducing a refreshable
    // ClientRegistrationRepository that wasn't clean. Instead, we will keep it simple and use
    // the tried and tested approach of using @Value and @RefreshScope annotations and wire
    // things up ourselves.
    public NvcfClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            ManagedHttpResources nvcfHttpResources,
            @Value("${nvct.nvcf.base-url}") String baseUrl,
            @Value("${spring.security.oauth2.client.registration.nvcf.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.nvcf.client-secret}") String clientSecret,
            @Value("${spring.security.oauth2.client.registration.nvcf.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.nvcf.token-uri}") String tokenUri,
            @Value("${nvct.nvcf.cache-ttl:PT15M}") Duration cacheTtl,
            Optional<StaticClientNvcfProperties> staticClientNvcfProperties) {
        this.cachedNvcfAccounts = Caffeine.newBuilder()
                .maximumSize(512)
                .scheduler(Scheduler.systemScheduler())
                .expireAfterWrite(cacheTtl)
                .build(this::fetchAccount);
        this.cachedNvcfClients = Caffeine.newBuilder()
                .maximumSize(512)
                .scheduler(Scheduler.systemScheduler())
                .expireAfterWrite(cacheTtl)
                .build(this::fetchClient);

        var authFilter = oauthFilter(staticClientNvcfProperties, webClientBuilder,
                                     clientId, clientSecret, scope, tokenUri);
        var webClient = webClientBuilder
                .baseUrl(baseUrl)
                .codecs(configurer -> configurer.defaultCodecs().maxInMemorySize(MAX_BUFFER_LIMIT))
                .clientConnector(nvcfHttpResources.connector())
                .filter(NvctOAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(authFilter)
                .filter(NvctOAuth2ClientUtils.getResponseFilterProcessor("NVCF"))
                .build();
        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.nvcfStubService = factory.createClient(NvcfStubService.class);
    }

    private static ExchangeFilterFunction oauthFilter(
            Optional<StaticClientNvcfProperties> staticClientNvcfProperties,
            WebClient.Builder webClientBuilder,
            String clientId,
            String clientSecret,
            String scope,
            String tokenUri) {
        return staticClientNvcfProperties
                .map(p -> (ExchangeFilterFunction)
                        new FixedBearerExchangeFilterFunction(p::getToken))
                .orElseGet(() -> NvctOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    public AccountDto getAccount(String ncaId) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        return cachedNvcfAccounts.get(ncaId);
    }

    public ClientDto getClient(String clientId) {
        if (StringUtils.isBlank(clientId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("clientId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        return cachedNvcfClients.get(clientId);
    }

    public void invalidateCache() {
        cachedNvcfClients.invalidateAll();
        cachedNvcfAccounts.invalidateAll();
    }

    public void invalidateCacheForSpecificAccount(String ncaId) {
        var account = cachedNvcfAccounts.get(ncaId);
        if (account == null) {
            return;
        }
        cachedNvcfAccounts.invalidate(ncaId);
    }

    private AccountDto fetchAccount(String ncaId) {
        var response = nvcfStubService.fetchAccount(ncaId);
        if (response == null) {
            throw new UpstreamException("No response from NVCF");
        }
        var result = response.getAccount();
        if (result == null) {
            throw new UnauthorizedException("Unknown NCA Id: " + ncaId);
        }
        return result;
    }

    private ClientDto fetchClient(String clientId) {
        var response = nvcfStubService.fetchClient(clientId);
        if (response == null) {
            throw new UpstreamException("No response from NVCF");
        }
        var result = response.getClient();
        if (result == null) {
            throw new UnauthorizedException("Unknown Client Id: " + clientId);
        }
        return result;
    }

}
