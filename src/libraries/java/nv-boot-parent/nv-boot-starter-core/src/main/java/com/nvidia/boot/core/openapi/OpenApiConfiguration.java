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

import io.swagger.v3.oas.models.OpenAPI;
import io.swagger.v3.oas.models.info.Contact;
import io.swagger.v3.oas.models.info.Info;
import lombok.RequiredArgsConstructor;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.env.Environment;

/**
 * Provides a default OpenAPI bean when none exists.
 * Uses springdoc.info.* and falls back to spring.application.name/version.
 */
@Configuration
@ConditionalOnClass(name = "io.swagger.v3.oas.models.OpenAPI")
@ConditionalOnMissingBean(OpenAPI.class)
@EnableConfigurationProperties(SpringDocProperties.class)
@RequiredArgsConstructor
public class OpenApiConfiguration {

    private final SpringDocProperties properties;
    private final Environment environment;

    @Bean
    public OpenAPI customOpenAPI() {
        var info = new Info()
                .title(resolveTitle())
                .description(properties.getInfo().getDescription())
                .version(resolveVersion());

        var contact = properties.getInfo().getContact();
        info.contact(new Contact()
                .name(StringUtils.isNotBlank(contact.getName()) ? contact.getName() : "NVIDIA")
                .email(contact.getEmail())
                .url(StringUtils.isNotBlank(contact.getUrl()) ? contact.getUrl() :
                                                                "https://www.nvidia.com/"));

        var termsOfService = properties.getInfo().getTermsOfService();
        info.termsOfService(StringUtils.isNotBlank(termsOfService) ? termsOfService :
                                    "https://www.nvidia.com/en-us/legal_info");

        return new OpenAPI().info(info);
    }

    private String resolveTitle() {
        var title = properties.getInfo().getTitle();
        if (StringUtils.isNotBlank(title)) {
            return title;
        }
        var appName = environment.getProperty("spring.application.name", "API");
        return capitalizeFully(appName.replace("-", " "));
    }

    private String resolveVersion() {
        var version = properties.getInfo().getVersion();
        if (StringUtils.isNotBlank(version)) {
            return version;
        }
        return environment.getProperty("spring.application.version", "1.0.0");
    }

    private static String capitalizeFully(String str) {
        if (StringUtils.isBlank(str)) {
            return str;
        }
        var words = str.split("\\s+");
        var result = new StringBuilder();
        for (var i = 0; i < words.length; i++) {
            if (i > 0) {
                result.append(" ");
            }
            var word = words[i];
            if (!word.isEmpty()) {
                result.append(Character.toUpperCase(word.charAt(0)))
                        .append(word.substring(1).toLowerCase());
            }
        }
        return result.toString();
    }
}
