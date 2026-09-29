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

import java.util.ArrayList;
import java.util.List;
import lombok.Data;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;

/**
 * Binds the set of trusted static JWT issuers under
 * {@code nvcf.security.jwt.trusted-issuers[]}.
 *
 * <p>Each entry is an {@code {issuer-uri, jwk-set-uri}} pair accepted <em>in addition</em> to
 * the primary {@code spring.security.oauth2.resourceserver.jwt.issuer-uri}. This lets a
 * self-managed deployment trust JWTs from its own identity provider while the built-in
 * OpenBao issuer keeps working for service-to-service traffic.</p>
 *
 * <p>An empty list (the default) preserves single-issuer behavior, so existing deployments
 * that only set {@code issuer-uri} / {@code jwk-set-uri} are unaffected.</p>
 *
 * <p>Refresh-scoped: entries can be added or removed at runtime and
 * {@code AuthManagerResolverConfiguration#authenticationManagerResolver()} is rebuilt from
 * the new value without a restart.</p>
 *
 * <p>Mirrors {@code icms.security.jwt.trusted-issuers[]} in instance-cluster-management so the
 * two services share one configuration convention.</p>
 */
@RefreshScope
@Configuration
@ConfigurationProperties(prefix = "nvcf.security.jwt")
@Data
public class TrustedJwtIssuerProperties {

    /** Additional trusted static JWT issuers, keyed by {@code iss} at resolution time. */
    private List<TrustedIssuer> trustedIssuers = new ArrayList<>();

    @Data
    public static class TrustedIssuer {

        /** The {@code iss} claim value to trust (must match the token exactly). */
        private String issuerUri;

        /** JWKS endpoint used to verify signatures for tokens from {@link #issuerUri}. */
        private String jwkSetUri;
    }
}
