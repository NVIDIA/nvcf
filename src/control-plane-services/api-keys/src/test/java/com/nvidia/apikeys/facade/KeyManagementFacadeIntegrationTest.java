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


package com.nvidia.apikeys.facade;

import static com.nvidia.apikeys.TestData.KEY_AUTHZ_1;
import static com.nvidia.apikeys.TestData.KEY_DESCRIPTION_1;
import static com.nvidia.apikeys.TestData.KEY_EXPIRES_AT_1;
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.NCA_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.config.IntegrationTestConfiguration.KEY_SPACE;
import static com.nvidia.apikeys.facade.KeyManagementActor.ACCOUNT_ADMIN;
import static com.nvidia.apikeys.facade.KeyManagementActor.KEY_OWNER;
import static com.nvidia.apikeys.facade.KeyManagementActor.PLATFORM_ADMIN;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.dto.keysV2.CreateKeyRequest;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerStatusesResponse;
import com.nvidia.apikeys.dto.keysV2.KeyResponse;
import com.nvidia.apikeys.dto.keysV2.KeyStatusV2;
import com.nvidia.apikeys.dto.keysV2.OwnerStatusByAccount;
import com.nvidia.apikeys.dto.keysV2.SettableKeyStatus;
import com.nvidia.apikeys.dto.keysV2.UpdateAuthorizationsRequest;
import com.nvidia.apikeys.dto.keysV2.UpdateOwnerStatusForAccountAndIssuerServiceRequest;
import com.nvidia.apikeys.dto.keysV2.UpdateOwnerStatusForAccountRequest;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import java.time.ZoneId;
import java.util.ArrayList;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = App.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class KeyManagementFacadeIntegrationTest {

    // apikeys.nca-id and the apikeys.service-id-map key for SERVICE_ID_1 in
    // application-integrationtest.yaml
    private static final String LEGACY_NCA_ID = "test-nca-id";
    private static final String LEGACY_CANONICAL_SERVICE_ID = "nvcf";
    private static final String NCA_ID_2 = "nca-2";
    private static final String NCA_ID_3 = "nca-3";
    private static final String USER_ID_2 = "user-2";
    private static final JsonMapper JSON_MAPPER = new JsonMapper();

    @Autowired
    private KeyManagementFacade facade;

    @Autowired
    private KeysDao keysDao;

    @BeforeEach
    void setUp() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));
    }

    @AfterEach
    void tearDown() {
        TestClock.resetToDefaults();
    }

    @Test
    void createReturnsFullValueAndLaterReadsReturnMaskedValue() {
        var created = create(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1);

        assertThat(created.getValue()).startsWith("nvcfapi-test-").doesNotContain("*");
        assertThat(created.getStatus()).isEqualTo(KeyStatusV2.ACTIVE);
        assertThat(created.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
        assertThat(created.getAccountId()).isEqualTo(NCA_ID_1);
        assertThat(created.getIssuerServiceId()).isEqualTo(SERVICE_ID_1);
        assertThat(created.getAuthorizations()).isEqualTo(JSON_MAPPER.readTree(KEY_AUTHZ_1));

        var fetched = get(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1, created.getId());
        assertThat(fetched.getValue()).contains("**********").isNotEqualTo(created.getValue());
        assertThat(fetched.getId()).isEqualTo(created.getId());
        assertThat(fetched.getAuthorizations()).isEqualTo(created.getAuthorizations());
    }

    @Test
    void sameUserInTwoAccountsKeepsKeysSeparate() {
        var first = create(ACCOUNT_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1);
        var second = create(ACCOUNT_ADMIN, NCA_ID_2, USER_KEY_OWNER_ID_1);

        assertThat(facade.listKeys(ACCOUNT_ADMIN, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1)
                           .getKeys())
                .extracting(KeyResponse::getId).containsExactly(first.getId());
        assertThat(facade.listKeys(ACCOUNT_ADMIN, NCA_ID_2, SERVICE_ID_1, USER_KEY_OWNER_ID_1)
                           .getKeys())
                .extracting(KeyResponse::getId).containsExactly(second.getId());
    }

    @Test
    void singleKeyRoutesReturnNotFoundWhenRouteDoesNotMatchStoredKey() {
        var key = create(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1);

        assertThrowsNotFound(() -> get(KEY_OWNER, NCA_ID_2, USER_KEY_OWNER_ID_1, key.getId()));
        assertThrowsNotFound(() -> get(KEY_OWNER, NCA_ID_1, USER_ID_2, key.getId()));
        assertThrowsNotFound(() -> facade.getKey(
                PLATFORM_ADMIN, NCA_ID_1, "other-service", USER_KEY_OWNER_ID_1, key.getId()));
        assertThrowsNotFound(() -> facade.deleteKey(
                ACCOUNT_ADMIN, NCA_ID_2, SERVICE_ID_1, USER_KEY_OWNER_ID_1, key.getId()));
        assertThat(get(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1, key.getId())).isNotNull();
    }

    @Test
    void keyOwnerIsDeniedWhenSuspendedButAdministratorsStillManageKeys() {
        var existing = create(ACCOUNT_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1);
        suspendAccount(NCA_ID_1, USER_KEY_OWNER_ID_1);

        assertForbidden(() -> create(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1));
        assertForbidden(() -> get(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1, existing.getId()));
        assertForbidden(() -> facade.listKeys(
                KEY_OWNER, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1));
        assertForbidden(() -> facade.updateKeyStatus(
                KEY_OWNER, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1, existing.getId(),
                SettableKeyStatus.REVOKED));
        assertForbidden(() -> facade.deleteKey(
                KEY_OWNER, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1, existing.getId()));

        var created = create(ACCOUNT_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1);
        assertThat(created.getStatus()).isEqualTo(KeyStatusV2.ACTIVE);
        assertThat(get(PLATFORM_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1, existing.getId())).isNotNull();
        assertThat(facade.updateKeyStatus(
                ACCOUNT_ADMIN, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1, existing.getId(),
                SettableKeyStatus.SUSPENDED).getStatus()).isEqualTo(KeyStatusV2.SUSPENDED);
        assertThat(create(PLATFORM_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1)).isNotNull();
    }

    @Test
    void suspensionInOneAccountDoesNotDenyKeyOwnerInAnotherAccount() {
        suspendAccount(NCA_ID_1, USER_KEY_OWNER_ID_1);

        assertThat(create(KEY_OWNER, NCA_ID_2, USER_KEY_OWNER_ID_1)).isNotNull();
    }

    @Test
    void issuerServiceSuspensionDeniesKeyOwnerForThatIssuerOnly() {
        facade.updateIssuerServiceStatus(
                PLATFORM_ADMIN, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1,
                issuerStatusRequest(KeyOwnerStatus.SUSPENDED));

        assertForbidden(() -> create(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1));
        assertThat(create(ACCOUNT_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1)).isNotNull();
    }

    @Test
    void updateKeyStatusAppliesTransitionsAndRejectsChangeFromRevoked() {
        var key = create(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1);

        assertThat(setStatus(key, SettableKeyStatus.SUSPENDED).getStatus())
                .isEqualTo(KeyStatusV2.SUSPENDED);
        assertThat(setStatus(key, SettableKeyStatus.ACTIVE).getStatus())
                .isEqualTo(KeyStatusV2.ACTIVE);
        assertThat(setStatus(key, SettableKeyStatus.REVOKED).getStatus())
                .isEqualTo(KeyStatusV2.REVOKED);

        assertThrows(ConflictException.class, () -> setStatus(key, SettableKeyStatus.ACTIVE));
    }

    @Test
    void updateAuthorizationsReplacesDocumentAndEnforcesMinimumInterval() throws Exception {
        var key = create(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1);
        var update = UpdateAuthorizationsRequest.builder()
                .authorizations(JSON_MAPPER.readTree("{\"allow\":false}"))
                .build();

        assertThrows(TooManyRequestsException.class, () -> facade.updateKeyAuthorizations(
                KEY_OWNER, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1, key.getId(), update));

        releaseAuthorizationLocks();
        var updated = facade.updateKeyAuthorizations(
                KEY_OWNER, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1, key.getId(), update);

        assertThat(updated.getAuthorizations()).isEqualTo(JSON_MAPPER.readTree("{\"allow\":false}"));
        assertThat(get(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1, key.getId()).getAuthorizations())
                .isEqualTo(updated.getAuthorizations());
    }

    @Test
    void deleteRemovesKey() {
        var key = create(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1);

        facade.deleteKey(KEY_OWNER, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1, key.getId());

        assertThrowsNotFound(() -> get(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1, key.getId()));
        assertThat(facade.listKeys(KEY_OWNER, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1)
                           .getKeys()).isEmpty();
    }

    @Test
    void createRejectsUnregisteredServiceAndBlankRouteIds() {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> facade.createKey(KEY_OWNER, NCA_ID_1, "other-service",
                                       USER_KEY_OWNER_ID_1, createRequest()),
                "Unknown issuer service: 'other-service'");
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> facade.createKey(KEY_OWNER, " ", SERVICE_ID_1, USER_KEY_OWNER_ID_1,
                                       createRequest()),
                "ncaId is invalid");
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> facade.getKey(KEY_OWNER, NCA_ID_1, SERVICE_ID_1, "*", KEY_ID_1),
                "userId is invalid");
    }

    @Test
    void keyOwnerListsOwnKeysAcrossAccountsInPages() {
        var ids = new ArrayList<String>();
        ids.add(create(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1).getId());
        ids.add(create(KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1).getId());
        ids.add(create(KEY_OWNER, NCA_ID_2, USER_KEY_OWNER_ID_1).getId());
        create(KEY_OWNER, NCA_ID_1, USER_ID_2);

        var first = facade.listKeys(KEY_OWNER, null, null, USER_KEY_OWNER_ID_1, null, 2, null);
        assertThat(first.getKeys()).hasSize(2);
        assertThat(first.getLimit()).isEqualTo(2);
        assertThat(first.getCursor()).isNotBlank();

        var second = facade.listKeys(
                KEY_OWNER, null, null, USER_KEY_OWNER_ID_1, null, 2, first.getCursor());
        assertThat(second.getCursor()).isNull();

        var listed = new ArrayList<>(first.getKeys());
        listed.addAll(second.getKeys());
        assertThat(listed).extracting(KeyResponse::getId).containsExactlyInAnyOrderElementsOf(ids);
        assertThat(listed).allSatisfy(key -> {
            assertThat(key.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
            assertThat(key.getAuthorizations()).isNull();
            assertThat(key.getValue()).contains("**********");
        });
    }

    @Test
    void listNarrowsByAccountServiceAndStatus() {
        var active = create(ACCOUNT_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1);
        var suspended = create(ACCOUNT_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1);
        setStatus(suspended, SettableKeyStatus.SUSPENDED);
        create(ACCOUNT_ADMIN, NCA_ID_2, USER_KEY_OWNER_ID_1);

        assertThat(facade.listKeys(ACCOUNT_ADMIN, NCA_ID_1, null, null, null, null, null).getKeys())
                .extracting(KeyResponse::getId)
                .containsExactlyInAnyOrder(active.getId(), suspended.getId());
        assertThat(facade.listKeys(ACCOUNT_ADMIN, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1,
                                   KeyStatus.SUSPENDED, null, null).getKeys())
                .extracting(KeyResponse::getId).containsExactly(suspended.getId());
        assertThat(facade.listKeys(ACCOUNT_ADMIN, NCA_ID_1, "other-service", null, null, null, null)
                           .getKeys()).isEmpty();
    }

    @Test
    void listRequiresTheDimensionFixedByTheActorRoute() {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> facade.listKeys(KEY_OWNER, NCA_ID_1, null, null, null, null, null),
                "userId is invalid");
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> facade.listKeys(ACCOUNT_ADMIN, null, null, USER_KEY_OWNER_ID_1, null, null,
                                      null),
                "ncaId is invalid");
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> facade.listKeys(PLATFORM_ADMIN, " ", null, null, null, null, null),
                "ncaId is invalid");
    }

    @Test
    void platformAdminListsEveryKeyWithStrictCapWhenUnfiltered() {
        create(ACCOUNT_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1);
        create(ACCOUNT_ADMIN, NCA_ID_2, USER_ID_2);

        assertThat(facade.listKeys(PLATFORM_ADMIN, null, null, null, null, null, null).getKeys())
                .hasSize(2);
        assertThrows(BadRequestException.class, () -> facade.listKeys(
                PLATFORM_ADMIN, null, null, null, null, 101, null));
        assertThat(facade.listKeys(PLATFORM_ADMIN, NCA_ID_1, null, null, null, 100, null)
                           .getKeys()).hasSize(1);
    }

    @Test
    void listRejectsInvalidCursor() {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> facade.listKeys(
                        PLATFORM_ADMIN, null, null, null, null, 10, "not-a-cursor"),
                "Invalid cursor: 'not-a-cursor'");
    }

    @Test
    void legacyKeyIsManagedThroughCanonicalServiceId() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);

        var fetched = facade.getKey(
                KEY_OWNER, LEGACY_NCA_ID, LEGACY_CANONICAL_SERVICE_ID, USER_KEY_OWNER_ID_1,
                KEY_ID_1);
        assertThat(fetched.getAccountId()).isEqualTo(LEGACY_NCA_ID);
        assertThat(fetched.getIssuerServiceId()).isEqualTo(LEGACY_CANONICAL_SERVICE_ID);
        assertThat(fetched.getExpiresAt()).isEqualTo(KEY_EXPIRES_AT_1);

        assertThat(facade.listKeys(
                ACCOUNT_ADMIN, LEGACY_NCA_ID, LEGACY_CANONICAL_SERVICE_ID, USER_KEY_OWNER_ID_1)
                           .getKeys())
                .extracting(KeyResponse::getId).containsExactly(KEY_ID_1);

        assertThat(facade.updateKeyStatus(
                PLATFORM_ADMIN, LEGACY_NCA_ID, LEGACY_CANONICAL_SERVICE_ID, USER_KEY_OWNER_ID_1,
                KEY_ID_1, SettableKeyStatus.REVOKED).getStatus()).isEqualTo(KeyStatusV2.REVOKED);

        facade.deleteKey(KEY_OWNER, LEGACY_NCA_ID, LEGACY_CANONICAL_SERVICE_ID,
                         USER_KEY_OWNER_ID_1, KEY_ID_1);
        assertThrowsNotFound(() -> facade.getKey(
                KEY_OWNER, LEGACY_NCA_ID, LEGACY_CANONICAL_SERVICE_ID, USER_KEY_OWNER_ID_1,
                KEY_ID_1));
    }

    @Test
    void statusesListEveryKnownAccountInPagesAndDefaultToActive() {
        create(ACCOUNT_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1);
        suspendAccount(NCA_ID_2, USER_KEY_OWNER_ID_1);
        facade.updateIssuerServiceStatus(
                PLATFORM_ADMIN, NCA_ID_3, SERVICE_ID_1, USER_KEY_OWNER_ID_1,
                issuerStatusRequest(KeyOwnerStatus.SUSPENDED));
        create(ACCOUNT_ADMIN, NCA_ID_1, USER_ID_2);

        var first = facade.getOwnerStatuses(KEY_OWNER, null, USER_KEY_OWNER_ID_1, 2, null);
        assertThat(first.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
        assertThat(first.getLimit()).isEqualTo(2);
        assertThat(first.getCursor()).isNotBlank();
        assertThat(first.getAccounts()).extracting(OwnerStatusByAccount::getAccountId)
                .containsExactly(NCA_ID_1, NCA_ID_2);
        assertThat(first.getAccounts()).extracting(OwnerStatusByAccount::getStatus)
                .containsExactly(KeyOwnerStatus.ACTIVE, KeyOwnerStatus.SUSPENDED);

        var second = facade.getOwnerStatuses(
                PLATFORM_ADMIN, null, USER_KEY_OWNER_ID_1, 2, first.getCursor());
        assertThat(second.getCursor()).isNull();
        assertThat(second.getAccounts()).singleElement().satisfies(account -> {
            assertThat(account.getAccountId()).isEqualTo(NCA_ID_3);
            assertThat(account.getStatus()).isEqualTo(KeyOwnerStatus.ACTIVE);
            assertThat(account.getIssuerServices()).singleElement().satisfies(issuer -> {
                assertThat(issuer.getIssuerServiceId()).isEqualTo(SERVICE_ID_1);
                assertThat(issuer.getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
            });
        });
    }

    @Test
    void statusesForOneAccountReturnExactlyOneEntryWithoutCursor() {
        suspendAccount(NCA_ID_2, USER_KEY_OWNER_ID_1);
        create(ACCOUNT_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1);

        KeyOwnerStatusesResponse known = facade.getOwnerStatuses(
                ACCOUNT_ADMIN, NCA_ID_2, USER_KEY_OWNER_ID_1, null, null);
        assertThat(known.getAccounts()).singleElement()
                .satisfies(account -> assertThat(account.getStatus())
                        .isEqualTo(KeyOwnerStatus.SUSPENDED));
        assertThat(known.getCursor()).isNull();
        assertThat(known.getLimit()).isNull();

        var unknown = facade.getOwnerStatuses(
                PLATFORM_ADMIN, "unknown-nca", USER_KEY_OWNER_ID_1, null, null);
        assertThat(unknown.getAccounts()).singleElement().satisfies(account -> {
            assertThat(account.getAccountId()).isEqualTo("unknown-nca");
            assertThat(account.getStatus()).isEqualTo(KeyOwnerStatus.ACTIVE);
        });
    }

    @Test
    void statusesRejectInvalidCursorAndAccountAdminCrossAccountRead() {
        assertThrows(BadRequestException.class, () -> facade.getOwnerStatuses(
                PLATFORM_ADMIN, null, USER_KEY_OWNER_ID_1, 10, "***"));
        assertForbidden(() -> facade.getOwnerStatuses(
                ACCOUNT_ADMIN, null, USER_KEY_OWNER_ID_1, 10, null));
    }

    @Test
    void legacyAccountStatusIsReportedForOwnerWithLegacyKeys() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        facade.updateAccountStatus(
                ACCOUNT_ADMIN, LEGACY_NCA_ID, USER_KEY_OWNER_ID_1,
                accountStatusRequest(KeyOwnerStatus.SUSPENDED));

        assertThat(facade.getOwnerStatuses(
                PLATFORM_ADMIN, null, USER_KEY_OWNER_ID_1, null, null).getAccounts())
                .singleElement().satisfies(account -> {
                    assertThat(account.getAccountId()).isEqualTo(LEGACY_NCA_ID);
                    assertThat(account.getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
                });
    }

    @Test
    void administratorsUpdateAccountStatusAndKeyOwnerCannot() {
        var response = facade.updateAccountStatus(
                ACCOUNT_ADMIN, NCA_ID_1, USER_KEY_OWNER_ID_1,
                accountStatusRequest(KeyOwnerStatus.SUSPENDED));

        assertThat(response.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
        assertThat(response.getAccountId()).isEqualTo(NCA_ID_1);
        assertThat(response.getIssuerServiceId()).isNull();
        assertThat(response.getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(response.getUpdatedAt()).isNotNull();
        assertForbidden(() -> facade.updateAccountStatus(
                KEY_OWNER, NCA_ID_1, USER_KEY_OWNER_ID_1,
                accountStatusRequest(KeyOwnerStatus.ACTIVE)));
    }

    @Test
    void issuerServiceStatusIsPlatformAdminOnlyAndUnsupportedForLegacyAccount() {
        var response = facade.updateIssuerServiceStatus(
                PLATFORM_ADMIN, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1,
                issuerStatusRequest(KeyOwnerStatus.SUSPENDED));

        assertThat(response.getIssuerServiceId()).isEqualTo(SERVICE_ID_1);
        assertThat(response.getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertForbidden(() -> facade.updateIssuerServiceStatus(
                ACCOUNT_ADMIN, NCA_ID_1, SERVICE_ID_1, USER_KEY_OWNER_ID_1,
                issuerStatusRequest(KeyOwnerStatus.SUSPENDED)));
        assertThrows(BadRequestException.class, () -> facade.updateIssuerServiceStatus(
                PLATFORM_ADMIN, LEGACY_NCA_ID, LEGACY_CANONICAL_SERVICE_ID, USER_KEY_OWNER_ID_1,
                issuerStatusRequest(KeyOwnerStatus.SUSPENDED)));
    }

    private KeyResponse create(KeyManagementActor actor, String ncaId, String userId) {
        return facade.createKey(actor, ncaId, SERVICE_ID_1, userId, createRequest());
    }

    private KeyResponse get(KeyManagementActor actor, String ncaId, String userId, String keyId) {
        return facade.getKey(actor, ncaId, SERVICE_ID_1, userId, keyId);
    }

    private KeyResponse setStatus(KeyResponse key, SettableKeyStatus status) {
        return facade.updateKeyStatus(
                PLATFORM_ADMIN, key.getAccountId(), key.getIssuerServiceId(), key.getOwnerId(),
                key.getId(), status);
    }

    // the lock expires by Cassandra TTL, which the test clock does not move
    private static void releaseAuthorizationLocks() {
        IntegrationTestConfiguration.CQL_SESSION.execute(
                "TRUNCATE " + KEY_SPACE + ".row_update_lock");
    }

    private void suspendAccount(String ncaId, String userId) {
        facade.updateAccountStatus(
                PLATFORM_ADMIN, ncaId, userId, accountStatusRequest(KeyOwnerStatus.SUSPENDED));
    }

    private static CreateKeyRequest createRequest() {
        return CreateKeyRequest.builder()
                .description(KEY_DESCRIPTION_1)
                .expiresAt(KEY_EXPIRES_AT_1)
                .authorizations(JSON_MAPPER.readTree(KEY_AUTHZ_1))
                .build();
    }

    private static UpdateOwnerStatusForAccountRequest accountStatusRequest(KeyOwnerStatus status) {
        return UpdateOwnerStatusForAccountRequest.builder().status(status).build();
    }

    private static UpdateOwnerStatusForAccountAndIssuerServiceRequest issuerStatusRequest(
            KeyOwnerStatus status) {
        return UpdateOwnerStatusForAccountAndIssuerServiceRequest.builder().status(status).build();
    }

    private static void assertForbidden(org.junit.jupiter.api.function.Executable call) {
        assertThrows(ForbiddenException.class, call);
    }

    private static void assertThrowsNotFound(org.junit.jupiter.api.function.Executable call) {
        assertThrows(NotFoundException.class, call);
    }
}
