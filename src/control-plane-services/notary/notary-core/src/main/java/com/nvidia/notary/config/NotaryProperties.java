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
package com.nvidia.notary.config;

import com.nimbusds.jose.JWSAlgorithm;
import jakarta.annotation.PostConstruct;
import jakarta.validation.constraints.AssertTrue;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Positive;
import java.util.List;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.ToString;
import lombok.extern.slf4j.Slf4j;
import org.hibernate.validator.constraints.URL;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Configuration;
import org.springframework.validation.annotation.Validated;

@Slf4j
@Data
@Builder
@NoArgsConstructor
@AllArgsConstructor
@Configuration
@Validated
@ConfigurationProperties(prefix = "notary", ignoreUnknownFields = false)
public class NotaryProperties {

    @PostConstruct
    private void postConstruct() {
        log.info("recreating service config");
    }

    @NotBlank
    @ToString.Exclude
    private String privateJwks;

    @NotBlank
    private String signingKid;

    @NotNull
    private JWSAlgorithm signingAlgorithm;

    @NotBlank
    @URL
    private String issuerUrl;

    @NotBlank
    private String signingScope;

    @Positive
    private long maxAssertionsRequestSize;

    /**
     * Audiences that must appear in the JWT {@code aud} claim. A token passes the audience check
     * if its {@code aud} contains at least one entry from this list. An empty list disables the
     * audience check entirely — but only when {@link #requireAudience} is {@code false}. With the
     * default {@code requireAudience=true}, an empty list fails startup validation (enforced by
     * {@link #isRequiredAudiencesValid()}) so that a misconfigured deployment cannot silently
     * accept any issuer-valid token. Each entry must be non-blank, not end with {@code :}, and not
     * contain an unresolved {@code ${...}} placeholder.
     *
     * <p>{@code @NotNull} guards against an explicit empty YAML/env value (e.g.
     * {@code notary.required-audiences:}) being coerced to {@code null} by Spring's relaxed
     * binder, which would bypass the element-level constraints and NPE downstream consumers.
     */
    @NotNull
    @Builder.Default
    private List<
            @NotBlank
            @Pattern(regexp = "(?s)^(?!.*\\$\\{).+(?<!:)$",
                    message = "must not end with ':' or contain an unresolved '${...}' placeholder")
            String> requiredAudiences = List.of();

    /**
     * Whether {@link #requiredAudiences} must be non-empty. Defaults to {@code true} so that an
     * operator who forgets to set {@code NOTARY_REQUIRED_AUDIENCES_<N>} fails at startup
     * rather than silently accepting any issuer-valid JWT. Set to {@code false} only when issuer
     * validation alone is sufficient (e.g. the NCP profile).
     */
    @Builder.Default
    private boolean requireAudience = true;

    /**
     * Cross-field constraint: when {@link #requireAudience} is {@code true} (the default),
     * {@link #requiredAudiences} must be non-empty. Method-level {@code @AssertTrue} on an
     * {@code isXxx} method is the Jakarta Bean Validation idiom for cross-field rules — the
     * spec requires a JavaBeans-compliant getter, so the {@code is} prefix is load-bearing.
     * The trade-off is that the JavaBeans introspector exposes {@code requiredAudiencesValid}
     * as a read-only computed property; harmless given {@code ignoreUnknownFields = false} on
     * the class.
     */
    @AssertTrue(message = "notary.required-audiences must be non-empty when "
            + "notary.require-audience=true (the default); set literal values via "
            + "NOTARY_REQUIRED_AUDIENCES_<N> env vars, or set NOTARY_REQUIRE_AUDIENCE=false "
            + "to validate against issuer alone")
    public boolean isRequiredAudiencesValid() {
        return !requireAudience || (requiredAudiences != null && !requiredAudiences.isEmpty());
    }
}
