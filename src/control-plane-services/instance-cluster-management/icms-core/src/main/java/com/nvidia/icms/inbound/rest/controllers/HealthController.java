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
package com.nvidia.icms.inbound.rest.controllers;

import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.tags.Tag;
import lombok.RequiredArgsConstructor;
import org.springframework.boot.actuate.endpoint.ApiVersion;
import org.springframework.boot.actuate.endpoint.SecurityContext;
import org.springframework.boot.actuate.endpoint.web.WebEndpointResponse;
import org.springframework.boot.actuate.endpoint.web.WebServerNamespace;
import org.springframework.boot.health.actuate.endpoint.HealthDescriptor;
import org.springframework.boot.health.actuate.endpoint.HealthEndpointWebExtension;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

@Tag(name = "Health")
@RestController("sisHealthController")
@RequiredArgsConstructor
public class HealthController {

    private final HealthEndpointWebExtension healthEndpoint;

    @Operation(summary = "Get Health Information",
            description = "Get Health Information about this service")
    @GetMapping(value = "/v1/health", produces = MediaType.APPLICATION_JSON_VALUE)
    public ResponseEntity<HealthDescriptor> getHealth() {
        WebEndpointResponse<HealthDescriptor> health = healthEndpoint.health(
                ApiVersion.V3, WebServerNamespace.SERVER, SecurityContext.NONE, "liveness");
        return new ResponseEntity<>(health.getBody(), HttpStatus.valueOf(health.getStatus()));
    }
}
