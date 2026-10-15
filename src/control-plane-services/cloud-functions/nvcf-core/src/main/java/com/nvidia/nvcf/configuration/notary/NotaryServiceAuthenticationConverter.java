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
import java.util.Objects;
import org.springframework.core.convert.converter.Converter;
import org.springframework.security.authentication.AbstractAuthenticationToken;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.server.resource.authentication.JwtGrantedAuthoritiesConverter;
import org.springframework.util.Assert;
import tools.jackson.databind.json.JsonMapper;

/**
 * handles Notary Service token used for function invocation
 */
public final class NotaryServiceAuthenticationConverter implements
        Converter<Jwt, AbstractAuthenticationToken> {

    private Converter<Jwt, Collection<GrantedAuthority>> jwtGrantedAuthoritiesConverter =
                    new JwtGrantedAuthoritiesConverter();

    private static final JsonMapper JSON_MAPPER = new JsonMapper();


    @Override
    public AbstractAuthenticationToken convert(Jwt jwt) {
        var authorities = Objects.requireNonNull(this.jwtGrantedAuthoritiesConverter.convert(jwt))
                .stream().toList();
        var assertion = JSON_MAPPER.convertValue(jwt.getClaim("assertion"),
                                                 InvocationAssertion.class);
        return new NotaryServiceAuthenticationToken(jwt, authorities, assertion);
    }

    public void setJwtGrantedAuthoritiesConverter(
            Converter<Jwt, Collection<GrantedAuthority>> jwtGrantedAuthoritiesConverter) {
        Assert.notNull(jwtGrantedAuthoritiesConverter, "jwtGrantedAuthoritiesConverter cannot be null");
        this.jwtGrantedAuthoritiesConverter = jwtGrantedAuthoritiesConverter;
    }

}