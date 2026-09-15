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
package com.nvidia.nvcf.rest.webhook;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.rest.webhook.dto.WebhookRequest;
import com.nvidia.nvcf.rest.webhook.dto.WebhookResponse;
import com.nvidia.nvcf.service.nats.AuthCalloutService;
import io.nats.jwt.Permission;
import io.nats.jwt.ResponsePermission;
import io.nats.jwt.UserClaim;
import io.swagger.v3.oas.annotations.Hidden;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE)
@Tag(name = "Webhook Plugin Authentication API", description = "API for authenticating webhook plugin requests")
public class WebhookController {

    private final AuthCalloutService authCalloutService;

    /**
     * no PreAuthorize on purpose. we're validating a token provided in the request, which is auth in itself.
     */
    @Hidden // no need to advertise a webhook for validating nats auth on our public docs.
    @PostMapping(value = "/v2/nvcf/webhook/nats-auth", consumes = APPLICATION_JSON_VALUE)
    @Operation(summary = "Authenticate Webhook Plugin", description = """
            Authenticates a webhook plugin request and returns appropriate permissions.
            Only supports webhook plugin for Worker account.
            """)
    public WebhookResponse authenticateWebhookPlugin(
            @Valid @RequestBody WebhookRequest request) {
        log.info("Received webhook authentication request for account: {}, plugin: {}",
                 request.account(), request.pluginName());
        var result = authCalloutService.validateWebhookPlugin(
                request.account(), request.pluginName(), request.payload());
        return WebhookResponse.builder()
                .userId(result.userId())
                .account(result.account())
                .permissions(convertToWebhookPermissions(result.userClaim()))
                .ttl(null) // never apply a ttl to the nats connection
                .build();
    }

    private static WebhookResponse.WebhookPermissions convertToWebhookPermissions(
            UserClaim userClaim) {
        var pubPermission = convertPermission(userClaim.pub);
        var subPermission = convertPermission(userClaim.sub);
        var responsePermission = convertResponsePermission(userClaim.resp);

        return WebhookResponse.WebhookPermissions.builder()
                .publish(pubPermission)
                .subscribe(subPermission)
                .response(responsePermission)
                .build();
    }

    private static WebhookResponse.WebhookPermission convertPermission(Permission permission) {
        return WebhookResponse.WebhookPermission.builder()
                .allow(permission.allow)
                .deny(permission.deny)
                .build();
    }

    private static WebhookResponse.WebhookResponsePermission convertResponsePermission(
            ResponsePermission responsePermission) {
        return WebhookResponse.WebhookResponsePermission.builder()
                .maxMsgs(responsePermission.max)
                .ttl(responsePermission.expires)
                .build();
    }
}