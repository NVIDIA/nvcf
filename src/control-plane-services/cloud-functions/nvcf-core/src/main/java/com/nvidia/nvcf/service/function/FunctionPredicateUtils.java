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
package com.nvidia.nvcf.service.function;

import static com.nvidia.nvcf.service.apikeys.ApiKeysService.isApiKeyAuth;

import com.nvidia.nvcf.configuration.notary.NotaryServiceAuthenticationToken;
import com.nvidia.nvcf.configuration.notary.NotaryServiceAuthenticationToken.AllowedVersions;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;

@Slf4j
@UtilityClass
public class FunctionPredicateUtils {

    public static boolean authorizedFunctionMatch(
            String ncaId,
            Authentication authentication,
            FunctionEntity function) {
        var functionLevelAuthCheck =
                Objects.requireNonNullElse(function.getFunctionLevelAuthorizedAccounts(),
                                           Set.<String>of()).contains(ncaId);
        var versionLevelAuthCheck =
                Objects.requireNonNullElse(function.getVersionLevelAuthorizedAccounts(),
                                           Set.<String>of()).contains(ncaId);

        return isApiKeyAuth(authentication).map(access -> {
            if (access.azpFunctionsAllowed()
                    && (functionLevelAuthCheck || versionLevelAuthCheck)) {
                return true;
            }

            var id = function.getFunctionId();
            var versionId = function.getFunctionVersionId();
            var resourceCheck = access.hasResourcesScopedForFunction(id, versionId);
            return resourceCheck && (functionLevelAuthCheck || versionLevelAuthCheck);
        }).orElseGet(() -> functionLevelAuthCheck || versionLevelAuthCheck);
    }

    public static boolean privateFunctionMatch(
            String ncaId,
            Authentication authentication,
            FunctionEntity function) {
        return isApiKeyAuth(authentication).map(access -> {
            if (access.privateFunctionsAllowed() && ncaId.equals(function.getNcaId())) {
                return true;
            }
            return ncaId.equals(function.getNcaId())
                    && access.hasResourcesScopedForFunction(function.getFunctionId(),
                                                            function.getFunctionVersionId());
        }).orElseGet(() -> {
            if (authentication instanceof NotaryServiceAuthenticationToken notaryToken) {
                return validateNotaryAssertion(function,
                                               notaryToken.getNcaId(),
                                               notaryToken.getFunctionIdToFunctionVersions());
            }
            return function.getNcaId().equals(ncaId);
        });
    }

    public static boolean createFunctionMatch(
            String ncaId,
            Authentication authentication,
            Optional<FunctionEntity> optFunction) {
        return isApiKeyAuth(authentication).map(access -> {
            if (access.privateFunctionsAllowed()) {
                return true;
            }

            return optFunction
                    .map(functionEntity -> {
                        var functionId = functionEntity.getFunctionId();
                        return functionEntity.getNcaId().equals(ncaId)
                                && access.hasResourcesScopedForFunction(functionId, null);
                    })
                    .orElse(false);
        }).orElseGet(() -> true);  // No-op for JWT and other auth tokens
    }

    private static boolean validateNotaryAssertion(
            FunctionEntity functionEntity,
            String ncaId,
            Map<UUID, AllowedVersions> functionIdToFunctionVersions) {
        if (!functionEntity.getNcaId().equals(ncaId)) {
            log.debug("Notary Service token has invalid ncaId '{}'", ncaId);
            return false;
        }

        var targetFunctionId = functionEntity.getFunctionId();
        var targetFunctionVersionId = functionEntity.getFunctionVersionId();
        var versionIds = functionIdToFunctionVersions.get(targetFunctionId);
        if (versionIds == null) {
            log.debug("Notary Service token has invalid functionId '{}'",
                      functionIdToFunctionVersions);
            return false;
        }

        if (!versionIds.isAllowed(targetFunctionVersionId)) {
            log.debug("Notary Service token has invalid functionVersionId '{}'",
                      functionIdToFunctionVersions);
            return false;
        }

        return true;
    }

}
