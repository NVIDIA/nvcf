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
package com.nvidia.nvct.configuration.staticclientauth;


import lombok.Data;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;

@AutoConfiguration
public class StaticClientAuthConfiguration {

    @Data
    @RefreshScope
    @Configuration
    @ConditionalOnProperty("nvct.notary.static.token")
    @ConfigurationProperties("nvct.notary.static")
    public static class StaticClientNotaryProperties {

        private String token;
    }

    @Data
    @RefreshScope
    @Configuration
    @ConditionalOnProperty("nvct.nvcf.static.token")
    @ConfigurationProperties("nvct.nvcf.static")
    public static class StaticClientNvcfProperties {

        private String token;
    }

    @Data
    @RefreshScope
    @Configuration
    @ConditionalOnProperty("nvct.reval.static.token")
    @ConfigurationProperties("nvct.reval.static")
    public static class StaticClientRevalProperties {

        private String token;
    }

    @Data
    @RefreshScope
    @Configuration
    @ConditionalOnProperty("nvct.icms.static.token")
    @ConfigurationProperties("nvct.icms.static")
    public static class StaticClientIcmsProperties {

        private String token;
    }

    @Data
    @RefreshScope
    @Configuration
    @ConditionalOnProperty("nvct.ess.static.token")
    @ConfigurationProperties("nvct.ess.static")
    public static class StaticClientEssProperties {

        private String token;
    }

    @Data
    @RefreshScope
    @Configuration
    @ConditionalOnProperty("nvct.api-keys.static.token")
    @ConfigurationProperties("nvct.api-keys.static")
    public static class StaticClientApiKeysProperties {

        private String token;
    }
}
