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
package com.nvidia.icms.service.byoc;

import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import jakarta.annotation.Nullable;
import java.util.HashSet;
import java.util.Objects;
import java.util.Set;
import lombok.AccessLevel;
import lombok.NoArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

@Slf4j
@NoArgsConstructor(access = AccessLevel.PRIVATE)
public final class ClusterValidation {

    public static void validateNcaId(String expected, String actual) {
        if (!expected.equals(actual)) {
            String errorMsg = String.format(
                    "There exists an entry for the cluster with different ncaId. "
                            + "Specified ncaId %s, existing ncaId %s",
                    actual, expected);
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    public static void validateNcaIdDoNotBelongsToAuthorizedNcaIds(
            Set<String> authorizedNcaIds, String ncaId) {
        if (authorizedNcaIds == null) {
            String errorMsg = "Specified list authorizedNcaIds is null.";
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
        if (authorizedNcaIds.contains(ncaId)) {
            String errorMsg = String.format(
                    "Specified ncaId %s is duplicated in the set of authorized ncaIds.", ncaId);
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    public static void validateAuthorizedNcaIdsForNewCluster(@Nullable Set<String> actual) {
        if (actual == null) {
            String errorMsg = "List of authorized nca ids is null.";
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
        if (actual.contains("*") && actual.size() != 1) {
            String errorMsg =
                    "If specified authorized nca ids contains * then it should not have other entries.";
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
        for (String ncaId : actual) {
            if (StringUtils.isBlank(ncaId)) {
                String errorMsg = "Specified authorized nca ids should not contain empty string.";
                log.error(errorMsg);
                throw new IcmsConflictException(errorMsg);
            }
        }
    }

    public static void validateInstanceTypesV5(
            Set<InstanceTypeV5Udt> expected, Set<InstanceTypeV5Udt> actual,
            boolean isNvca2Flow, String gpuName) {
        Set<InstanceTypeV5Udt> unmatched = new HashSet<>(actual);
        for (InstanceTypeV5Udt exp : expected) {
            InstanceTypeV5Udt found = null;
            for (InstanceTypeV5Udt act : unmatched) {
                if (act.getName().equals(exp.getName())
                        && act.getValue().equals(exp.getValue())
                        && act.getDescription().equals(exp.getDescription())
                        && Objects.equals(act.getIsDefault(), exp.getIsDefault())
                        && act.getGpuMemory().equals(exp.getGpuMemory())
                        && act.getSystemMemory().equals(exp.getSystemMemory())
                        && act.getCpuCores() == exp.getCpuCores()) {
                    found = act;
                }
            }
            if (found == null) {
                String errorMsg = isNvca2Flow
                        ? String.format(
                                "Specified instance type spec is not matching with the %s instance "
                                        + "type spec of the cluster sharing %s gpu in cluster group",
                                exp.getName(), gpuName)
                        : "Specified instance type spec is not matching with the instance type "
                                + "spec of the cluster group";
                log.error(errorMsg);
                throw new IcmsConflictException(errorMsg);
            }
            unmatched.remove(found);
        }
        if (!unmatched.isEmpty()) {
            String errorMsg = isNvca2Flow
                    ? String.format(
                            "Number of instance types is not matching with the number of existing "
                                    + "instance types of the cluster sharing %s gpus in cluster group",
                            gpuName)
                    : "Number of instance types is not matching with the number of existing "
                            + "instance types of the cluster group";
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }
}
