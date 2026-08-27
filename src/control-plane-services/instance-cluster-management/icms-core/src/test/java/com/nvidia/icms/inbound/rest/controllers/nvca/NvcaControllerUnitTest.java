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
package com.nvidia.icms.inbound.rest.controllers.nvca;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.configuration.security.AuthManagerResolver;
import com.nvidia.icms.inbound.rest.model.nvca.NatsAuthorizeRequest;
import com.nvidia.icms.inbound.rest.model.nvca.TokenIntrospectRequest;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.byoc.nvca.ClusterOidcIdentityService;
import com.nvidia.icms.service.byoc.nvca.NvcaNatsAuthorizationService;
import com.nvidia.icms.service.byoc.nvca.ClusterOIDCTokenVerificationService;
import java.nio.charset.StandardCharsets;
import java.text.ParseException;
import java.util.Base64;
import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.JwtDecoder;
import org.springframework.test.util.ReflectionTestUtils;

@ExtendWith(MockitoExtension.class)
class NvcaControllerUnitTest {

    @Mock
    private AuthManagerResolver authManagerResolver;

    @Mock
    private ClusterOidcIdentityService clusterOidcIdentityService;

    @Mock
    private NvcaNatsAuthorizationService nvcaNatsAuthorizationService;

    @Mock
    private JwtDecoder jwtDecoder;

    @Test
    void introspectToken_malformedStoredJwks_returnsInactiveWithGenericError() throws Exception {
        String clusterId = "11111111-1111-1111-1111-111111111111";
        String storedJwks = "not-valid-jwks-json";
        NvcaController controller = controllerWithOidcEnabled();
        TokenIntrospectRequest request = new TokenIntrospectRequest();
        request.setToken(unsignedToken(clusterId));

        when(clusterOidcIdentityService.findByClusterId(clusterId))
                .thenReturn(Optional.of(ClusterEntity.builder()
                        .clusterId(clusterId)
                        .jwks(storedJwks)
                        .build()));
        when(authManagerResolver.buildJwtDecoderFromJwks(storedJwks))
                .thenThrow(new ParseException("bad jwks", 0));

        var response = controller.introspectToken(request);

        assertEquals(200, response.getStatusCode().value());
        assertFalse(response.getBody().isActive());
        assertEquals("JWT verification failed", response.getBody().getError());
    }

    @Test
    void natsAuthorize_unexpectedAccount_returns403BeforeTokenVerification() {
        NvcaController controller = controllerWithOidcEnabled();

        var response = controller.natsAuthorize(natsRequest("SYS", "oidc"));

        assertEquals(403, response.getStatusCode().value());
    }

    @Test
    void natsAuthorize_unexpectedPluginName_returns403BeforeTokenVerification() {
        NvcaController controller = controllerWithOidcEnabled();

        var response = controller.natsAuthorize(natsRequest("APP", "nkey"));

        assertEquals(403, response.getStatusCode().value());
    }

    @Test
    void natsAuthorize_permissionConfigInvalid_returns403() throws Exception {
        String clusterId = "11111111-1111-1111-1111-111111111111";
        String storedJwks = "{\"keys\":[]}";
        String token = unsignedToken(clusterId);
        NvcaController controller = controllerWithOidcEnabled();
        ReflectionTestUtils.setField(controller, "nvcaNatsAuthorizationService", nvcaNatsAuthorizationService);
        NatsAuthorizeRequest request = natsRequest("APP", "oidc");
        request.setPayload(token);

        when(clusterOidcIdentityService.findByClusterId(clusterId))
                .thenReturn(Optional.of(ClusterEntity.builder()
                        .clusterId(clusterId)
                        .jwks(storedJwks)
                        .build()));
        when(authManagerResolver.buildJwtDecoderFromJwks(storedJwks)).thenReturn(jwtDecoder);
        when(jwtDecoder.decode(token)).thenReturn(mock(Jwt.class));
        when(nvcaNatsAuthorizationService.buildResponse(clusterId))
                .thenThrow(new IllegalStateException("NATS auth permissions require at least one allow template"));

        var response = controller.natsAuthorize(request);

        assertEquals(403, response.getStatusCode().value());
        verify(nvcaNatsAuthorizationService).buildResponse(clusterId);
    }

    private NvcaController controllerWithOidcEnabled() {
        NvcaConfigurationProperties config = new NvcaConfigurationProperties();
        config.setOidcClusterIdentityEnabled(true);

        NvcaController controller = new NvcaController();
        ReflectionTestUtils.setField(controller, "nvcaConfig", config);
        ReflectionTestUtils.setField(controller, "nvcaTokenVerificationService",
                new ClusterOIDCTokenVerificationService(authManagerResolver, clusterOidcIdentityService));
        return controller;
    }

    private static String unsignedToken(String clusterId) {
        String claims = "{\"iss\":\"https://k8s.example.com\","
                + "\"sub\":\"system:serviceaccount:nvca-system:nvca\","
                + "\"aud\":\"nvcf-icms:" + clusterId + "\"}";
        String payload = Base64.getUrlEncoder().withoutPadding()
                .encodeToString(claims.getBytes(StandardCharsets.UTF_8));
        return "eyJhbGciOiJSUzI1NiJ9." + payload + ".signature";
    }

    private static NatsAuthorizeRequest natsRequest(String account, String pluginName) {
        NatsAuthorizeRequest request = new NatsAuthorizeRequest();
        request.setAccount(account);
        request.setPluginName(pluginName);
        request.setPayload("eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig");
        return request;
    }
}
