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

package com.nvidia.boot.registries.configurations;

import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import java.time.Duration;
import java.util.Map;
import lombok.Data;
import org.springframework.validation.annotation.Validated;

@Data
@Validated
public class RegistryConfigurationProperties {

    private RecognizedRegistryConfiguration recognized;

    @Data
    public static class RecognizedRegistryConfiguration {

        @Valid
        private Map<String, RegistryConfiguration> container;
        @Valid
        private Map<String, RegistryConfiguration> helm;
        @Valid
        private Map<String, RegistryConfiguration> model;
        @Valid
        private Map<String, RegistryConfiguration> resource;
    }

    @Data
    public static class RegistryConfiguration {

        @NotBlank(message = "Registry name is required")
        private String name;
        @NotBlank(message = "Registry hostname is required")
        private String hostname;
        private Duration callTimeout;
        private Duration readTimeout;
        private Duration writeTimeout;
        private Duration connectionTimeout;
        private OAuth2Configuration oauth2;
        private ValidationProperties credentialValidation = new ValidationProperties();
        private ValidationProperties artifactValidation = new ValidationProperties();
    }

    @Data
    public static class ValidationProperties {

        private boolean enabled = true;
    }

    @Data
    public static class OAuth2Configuration {

        private String baseUrl;
        private String groupScope;
    }
}
