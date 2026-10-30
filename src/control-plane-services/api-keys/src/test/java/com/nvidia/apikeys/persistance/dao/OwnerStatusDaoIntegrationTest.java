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

import static com.nvidia.apikeys.vo.KeyOwnerStatus.ACTIVE;
import static com.nvidia.apikeys.vo.KeyOwnerStatus.SUSPENDED;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.tuple;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import java.time.Instant;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = App.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class OwnerStatusDaoIntegrationTest {

    private static final String NCA_1 = "nca-1";
    private static final String NCA_2 = "nca-2";
    private static final String OWNER = "owner-1@example.com";
    private static final String SERVICE_A = "service-a";
    private static final String SERVICE_B = "service-b";

    @Autowired
    private OwnerStatusByAccountDao dao;

    @Test
    void missingStatusIsActive() {
        assertThat(dao.getOwnerStatusByAccount(NCA_1, USER, OWNER)).isEmpty();
        assertThat(dao.getOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A)).isEmpty();
        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(ACTIVE);
    }

    @Test
    void accountSuspensionAppliesToEveryIssuerInThatAccount() {
        dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, SUSPENDED);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(SUSPENDED);
        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_B)).isEqualTo(SUSPENDED);
        assertThat(dao.getEffectiveStatus(NCA_2, USER, OWNER, SERVICE_A)).isEqualTo(ACTIVE);
    }

    @Test
    void serviceSuspensionAppliesOnlyToThatIssuerAndAccount() {
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(SUSPENDED);
        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_B)).isEqualTo(ACTIVE);
        assertThat(dao.getEffectiveStatus(NCA_2, USER, OWNER, SERVICE_A)).isEqualTo(ACTIVE);
    }

    @Test
    void ownerStatusByAccountWinsOverActiveOwnerStatusByAccountAndService() {
        dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, SUSPENDED);
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, ACTIVE);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(SUSPENDED);
    }

    @Test
    void ownerStatusByAccountAndServiceWinsOverActiveOwnerStatusByAccount() {
        dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, ACTIVE);
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(SUSPENDED);
        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_B)).isEqualTo(ACTIVE);
    }

    @Test
    void listOwnerStatusByAccountSpansAccountsForOneOwner() {
        dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, SUSPENDED);
        dao.saveOwnerStatusByAccount(NCA_2, USER, OWNER, ACTIVE);
        dao.saveOwnerStatusByAccount(NCA_1, USER, "owner-2@example.com", SUSPENDED);
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.listOwnerStatusByAccount(USER, OWNER))
                .extracting(OwnerStatusByAccountModel::getNcaId,
                            OwnerStatusByAccountModel::getOwnerStatus)
                .containsExactlyInAnyOrder(
                        tuple(NCA_1, SUSPENDED),
                        tuple(NCA_2, ACTIVE));
        assertThat(dao.listOwnerStatusByAccount(USER, "unknown-owner")).isEmpty();
    }

    @Test
    void listOwnerStatusByAccountAndServiceSpansAccountsAndServices() {
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_B, ACTIVE);
        dao.saveOwnerStatusByAccountAndService(NCA_2, USER, OWNER, SERVICE_A, SUSPENDED);
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, "owner-2@example.com", SERVICE_A,
                                               SUSPENDED);
        dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, SUSPENDED);

        List<OwnerStatusByAccountAndServiceModel> all =
                dao.listOwnerStatusByAccountAndService(USER, OWNER);

        assertThat(all)
                .extracting(OwnerStatusByAccountAndServiceModel::getNcaId,
                            OwnerStatusByAccountAndServiceModel::getIssuerServiceId)
                .containsExactlyInAnyOrder(
                        tuple(NCA_1, SERVICE_A),
                        tuple(NCA_1, SERVICE_B),
                        tuple(NCA_2, SERVICE_A));
    }

    @Test
    void listOwnerStatusByAccountAndServiceCanBeScopedToOneAccount() {
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_B, ACTIVE);
        dao.saveOwnerStatusByAccountAndService(NCA_2, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.listOwnerStatusByAccountAndService(NCA_1, USER, OWNER))
                .extracting(OwnerStatusByAccountAndServiceModel::getIssuerServiceId)
                .containsExactlyInAnyOrder(SERVICE_A, SERVICE_B);
        assertThat(dao.listOwnerStatusByAccountAndService("unknown-nca", USER, OWNER)).isEmpty();
    }

    @Test
    void reactivatingAccountRestoresActiveStatus() {
        dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, SUSPENDED);
        dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, ACTIVE);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(ACTIVE);
    }

    @Test
    void suspensionDoesNotAffectOtherOwnersInTheAccount() {
        dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, SUSPENDED);
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, "owner-2@example.com", SERVICE_A))
                .isEqualTo(ACTIVE);
        assertThat(dao.getOwnerStatusByAccount(NCA_1, USER, "owner-2@example.com")).isEmpty();
    }

    @Test
    void ownerStatusByAccountAndServiceDoesNotCreateOwnerStatusByAccount() {
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.getOwnerStatusByAccount(NCA_1, USER, OWNER)).isEmpty();
        assertThat(dao.getOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_B)).isEmpty();
        assertThat(dao.getOwnerStatusByAccountAndService(NCA_2, USER, OWNER, SERVICE_A)).isEmpty();
    }

    @Test
    void saveReturnsStoredRow() {
        OwnerStatusByAccountModel saved = dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, SUSPENDED);
        OwnerStatusByAccountAndServiceModel savedService =
                dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.getOwnerStatusByAccount(NCA_1, USER, OWNER))
                .get()
                .usingRecursiveComparison()
                .ignoringFields("createdAt", "updatedAt")
                .isEqualTo(saved);
        assertThat(dao.getOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A))
                .get()
                .usingRecursiveComparison()
                .ignoringFields("createdAt", "updatedAt")
                .isEqualTo(savedService);
    }

    @Test
    void reactivatingKeepsCreatedAtAndRefreshesUpdatedAt() {
        dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, SUSPENDED);
        Instant createdAt = dao.getOwnerStatusByAccount(NCA_1, USER, OWNER)
                .map(OwnerStatusByAccountModel::getCreatedAt)
                .orElseThrow();

        dao.saveOwnerStatusByAccount(NCA_1, USER, OWNER, ACTIVE);

        assertThat(dao.getOwnerStatusByAccount(NCA_1, USER, OWNER))
                .get()
                .satisfies(status -> {
                    assertThat(status.getOwnerStatus()).isEqualTo(ACTIVE);
                    assertThat(status.getCreatedAt()).isEqualTo(createdAt);
                    assertThat(status.getUpdatedAt()).isNotNull();
                });
        assertThat(dao.getOwnerStatusByAccount(NCA_1, USER, OWNER))
                .map(OwnerStatusByAccountModel::getNcaId)
                .contains(NCA_1);
    }

    @Test
    void ownerStatusByAccountAndServiceKeepsCreatedAtOnUpdate() {
        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);
        Instant createdAt = dao.getOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A)
                .map(OwnerStatusByAccountAndServiceModel::getCreatedAt)
                .orElseThrow();

        dao.saveOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A, ACTIVE);

        assertThat(dao.getOwnerStatusByAccountAndService(NCA_1, USER, OWNER, SERVICE_A))
                .get()
                .satisfies(status -> {
                    assertThat(status.getOwnerStatus()).isEqualTo(ACTIVE);
                    assertThat(status.getCreatedAt()).isEqualTo(createdAt);
                    assertThat(status.getUpdatedAt()).isNotNull();
                });
    }
}
