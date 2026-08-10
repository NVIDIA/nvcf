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

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import io.nats.jwt.Permission;
import io.nats.jwt.ResponsePermission;
import io.nats.jwt.UserClaim;
import java.util.Base64;
import java.util.List;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
@RequiredArgsConstructor
public class AuthCalloutService {

    public static final String API_USER_ACCOUNT = "Worker";

    private final GrpcTokenService grpcTokenService;

    /**
     * Validates a webhook plugin token and returns the user claim and user ID.
     * Only supports webhook plugin and the NATS Worker account.
     */
    public WebhookValidationResult validateWebhookPlugin(
            String account, String pluginName, String payload) {
        if (!"webhook".equals(pluginName)) {
            throw new ForbiddenException("Only webhook plugin is supported");
        }

        if (!API_USER_ACCOUNT.equals(account)) {
            throw new ForbiddenException("Webhook plugin can only be used with Worker account");
        }

        // Validate as worker token and create UserClaim - reuse existing logic
        var nvcfIssuedToken = grpcTokenService.validateToken(payload, TokenType.WORKER);
        var userClaim = createWorkerUserClaim(nvcfIssuedToken);

        log.debug("Successfully validated webhook plugin for function version: {}",
                  nvcfIssuedToken.functionVersionId());

        return new WebhookValidationResult(
                "worker-" + nvcfIssuedToken.functionVersionId(),
                account,
                userClaim
        );
    }

    private static UserClaim createWorkerUserClaim(
            GrpcTokenService.NvcfIssuedToken nvcfIssuedToken) {
        // rq.${region}.${function_version}.${request_id}
        // stateful_session.lookup.${region}.${function_version}.${request_id}
        // stateful_session.reconnect.${request_id}
        // llsrq.${region}.${function_version}.${request_id}
        // rq_polling.${request_id}
        // nvcf.cancel.${function_version} (invocation service broadcasts client
        //   disconnects/cancellations so workers can tear down in-flight inference)
        var allowedSubjects = List.of(
                "rq.*.%s.>".formatted(nvcfIssuedToken.functionVersionId()),
                "stateful_session.lookup.*.%s.>".formatted(nvcfIssuedToken.functionVersionId()),
                "stateful_session.reconnect.>",
                "llsrq.*.%s.>".formatted(nvcfIssuedToken.functionVersionId()),
                "nvcf.cancel.%s".formatted(nvcfIssuedToken.functionVersionId()),
                "$JS.API.STREAM.INFO.*",
                "rq_polling.*",
                // for workers to purge their registration in stateful_session.lookup
                "$JS.API.STREAM.PURGE.*",
                "$JS.API.CONSUMER.INFO.>",
                "$JS.API.CONSUMER.MSG.NEXT.*.*",
                "$JS.ACK.>",
                "_INBOX.>");
        var permission = new Permission().allow(allowedSubjects);
        return new UserClaim().pub(permission).sub(permission)
                .resp(new ResponsePermission().max(1));
    }

    public record WebhookValidationResult(String userId, String account, UserClaim userClaim) {

    }

    public record AuthCalloutPluginRequest(String account, String pluginName, String payload) {

        public static AuthCalloutPluginRequest fromToken(String token, JsonMapper jsonMapper)
                throws JacksonException {
            var jsonToken = Base64.getUrlDecoder().decode(token);
            var request = jsonMapper.readValue(jsonToken, AuthCalloutPluginRequest.class);
            if (!StringUtils.hasText(request.account()) || !StringUtils.hasText(
                    request.pluginName())
                    || !StringUtils.hasText(request.payload())) {
                throw new IllegalArgumentException("Invalid auth callout plugin token");
            }
            return request;
        }

        @SneakyThrows
        public char[] toToken(JsonMapper jsonMapper) {
            var bytes = jsonMapper.writeValueAsBytes(this);
            return Base64.getUrlEncoder().encodeToString(bytes).toCharArray();
        }
    }
}
