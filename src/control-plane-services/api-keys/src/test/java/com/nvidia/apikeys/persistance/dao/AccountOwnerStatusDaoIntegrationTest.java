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

import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.vo.KeyOwnerStatus.ACTIVE;
import static com.nvidia.apikeys.vo.KeyOwnerStatus.SUSPENDED;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.utils.TestClock;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneId;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
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
class AccountOwnerStatusDaoIntegrationTest {

    private static final String NCA_1 = "nca-1";
    private static final String NCA_2 = "nca-2";
    private static final String OWNER = "owner-1@example.com";
    private static final String SERVICE_A = "service-a";
    private static final String SERVICE_B = "service-b";

    @Autowired
    private AccountOwnerStatusDao dao;

    @BeforeEach
    void setUp() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));
    }

    @AfterEach
    void tearDown() {
        TestClock.resetToDefaults();
    }

    @Test
    void missingStatusIsActive() {
        assertThat(dao.getAccountStatus(NCA_1, USER, OWNER)).isEmpty();
        assertThat(dao.getServiceStatus(NCA_1, USER, OWNER, SERVICE_A)).isEmpty();
        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(ACTIVE);
    }

    @Test
    void accountSuspensionAppliesToEveryIssuerInThatAccount() {
        dao.saveAccountStatus(NCA_1, USER, OWNER, SUSPENDED);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(SUSPENDED);
        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_B)).isEqualTo(SUSPENDED);
        assertThat(dao.getEffectiveStatus(NCA_2, USER, OWNER, SERVICE_A)).isEqualTo(ACTIVE);
    }

    @Test
    void serviceSuspensionAppliesOnlyToThatIssuerAndAccount() {
        dao.saveServiceStatus(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(SUSPENDED);
        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_B)).isEqualTo(ACTIVE);
        assertThat(dao.getEffectiveStatus(NCA_2, USER, OWNER, SERVICE_A)).isEqualTo(ACTIVE);
    }

    @Test
    void accountSuspensionWinsOverActiveServiceStatus() {
        dao.saveAccountStatus(NCA_1, USER, OWNER, SUSPENDED);
        dao.saveServiceStatus(NCA_1, USER, OWNER, SERVICE_A, ACTIVE);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(SUSPENDED);
    }

    @Test
    void serviceSuspensionWinsOverActiveAccountStatus() {
        dao.saveAccountStatus(NCA_1, USER, OWNER, ACTIVE);
        dao.saveServiceStatus(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(SUSPENDED);
        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_B)).isEqualTo(ACTIVE);
    }

    @Test
    void reactivatingAccountRestoresActiveStatus() {
        dao.saveAccountStatus(NCA_1, USER, OWNER, SUSPENDED);
        dao.saveAccountStatus(NCA_1, USER, OWNER, ACTIVE);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, OWNER, SERVICE_A)).isEqualTo(ACTIVE);
    }

    @Test
    void suspensionDoesNotAffectOtherOwnersInTheAccount() {
        dao.saveAccountStatus(NCA_1, USER, OWNER, SUSPENDED);
        dao.saveServiceStatus(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.getEffectiveStatus(NCA_1, USER, "owner-2@example.com", SERVICE_A))
                .isEqualTo(ACTIVE);
        assertThat(dao.getAccountStatus(NCA_1, USER, "owner-2@example.com")).isEmpty();
    }

    @Test
    void serviceStatusDoesNotCreateAccountStatus() {
        dao.saveServiceStatus(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        assertThat(dao.getAccountStatus(NCA_1, USER, OWNER)).isEmpty();
        assertThat(dao.getServiceStatus(NCA_1, USER, OWNER, SERVICE_B)).isEmpty();
        assertThat(dao.getServiceStatus(NCA_2, USER, OWNER, SERVICE_A)).isEmpty();
    }

    @Test
    void saveReturnsStoredRow() {
        OwnerStatusByAccountModel saved = dao.saveAccountStatus(NCA_1, USER, OWNER, SUSPENDED);

        assertThat(dao.getAccountStatus(NCA_1, USER, OWNER)).contains(saved);
        assertThat(dao.saveServiceStatus(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED))
                .satisfies(status -> assertThat(dao.getServiceStatus(
                        NCA_1, USER, OWNER, SERVICE_A)).contains(status));
    }

    @Test
    void reactivatingKeepsCreatedAtAndRefreshesUpdatedAt() {
        dao.saveAccountStatus(NCA_1, USER, OWNER, SUSPENDED);

        Instant later = TEST_TIME.plus(Duration.ofHours(1));
        TestClock.setBaseClock(TestClock.fixed(later, ZoneId.systemDefault()));
        dao.saveAccountStatus(NCA_1, USER, OWNER, ACTIVE);

        assertThat(dao.getAccountStatus(NCA_1, USER, OWNER))
                .get()
                .satisfies(status -> {
                    assertThat(status.getOwnerStatus()).isEqualTo(ACTIVE);
                    assertThat(status.getCreatedAt()).isEqualTo(TEST_TIME);
                    assertThat(status.getUpdatedAt()).isEqualTo(later);
                });
        assertThat(dao.getAccountStatus(NCA_1, USER, OWNER))
                .map(OwnerStatusByAccountModel::getNcaId)
                .contains(NCA_1);
    }

    @Test
    void serviceStatusKeepsCreatedAtOnUpdate() {
        dao.saveServiceStatus(NCA_1, USER, OWNER, SERVICE_A, SUSPENDED);

        Instant later = TEST_TIME.plus(Duration.ofHours(1));
        TestClock.setBaseClock(TestClock.fixed(later, ZoneId.systemDefault()));
        dao.saveServiceStatus(NCA_1, USER, OWNER, SERVICE_A, ACTIVE);

        assertThat(dao.getServiceStatus(NCA_1, USER, OWNER, SERVICE_A))
                .get()
                .satisfies(status -> {
                    assertThat(status.getOwnerStatus()).isEqualTo(ACTIVE);
                    assertThat(status.getCreatedAt()).isEqualTo(TEST_TIME);
                    assertThat(status.getUpdatedAt()).isEqualTo(later);
                });
    }
}
