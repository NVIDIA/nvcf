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

package com.nvidia.boot.registries.service.registry.client.ngc;

import tools.jackson.databind.PropertyNamingStrategies;
import tools.jackson.databind.annotation.JsonNaming;
import java.net.URI;
import lombok.Data;
import org.springframework.http.HttpHeaders;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.service.annotation.GetExchange;

public interface NgcContainerRegistryStub {

    @GetExchange("proxy_auth")
    NgcRegistryAuthResponse proxyAuth(
            @RequestHeader(HttpHeaders.AUTHORIZATION) String basic,
            @RequestParam("account") String account,
            @RequestParam(value = "scope", required = false) String scope);

    @GetExchange
    void validateManifest(
            URI url,
            @RequestHeader(HttpHeaders.AUTHORIZATION) String bearer,
            @RequestHeader(HttpHeaders.ACCEPT) String imageMediaTypes);

    @Data
    @JsonNaming(PropertyNamingStrategies.SnakeCaseStrategy.class)
    class NgcRegistryAuthResponse {

        private int expiresIn;
        private String token;
    }
}
