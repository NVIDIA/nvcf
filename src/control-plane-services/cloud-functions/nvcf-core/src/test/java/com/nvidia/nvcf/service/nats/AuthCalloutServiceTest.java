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
package com.nvidia.nvcf.service.nats;

import static com.nvidia.nvcf.service.nats.AuthCalloutService.API_USER_ACCOUNT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvcf.service.nats.AuthCalloutService.AuthCalloutPluginRequest;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import java.time.Instant;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import tools.jackson.databind.json.JsonMapper;

@ExtendWith(MockitoExtension.class)
class AuthCalloutServiceTest {

    private static final JsonMapper OBJECT_MAPPER = new JsonMapper();
    private static final String VALID_PAYLOAD = "valid-worker-token";

    @Mock
    private GrpcTokenService grpcTokenService;

    @InjectMocks
    private AuthCalloutService authCalloutService;

    // --- validateWebhookPlugin ---

    @Test
    void validateWebhookPluginValidRequestReturnsResultWithCorrectUserIdAndAccount() {
        var issuedToken = workerToken();
        when(grpcTokenService.validateToken(VALID_PAYLOAD, TokenType.WORKER)).thenReturn(issuedToken);

        var result = authCalloutService.validateWebhookPlugin(API_USER_ACCOUNT, "webhook",
                                                              VALID_PAYLOAD);

        assertThat(result.account()).isEqualTo(API_USER_ACCOUNT);
        assertThat(result.userId()).isEqualTo("worker-" + TEST_VERSION_ID_1);
        assertThat(result.userClaim()).isNotNull();
    }

    @Test
    void validateWebhookPluginValidRequestUserClaimIsNotNull() {
        when(grpcTokenService.validateToken(VALID_PAYLOAD, TokenType.WORKER)).thenReturn(
                workerToken());

        var result = authCalloutService.validateWebhookPlugin(API_USER_ACCOUNT, "webhook",
                                                              VALID_PAYLOAD);

        // Detailed permission contents are exercised implicitly; assert the claim was built.
        assertThat(result.userClaim()).isNotNull();
        assertThat(result.userClaim().pub).isNotNull();
        assertThat(result.userClaim().sub).isNotNull();
        assertThat(result.userClaim().resp).isNotNull();
    }

    @Test
    void validateWebhookPluginNonWebhookPluginThrowsForbidden() {
        assertThatThrownBy(() -> authCalloutService.validateWebhookPlugin(
                API_USER_ACCOUNT, "some-other-plugin", VALID_PAYLOAD))
                .isInstanceOf(ForbiddenException.class);
    }

    @Test
    void validateWebhookPluginNonWorkerAccountThrowsForbidden() {
        assertThatThrownBy(() -> authCalloutService.validateWebhookPlugin(
                "SomeOtherAccount", "webhook", VALID_PAYLOAD))
                .isInstanceOf(ForbiddenException.class);
    }

    @Test
    void validateWebhookPluginInvalidTokenPropagatesForbiddenFromGrpcTokenService() {
        when(grpcTokenService.validateToken(VALID_PAYLOAD, TokenType.WORKER))
                .thenThrow(new ForbiddenException("invalid token"));

        assertThatThrownBy(() -> authCalloutService.validateWebhookPlugin(
                API_USER_ACCOUNT, "webhook", VALID_PAYLOAD))
                .isInstanceOf(ForbiddenException.class);
    }

    // --- AuthCalloutPluginRequest ---

    @Test
    void authCalloutPluginRequestToTokenFromTokenRoundTrip() throws Exception {
        var original = new AuthCalloutPluginRequest(API_USER_ACCOUNT, "oidc", "my-access-token");

        var encoded = original.toToken(OBJECT_MAPPER);
        var decoded = AuthCalloutPluginRequest.fromToken(new String(encoded), OBJECT_MAPPER);

        assertThat(decoded.account()).isEqualTo(original.account());
        assertThat(decoded.pluginName()).isEqualTo(original.pluginName());
        assertThat(decoded.payload()).isEqualTo(original.payload());
    }

    @Test
    void authCalloutPluginRequestFromTokenBlankAccountThrowsIllegalArgument() throws Exception {
        var token = new String(
                new AuthCalloutPluginRequest("", "oidc", "my-access-token").toToken(OBJECT_MAPPER));

        assertThatThrownBy(() -> AuthCalloutPluginRequest.fromToken(token, OBJECT_MAPPER))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessage("Invalid auth callout plugin token");
    }

    @Test
    void authCalloutPluginRequestFromTokenBlankPluginNameThrowsIllegalArgument()
            throws Exception {
        var token = new String(
                new AuthCalloutPluginRequest(API_USER_ACCOUNT, "", "my-access-token").toToken(
                        OBJECT_MAPPER));

        assertThatThrownBy(() -> AuthCalloutPluginRequest.fromToken(token, OBJECT_MAPPER))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessage("Invalid auth callout plugin token");
    }

    @Test
    void authCalloutPluginRequestFromTokenBlankPayloadThrowsIllegalArgument() throws Exception {
        var token = new String(
                new AuthCalloutPluginRequest(API_USER_ACCOUNT, "oidc", "").toToken(OBJECT_MAPPER));

        assertThatThrownBy(() -> AuthCalloutPluginRequest.fromToken(token, OBJECT_MAPPER))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessage("Invalid auth callout plugin token");
    }

    // --- helpers ---

    private static NvcfIssuedToken workerToken() {
        return new NvcfIssuedToken(TEST_FUNCTION_ID, TEST_VERSION_ID_1, Instant.now(),
                                   TokenType.WORKER);
    }
}
