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
import org.springframework.web.service.annotation.GetExchange;
import org.springframework.web.service.annotation.HttpExchange;

public interface NgcContainerRegistryStub {

    /**
     * Exchanges a credential for a bearer token at the realm a challenge advertised. The URL is
     * absolute because the realm may point at a different origin than the registry.
     */
    @GetExchange
    NgcRegistryAuthResponse fetchToken(
            URI tokenUrl,
            @RequestHeader(HttpHeaders.AUTHORIZATION) String basic);

    /**
     * Checks a manifest with HEAD: existence and pullability are signaled by the status alone,
     * so the body a GET would carry is never needed.
     */
    @HttpExchange(method = "HEAD")
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
