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

package com.nvidia.apikeys.persistance.dao;

import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.persistance.repositories.OwnerStatusByAccountAndServiceRepository;
import com.nvidia.apikeys.persistance.repositories.OwnerStatusByAccountRepository;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import java.time.Instant;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

/**
 * Owner status scoped to an account, and optionally to one issuer service within the account.
 * A missing row means the owner is active.
 */
@Service
@RequiredArgsConstructor
public class OwnerStatusByAccountDao {

    private final OwnerStatusByAccountRepository accountRepository;
    private final OwnerStatusByAccountAndServiceRepository serviceRepository;

    public Optional<OwnerStatusByAccountModel> getAccountStatus(
            String ncaId, KeyOwnerType ownerType, String ownerId) {
        return accountRepository.findByNcaIdAndOwnerTypeAndOwnerId(ncaId, ownerType, ownerId);
    }

    public Optional<OwnerStatusByAccountAndServiceModel> getServiceStatus(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId) {
        return serviceRepository.findByNcaIdAndOwnerTypeAndOwnerIdAndIssuerServiceId(
                ncaId, ownerType, ownerId, issuerServiceId);
    }

    public OwnerStatusByAccountModel saveAccountStatus(
            String ncaId, KeyOwnerType ownerType, String ownerId, KeyOwnerStatus status) {
        var now = Instant.now();
        var createdAt = getAccountStatus(ncaId, ownerType, ownerId)
                .map(OwnerStatusByAccountModel::getCreatedAt)
                .orElse(now);
        return accountRepository.save(OwnerStatusByAccountModel.builder()
                                              .ncaId(ncaId)
                                              .ownerType(ownerType)
                                              .ownerId(ownerId)
                                              .ownerStatus(status)
                                              .createdAt(createdAt)
                                              .updatedAt(now)
                                              .build());
    }

    public OwnerStatusByAccountAndServiceModel saveServiceStatus(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            KeyOwnerStatus status) {
        var now = Instant.now();
        var createdAt = getServiceStatus(ncaId, ownerType, ownerId, issuerServiceId)
                .map(OwnerStatusByAccountAndServiceModel::getCreatedAt)
                .orElse(now);
        return serviceRepository.save(OwnerStatusByAccountAndServiceModel.builder()
                                              .ncaId(ncaId)
                                              .ownerType(ownerType)
                                              .ownerId(ownerId)
                                              .issuerServiceId(issuerServiceId)
                                              .ownerStatus(status)
                                              .createdAt(createdAt)
                                              .updatedAt(now)
                                              .build());
    }

    /**
     * SUSPENDED if the owner is suspended for the whole account or for the issuer service.
     */
    public KeyOwnerStatus getEffectiveStatus(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId) {
        var accountSuspended = getAccountStatus(ncaId, ownerType, ownerId)
                .map(OwnerStatusByAccountModel::getOwnerStatus)
                .filter(KeyOwnerStatus.SUSPENDED::equals)
                .isPresent();
        if (accountSuspended) {
            return KeyOwnerStatus.SUSPENDED;
        }
        return getServiceStatus(ncaId, ownerType, ownerId, issuerServiceId)
                .map(OwnerStatusByAccountAndServiceModel::getOwnerStatus)
                .filter(KeyOwnerStatus.SUSPENDED::equals)
                .orElse(KeyOwnerStatus.ACTIVE);
    }
}
