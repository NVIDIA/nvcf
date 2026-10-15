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


import com.nimbusds.jose.jwk.JWK;
import com.nimbusds.jose.jwk.JWKSet;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

/**
 * Validates runtime dependencies that Jakarta Bean Validation on {@link NotaryProperties} can't
 * express — specifically, that {@code signingKid} resolves to a private key in the loaded
 * {@link JWKSet}. All other field-level constraints (blank/null/positive/URL/audience-element
 * shape, plus the {@code requireAudience}/{@code requiredAudiences} cross-field rule) are
 * enforced declaratively during {@code @ConfigurationProperties} binding.
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class ConfigurationValidator {

    private final NotaryProperties notaryProperties;
    private final JWKSet keySet;

    public void validate() {
        validateSigningKey(keySet);
    }

    private void validateSigningKey(JWKSet keySet) {
        String signingKid = notaryProperties.getSigningKid();
        // Defense-in-depth: @NotBlank on the field handles this in the normal Spring binding
        // path, but a clearer error than "Signing key 'null' not found in key set" is cheap.
        if (StringUtils.isBlank(signingKid)) {
            throw new IllegalStateException("Signing key id is not set.");
        }
        if (keySet.isEmpty()) {
            throw new IllegalStateException("Signing key set is empty.");
        }
        JWK signingKey = keySet.getKeyByKeyId(signingKid);
        if (signingKey == null) {
            throw new IllegalStateException(
                    "Signing key '%s' not found in key set.".formatted(signingKid));
        }
        if (!signingKey.isPrivate()) {
            throw new IllegalStateException("Signing key '%s' is not private.".formatted(signingKid));
        }
    }

}
