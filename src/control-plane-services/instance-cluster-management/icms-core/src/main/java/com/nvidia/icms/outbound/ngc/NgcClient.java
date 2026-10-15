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
package com.nvidia.icms.outbound.ngc;

import com.nvidia.icms.outbound.ngc.model.GetOrganizationResponse;
import com.nvidia.icms.util.OAuth2ClientUtils;
import com.nvidia.icms.util.OAuth2ClientUtils.ManagedHttpResources;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

@Slf4j
@Service
@RefreshScope
public class NgcClient {

    private static final String CLIENT_REGISTRATION_ID = "ngc";

    private final NgcStubService ngcStubService;

    public NgcClient(
            WebClient.Builder webClientBuilder, // Prototype-scoped - Safe to mutate.
            @Qualifier("ngcHttpResources") ManagedHttpResources httpResources,
            @Value("${icms.ngc.service-url}") String baseUrl,
            @Value("${spring.security.oauth2.client.registration.ngc.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.ngc.client-secret}")
            String clientSecret,
            @Value("${spring.security.oauth2.client.registration.ngc.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.ngc.token-uri}") String tokenUri) {

        var webClient = webClientBuilder
                .baseUrl(baseUrl)
                .clientConnector(httpResources.connector())
                .filter(OAuth2ClientUtils.getOauth2ExchangeFilter(
                        CLIENT_REGISTRATION_ID, tokenUri, clientId, clientSecret, scope))
                .filter(OAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .build();

        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.ngcStubService = factory.createClient(NgcStubService.class);
    }

    public GetOrganizationResponse getOrgInfo(String ncaId) {
        return ngcStubService.getOrgInfo(ncaId);
    }
}
