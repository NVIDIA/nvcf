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
package com.nvidia.nvcf.configuration;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.tuple;

import com.nvidia.nvcf.configuration.TrustedJwtIssuerProperties.TrustedIssuer;
import java.util.LinkedHashMap;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.boot.context.properties.bind.Bindable;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.core.env.StandardEnvironment;
import org.springframework.core.env.SystemEnvironmentPropertySource;

// Pins the env var names the self-managed stack emits against the property names bound here.
class TrustedIssuerEnvBindingTest {

    @Test
    void bindsTrustedIssuersFromTheEnvironmentVariablesTheStackEmits() {
        Map<String, Object> variables = new LinkedHashMap<>();
        variables.put("NVCF_SECURITY_JWT_TRUSTEDISSUERS_0_ISSUERURI", "https://idp.example.test");
        variables.put("NVCF_SECURITY_JWT_TRUSTEDISSUERS_0_JWKSETURI",
                      "https://idp.example.test/jwks");
        variables.put("NVCF_SECURITY_JWT_TRUSTEDISSUERS_1_ISSUERURI",
                      "https://other.example.test");
        variables.put("NVCF_SECURITY_JWT_TRUSTEDISSUERS_1_JWKSETURI",
                      "https://other.example.test/jwks");

        assertThat(bind(variables).getTrustedIssuers())
                .extracting(TrustedIssuer::getIssuerUri, TrustedIssuer::getJwkSetUri)
                .containsExactly(
                        tuple("https://idp.example.test", "https://idp.example.test/jwks"),
                        tuple("https://other.example.test", "https://other.example.test/jwks"));
    }

    @Test
    void bindsAnEmptyListWhenTheStackEmitsNoTrustedIssuerVariables() {
        assertThat(bind(Map.of()).getTrustedIssuers()).isEmpty();
    }

    private static TrustedJwtIssuerProperties bind(Map<String, Object> variables) {
        var environment = new StandardEnvironment();
        environment.getPropertySources().replace(
                StandardEnvironment.SYSTEM_ENVIRONMENT_PROPERTY_SOURCE_NAME,
                new SystemEnvironmentPropertySource(
                        StandardEnvironment.SYSTEM_ENVIRONMENT_PROPERTY_SOURCE_NAME, variables));
        return Binder.get(environment)
                .bind("nvcf.security.jwt", Bindable.of(TrustedJwtIssuerProperties.class))
                .orElseGet(TrustedJwtIssuerProperties::new);
    }
}
