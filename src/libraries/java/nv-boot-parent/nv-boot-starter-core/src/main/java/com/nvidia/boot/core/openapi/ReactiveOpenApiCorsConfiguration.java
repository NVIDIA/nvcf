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

package com.nvidia.boot.core.openapi;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.web.server.WebFilter;

@Configuration
@ConditionalOnProperty(name = "springdoc.api-docs.cors.enabled", matchIfMissing = true)
@ConditionalOnWebApplication(type = ConditionalOnWebApplication.Type.REACTIVE)
public class ReactiveOpenApiCorsConfiguration {

    @Bean
    public WebFilter openApiReactiveCorsFilter(
            @Value("${springdoc.api-docs.path:/v3/openapi}") String apiDocsPath) {
        return (exchange, chain) -> {
            if (exchange.getRequest().getURI().getPath().equals(apiDocsPath)) {
                var headers = exchange.getResponse().getHeaders();
                headers.set("Access-Control-Allow-Origin", "*");
                headers.set("Access-Control-Allow-Methods", "*");
                headers.set("Access-Control-Max-Age", "3600");
                headers.set("Access-Control-Allow-Credentials", "true");
                headers.set("Access-Control-Allow-Headers", "*");
            }
            return chain.filter(exchange);
        };
    }
}
