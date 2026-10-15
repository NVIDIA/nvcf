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
package com.nvidia.nvcf.rest.azp;

import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.tuple.ImmutablePair;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
public class TestAuthorizedPartiesService {
    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private AuthorizedPartiesService authorizedPartiesService;

    @Autowired
    private FunctionLookupService functionLookupService;

    public FunctionEntity createTestFunction(
            UUID id,
            UUID versionId,
            String ncaId,
            String name) {
        var entity = FunctionEntity.builder()
                .functionId(id)
                .functionVersionId(versionId)
                .functionName(name)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(ncaId)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE.toString())
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .utilsContainerImage(GO)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .createdAt(Instant.now())
                .build();
        functionsRepository.save(entity);
        return entity;
    }

    public void associateAuthParties(
            String ncaId,
            UUID functionId,
            Optional<UUID> optVersionId,
            Set<AuthorizedPartyDto> authPartiesDtos) {
        var authAccounts = authPartiesDtos.stream()
                .map(AuthorizedPartyDto::ncaId)
                .collect(Collectors.toSet());
        var cachedMapOfFunctionLevelAuthzAccounts =
                Stream.of(functionLookupService.lookupFirstUsingFunctionId(functionId))
                        .filter(function -> !CollectionUtils
                                .isEmpty(function.getFunctionLevelAuthorizedAccounts()))
                        .collect(Collectors.toMap(FunctionEntity::getFunctionId,
                                                  FunctionEntity::getFunctionLevelAuthorizedAccounts));
        var cachedMapOfCurrentVersionLevelAuthzAccounts = optVersionId
                .flatMap(verId -> functionLookupService
                        .lookupUsingFunctionIdAndVersionId(functionId, verId))
                .map(Stream::of)
                .map(stream -> stream
                        .filter(function -> !CollectionUtils
                                .isEmpty(function.getVersionLevelAuthorizedAccounts()))
                        .collect(Collectors.toMap(FunctionEntity::getFunctionVersionId,
                                                  FunctionEntity::getVersionLevelAuthorizedAccounts)))
                .orElse(Map.of());

        authorizedPartiesService.createAuthorizedParties(functionId, optVersionId,
                                                         ncaId, authPartiesDtos.stream().toList());

        // Check if DB is populated with the auth parties.
        optVersionId
                .map(verId -> {
                    var function = functionLookupService
                            .lookupUsingFunctionIdAndVersionIdOrThrow(functionId, verId);
                    assertThat(function.getVersionLevelAuthorizedAccounts())
                            .containsExactlyInAnyOrderElementsOf(authAccounts);
                    var originalFunctionLevelAuthzAccounts =
                            cachedMapOfFunctionLevelAuthzAccounts.get(functionId);
                    if (originalFunctionLevelAuthzAccounts == null) {
                        assertThat(function.getFunctionLevelAuthorizedAccounts()).isNull();
                    } else {
                        assertThat(function.getFunctionLevelAuthorizedAccounts())
                                .containsExactlyInAnyOrderElementsOf(
                                        originalFunctionLevelAuthzAccounts);
                    }
                    return List.of(function);
                })
                .orElseGet(() -> {
                    var functions = functionLookupService.lookupUsingFunctionId(functionId);
                    functions.forEach(function -> {
                        assertThat(function.getFunctionLevelAuthorizedAccounts())
                                .containsExactlyInAnyOrderElementsOf(authAccounts);
                        var originalVersionLevelAuthzAccounts =
                                cachedMapOfCurrentVersionLevelAuthzAccounts
                                        .get(function.getFunctionVersionId());
                        if (originalVersionLevelAuthzAccounts == null) {
                            assertThat(function.getVersionLevelAuthorizedAccounts()).isNull();
                        } else {
                            assertThat(function.getVersionLevelAuthorizedAccounts())
                                    .containsExactlyInAnyOrderElementsOf(
                                            originalVersionLevelAuthzAccounts);
                        }
                    });
                    return functions;
                });
    }

    public List<AuthorizedPartyDto> authorizedParties(
            List<ImmutablePair<String, String>> authParties) {
        return authParties.stream()
                .map(pair -> AuthorizedPartyDto.builder()
                        // .clientId(pair.getLeft())
                        .ncaId(pair.getRight()).build())
                .toList();
    }

    public void verifyAuthAccountsOnFunctionEntities(
            String ncaId,
            UUID functionId,
            Optional<UUID> optVersionId,
            Set<String> expectedFunctionLevelAuthAccounts,
            Set<String> expectedVersionLevelAuthAccounts) {
        var functions = optVersionId
                .map(versionId -> {
                    var function = functionLookupService
                            .lookupUsingFunctionIdAndVersionIdOrThrow(functionId, versionId);
                    return List.of(function);
                })
                .orElseGet(() -> functionLookupService.lookupUsingFunctionId(functionId));

        functions.forEach(function -> {
            if (CollectionUtils.isEmpty(expectedFunctionLevelAuthAccounts)) {
                assertThat(function.getFunctionLevelAuthorizedAccounts()).isNull();
            } else {
                assertThat(function.getFunctionLevelAuthorizedAccounts())
                        .containsExactlyInAnyOrderElementsOf(expectedFunctionLevelAuthAccounts);
            }

            if (CollectionUtils.isEmpty(expectedVersionLevelAuthAccounts)) {
                assertThat(function.getVersionLevelAuthorizedAccounts()).isNull();
            } else {
                assertThat(function.getVersionLevelAuthorizedAccounts())
                        .containsExactlyInAnyOrderElementsOf(expectedVersionLevelAuthAccounts);
            }
        });
    }
}
