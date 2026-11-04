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
package com.nvidia.nvcf.service.azp;

import static com.nvidia.nvcf.service.azp.AuthorizedPartiesService.AUTHORIZED_WILDCARD_ACCOUNT;

import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartiesByFunctionDto;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@UtilityClass
public class AuthorizedPartiesMapperUtils {

    public static AuthorizedPartiesByFunctionDto toAuthorizedPartiesByFunctionDto(
            FunctionEntity entity,
            Optional<UUID> optVersionId) {
        Set<String> authAccounts;
        if (optVersionId.isPresent()) {
            authAccounts =
                    Objects.requireNonNullElse(entity.getVersionLevelAuthorizedAccounts(),
                                               Set.<String>of());
        } else {
            authAccounts =
                    Objects.requireNonNullElse(entity.getFunctionLevelAuthorizedAccounts(),
                                               Set.<String>of());
        }
        var authPartiesDtos = authAccounts.stream()
                .map(AuthorizedPartiesMapperUtils::toAuthorizedPartyDto)
                .toList();
        return AuthorizedPartiesByFunctionDto.builder()
                .id(entity.getFunctionId())
                .versionId(optVersionId.orElse(null))
                .ncaId(entity.getNcaId())
                .authorizedParties(authPartiesDtos)
                .build();
    }

    public static AuthorizedPartyDto toAuthorizedPartyDto(String authNcaId) {
        // Map AUTHORIZED_WILDCARD_ACCOUNT in the DB to "*" in the DTO.
        return AuthorizedPartyDto.builder()
                .ncaId(authNcaId.equals(AUTHORIZED_WILDCARD_ACCOUNT) ? "*" : authNcaId)
                .build();
    }

}
