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
package com.nvidia.icms.configuration.security;

import java.util.ArrayList;
import java.util.List;
import lombok.Data;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Configuration;

/**
 * Binds the set of trusted static JWT issuers under
 * {@code icms.security.jwt.trusted-issuers[]}.
 *
 * <p>Each entry is an {@code {issuer-uri, jwk-set-uri}} pair accepted in addition
 * to the primary {@code issuer-uri} (service-to-service tokens) and the legacy
 * single {@code admin-issuer-uri}. This lets tokens from multiple external issuers
 * be trusted, keyed by the token's {@code iss} claim.</p>
 *
 * <p>An empty list (the default) preserves single/dual-issuer behavior, so
 * existing deployments that only set {@code issuer-uri} (and optionally
 * {@code admin-issuer-uri}) are unaffected.</p>
 */
@Configuration
@ConfigurationProperties(prefix = "icms.security.jwt")
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
