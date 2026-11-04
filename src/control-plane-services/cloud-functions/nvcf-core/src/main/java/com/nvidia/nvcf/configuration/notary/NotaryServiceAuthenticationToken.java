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
package com.nvidia.nvcf.configuration.notary;

import com.nvidia.nvcf.service.token.client.NotaryService.InvocationAssertion;
import java.util.Collection;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.Getter;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.Transient;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.server.resource.authentication.AbstractOAuth2TokenAuthenticationToken;

/**
 * handles Notary Service token used for function invocation. Copied largely from
 * {@link org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;)}
 */
@Transient
public class NotaryServiceAuthenticationToken extends AbstractOAuth2TokenAuthenticationToken<Jwt> {

    private final String name;

    @Getter
    private final String ncaId;

    @Getter
    private final Map<UUID, AllowedVersions> functionIdToFunctionVersions;


    public NotaryServiceAuthenticationToken(Jwt jwt,
                                            Collection<? extends GrantedAuthority> authorities,
                                            InvocationAssertion assertion) {
        super(jwt, authorities);
        this.setAuthenticated(true);
        this.name = assertion.clientId();
        this.ncaId = assertion.ncaId();

        Map<UUID, AllowedVersions> intendedFunctions = new HashMap<>();
        // Add the functionId and functionVersionId to the map
        var functionId = assertion.functionId();
        var functionVersionId = assertion.functionVersionId();
        if (functionId != null) {
            intendedFunctions.put(functionId,
                                  functionVersionId == null ?
                                          WildcardVersions.INSTANCE :
                                          new SpecificVersions(functionVersionId));
        }

        if (assertion.intendedFunctions() != null) {
            for (var function : assertion.intendedFunctions()) {
                var key = function.functionId();
                var value = function.functionVersionId();
                if (intendedFunctions.containsKey(key)) {
                    var currentValue = intendedFunctions.get(key);
                    switch (currentValue) {
                        case SpecificVersions sv: {
                            if (value == null) {
                                intendedFunctions.put(key, WildcardVersions.INSTANCE);
                            } else {
                                sv.addVersion(value);
                                intendedFunctions.put(key, sv);
                            }
                            break;
                        }
                        case WildcardVersions ignored: {
                            // no-op
                            break;
                        }
                    }
                } else {
                    intendedFunctions.put(key,
                                          value == null ?
                                                  WildcardVersions.INSTANCE :
                                                  new SpecificVersions(value));
                }
            }
        }

        this.functionIdToFunctionVersions = intendedFunctions;
    }

    @Override
    public Map<String, Object> getTokenAttributes() {
        return this.getToken().getClaims();
    }

    /**
     * The principal name which is, by default, the {@link Jwt}'s subject
     */
    @Override
    public String getName() {
        return this.name;
    }


    public sealed interface AllowedVersions {

        boolean isAllowed(UUID version);
    }

    public static final class WildcardVersions implements AllowedVersions {

        public static final WildcardVersions INSTANCE = new WildcardVersions();

        @Override
        public boolean isAllowed(UUID version) {
            return true;
        }
    }

    public static final class SpecificVersions implements AllowedVersions {

        private final Set<UUID> versions = new HashSet<>();

        public SpecificVersions(UUID version) {
            this.addVersion(version);
        }

        @Override
        public boolean isAllowed(UUID version) {
            return versions.contains(version);
        }

        public void addVersion(UUID version) {
            this.versions.add(version);
        }
    }

}
