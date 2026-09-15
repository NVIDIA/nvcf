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

package com.nvidia.apikeys.config;

import com.nvidia.boot.jwt.configuration.EncryptedModelConverterProperties;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import java.util.Map;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.boot.context.properties.NestedConfigurationProperty;
import org.springframework.context.annotation.Configuration;
import org.springframework.validation.annotation.Validated;

@Data
@Builder
@NoArgsConstructor
@AllArgsConstructor
@Configuration
@Validated
@ConfigurationProperties(prefix = "apikeys", ignoreUnknownFields = false)
public class NakProperties {

    // service metadata
    @NotBlank
    private String ncaId;
    @NotBlank
    private String registrations;
    // Resolves an apikey.allow request's URL namespace (e.g. "nvcf", "nvct") to
    // the service id that policies must carry in their `aud` claim.
    @NotEmpty
    private Map<String, String> serviceIdMap;

    // api key properties
    @NotBlank
    private String keyPrefix;
    @NotBlank
    private String dataDomainKey;
    @NotNull
    private Duration keepAfterExpiredDuration;

    @NotNull
    @Valid
    @NestedConfigurationProperty
    private JwksProperties jwks;

    @NotNull
    @NestedConfigurationProperty
    private EncryptedModelConverterProperties encryptedModelConverter;
}
