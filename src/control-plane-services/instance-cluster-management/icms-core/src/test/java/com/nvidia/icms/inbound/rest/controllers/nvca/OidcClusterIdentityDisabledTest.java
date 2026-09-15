/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package com.nvidia.icms.inbound.rest.controllers.nvca;

import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.inbound.rest.model.nvca.TokenIntrospectRequest;
import com.nvidia.icms.inbound.rest.model.nvca.UpdateJwksRequest;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.util.JwtKeyUtils;
import com.nvidia.icms.util.TestUtil;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.context.TestPropertySource;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.request.MockMvcRequestBuilders;
import org.springframework.test.web.servlet.result.MockMvcResultMatchers;

/**
 * Exercises ICMS behavior when the OIDC/PSAT cluster identity feature flag is <b>disabled</b>
 * (managed NVCF / legacy mode).
 *
 * <p>The base integration test profile enables the flag in {@code application-test.yaml} so the
 * existing feature tests pass. This class overrides it back to {@code false} via
 * {@link TestPropertySource} to assert the flag-off contract:
 *
 * <ul>
 *   <li>{@code PUT /v1/nvca/clusters/{id}/jwks} → 404
 *   <li>{@code POST /v1/nvca/tokens/introspect} without auth → 401
 * </ul>
 *
 * <p>Registration path (flag-off ignores jwks/oidcIssuer while persisting the cluster) is
 * covered at the service layer in {@code ClusterCreationServiceTest}.
 */
@TestPropertySource(properties = "icms.nvca.oidcClusterIdentityEnabled=false")
class OidcClusterIdentityDisabledTest extends IntegrationTest {

    private static final String JWKS_URL = "/v1/nvca/clusters/{clusterId}/jwks";
    private static final String INTROSPECT_URL = "/v1/nvca/tokens/introspect";
    private static final String INTROSPECT_ALIAS_URL = "/v1/si/oidc/tokens/introspect";
    private static final String INTROSPECT_SINGULAR_ALIAS_URL = "/v1/si/oidc/token/introspect";
    private static final String NATS_AUTHORIZE_URL = "/v1/nvca/nats-authorize";
    private static final String NATS_AUTHORIZE_ALIAS_URL = "/v1/si/oidc/nats-authorize";
    private static final String NATS_AUTHORIZE_CAMEL_ALIAS_URL = "/v1/si/oidc/natsAuthorize";

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private ObjectMapper objectMapper;

    @Test
    void updateClusterJwks_flagDisabled_returns404() throws Exception {
        // Authenticated call (OAuth with cluster-management scope) to isolate the flag-off
        // behavior from Spring Security's no-auth 401. Expected contract: endpoint
        // exists at the URL but returns 404 because the feature is not enabled on this
        // deployment.
        UpdateJwksRequest body = new UpdateJwksRequest();
        body.setJwks("{\"keys\":[]}");

        mockMvc.perform(
                        MockMvcRequestBuilders.put(JWKS_URL, DUMMY_CLUSTER_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(objectMapper.writeValueAsString(body))
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                  TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isNotFound());
    }

    @Test
    void introspectToken_flagDisabledWithoutAuth_returns401() throws Exception {
        TokenIntrospectRequest body = new TokenIntrospectRequest();
        body.setToken("any.jwt.here");

        mockMvc.perform(
                        MockMvcRequestBuilders.post(INTROSPECT_URL)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(objectMapper.writeValueAsString(body)))
                .andExpect(MockMvcResultMatchers.status().isUnauthorized());
    }

    @Test
    void introspectAlias_flagDisabledWithoutAuth_returns401() throws Exception {
        TokenIntrospectRequest body = new TokenIntrospectRequest();
        body.setToken("any.jwt.here");

        mockMvc.perform(
                        MockMvcRequestBuilders.post(INTROSPECT_ALIAS_URL)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(objectMapper.writeValueAsString(body)))
                .andExpect(MockMvcResultMatchers.status().isUnauthorized());
    }

    @Test
    void introspectSingularAlias_flagDisabledWithoutAuth_returns401() throws Exception {
        TokenIntrospectRequest body = new TokenIntrospectRequest();
        body.setToken("any.jwt.here");

        mockMvc.perform(
                        MockMvcRequestBuilders.post(INTROSPECT_SINGULAR_ALIAS_URL)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(objectMapper.writeValueAsString(body)))
                .andExpect(MockMvcResultMatchers.status().isUnauthorized());
    }

    @Test
    void natsAuthorize_flagDisabledWithoutAuth_returns401() throws Exception {
        String body = "{\"account\":\"APP\",\"pluginName\":\"oidc\",\"payload\":\"any.jwt.here\"}";

        mockMvc.perform(
                        MockMvcRequestBuilders.post(NATS_AUTHORIZE_URL)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(body))
                .andExpect(MockMvcResultMatchers.status().isUnauthorized());
    }

    @Test
    void natsAuthorizeAlias_flagDisabledWithoutAuth_returns401() throws Exception {
        String body = "{\"account\":\"APP\",\"pluginName\":\"oidc\",\"payload\":\"any.jwt.here\"}";

        mockMvc.perform(
                        MockMvcRequestBuilders.post(NATS_AUTHORIZE_ALIAS_URL)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(body))
                .andExpect(MockMvcResultMatchers.status().isUnauthorized());
    }

    @Test
    void natsAuthorizeCamelAlias_flagDisabledWithoutAuth_returns401() throws Exception {
        String body = "{\"account\":\"APP\",\"pluginName\":\"oidc\",\"payload\":\"any.jwt.here\"}";

        mockMvc.perform(
                        MockMvcRequestBuilders.post(NATS_AUTHORIZE_CAMEL_ALIAS_URL)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(body))
                .andExpect(MockMvcResultMatchers.status().isUnauthorized());
    }
}
