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
package com.nvidia.nvcf.service.worker;


import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvcf.icms.client.IcmsStubService.DescribeInstancesResponse.Instance;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.token.client.NotaryClient;
import java.util.HashSet;
import java.util.List;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class WorkerNotaryService {

    private final NotaryClient notaryClient;
    private final FunctionLookupService functionLookupService;

    public String validateAndIssueInstanceAssertion(
            UUID functionId,
            UUID functionVersionId,
            Instance instance,
            List<String> ipsList) {
        var instanceId = instance.getInstanceId();
        var containsAll = new HashSet<>(instance.getInstanceIps()).containsAll(ipsList);
        if (!containsAll) {
            throw new ForbiddenException("unknown IPs in request");
        }
        return notaryClient.issueInstanceCredentialAssertionToken(functionId,
                                                           functionVersionId,
                                                           instanceId,
                                                           ipsList);
    }

    public String issueSecretsAssertion(UUID functionId, UUID versionId) {
        var functionEntity = functionLookupService
                .lookupUsingFunctionIdAndVersionIdOrThrow(functionId, versionId);
        var accountTelemetrySecretsPresent = accountTelemetrySecretsPresent(functionId, versionId);
        var functionSecretsPresent = functionEntity.hasSecrets();
        var ncaId = functionEntity.getNcaId();
        var telemetries = functionEntity.getTelemetries();
        if (accountTelemetrySecretsPresent && functionSecretsPresent) {
            return notaryClient.issueSecretPathsAssertionToken(
                    ncaId,
                    functionId,
                    versionId,
                    telemetries);
        } else if (accountTelemetrySecretsPresent) {
            return notaryClient.issueSecretPathsAssertionToken(ncaId, telemetries);
        } else if (functionSecretsPresent) {
            return notaryClient.issueSecretPathsAssertionToken(functionId, versionId);
        }

        return StringUtils.EMPTY;
    }

    public String issueFunctionMetadataAssertion(
            String ncaId,
            UUID functionId,
            UUID functionVersionId) {
        return notaryClient.issueFunctionMetadataAssertionToken(ncaId,
                                                                functionId,
                                                                functionVersionId);
    }

    private boolean accountTelemetrySecretsPresent(UUID functionId, UUID versionId) {
        var functionEntity = functionLookupService
                .lookupUsingFunctionIdAndVersionIdOrThrow(functionId, versionId);
        return functionEntity.getTelemetries() != null;
    }

}
