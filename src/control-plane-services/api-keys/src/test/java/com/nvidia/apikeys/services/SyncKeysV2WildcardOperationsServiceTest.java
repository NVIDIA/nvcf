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

package com.nvidia.apikeys.services;

import static com.nvidia.apikeys.dto.keysV2.KeysOperationType.DELETE;
import static com.nvidia.apikeys.dto.keysV2.KeysOperationType.REVOKED;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.atLeastOnce;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.dto.keysV2.AccountAdminKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.KeysOperationType;
import com.nvidia.apikeys.dto.keysV2.PlatformAdminKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.SyncKeysOperationResponse;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.services.SyncKeysV2OperationsService.BulkOperationException;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.CsvSource;
import org.junit.jupiter.params.provider.EnumSource;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class SyncKeysV2WildcardOperationsServiceTest {

    private static final Instant CUTOFF = Instant.parse("2026-10-26T12:00:00Z");
    private static final List<String> ALL = List.of("*");
    private static final List<String> ACCOUNTS = List.of("a1", "a2");
    private static final List<String> USERS = List.of("u1", "u2");
    private static final List<String> ISSUERS = List.of("s1", "s2");

    @Mock private KeysV2Service keys;
    @Mock private ServicesService services;
    @Mock private KeysV2BulkSelectionService selection;
    private SyncKeysV2OperationsService service;
    private final List<Mutation> mutations = new ArrayList<>();

    @BeforeEach
    void setUp() {
        var legacy = new LegacyAccountTranslator(NakProperties.builder()
                .ncaId("legacy").serviceIdMap(Map.of("s1", "legacy-s1")).build());
        var clock = new MillisecondPrecisionClock(Clock.fixed(CUTOFF, ZoneOffset.UTC));
        service = new SyncKeysV2OperationsService(keys, services, legacy, clock, 10, selection);
        lenient().when(services.getServiceById(anyString())).thenAnswer(call ->
                Optional.of(ServiceVo.builder().serviceId(call.getArgument(0)).build()));
        lenient().doAnswer(call -> {
            mutations.add(new Mutation(DELETE, call.getArgument(0), call.getArgument(2),
                    call.getArgument(3), call.getArgument(4)));
            return null;
        }).when(keys).deleteKeyById(anyString(), eq(USER), anyString(), anyString(), anyString());
        lenient().doAnswer(call -> {
            assertThat(call.getArgument(5, KeyStatus.class)).isEqualTo(KeyStatus.REVOKED);
            mutations.add(new Mutation(REVOKED, call.getArgument(0), call.getArgument(2),
                    call.getArgument(3), call.getArgument(4)));
            return null;
        }).when(keys).updateKeyStatus(anyString(), eq(USER), anyString(), anyString(), anyString(), any());
    }

    @ParameterizedTest
    @MethodSource("selectorCombinations")
    void selectsEveryMatchingKeyWithinTheActorBoundary(String actor, KeysOperationType operation,
            boolean everyAccount, boolean everyUser, boolean everyIssuer) {
        var fixtures = new ArrayList<KeysByOwnerAndAccountAndServiceVo>();
        for (var account : List.of("a1", "a2", "a3")) {
            for (var user : List.of("u1", "u2", "u3")) {
                for (var issuer : List.of("s1", "s2", "s3")) {
                    fixtures.add(row(account, user, issuer, "first"));
                    fixtures.add(row(account, user, issuer, "second"));
                }
            }
        }
        stubSelection(fixtures);
        var accounts = actor.equals("ACCOUNT_ADMIN") ? List.of("a1") : everyAccount ? ALL : ACCOUNTS;
        var users = actor.equals("KEY_OWNER") ? List.of("u1") : everyUser ? ALL : USERS;
        var issuers = everyIssuer ? ALL : ISSUERS;
        var expected = fixtures.stream()
                .filter(key -> includes(accounts, key.getNcaId()) && includes(users, key.getOwnerId())
                        && includes(issuers, key.getIssuerServiceId()))
                .map(key -> mutation(operation, key)).toList();

        var response = operate(actor, operation, accounts, users, issuers,
                everyAccount && everyUser && everyIssuer ? true : null);

        assertThat(response.completedCount()).isEqualTo(expected.size());
        assertThat(mutations).containsExactlyInAnyOrderElementsOf(expected);
        assertThat(response.cutoffAt()).isEqualTo(CUTOFF);
        verify(selection, atLeastOnce()).select(any(), any(), any(), eq(CUTOFF));
    }

    static Stream<Arguments> selectorCombinations() {
        var cases = new ArrayList<Arguments>();
        for (var operation : KeysOperationType.values()) {
            for (int mask = 0; mask < 8; mask++) {
                boolean accounts = (mask & 1) != 0;
                boolean users = (mask & 2) != 0;
                boolean issuers = (mask & 4) != 0;
                cases.add(Arguments.of("PLATFORM_ADMIN", operation, accounts, users, issuers));
                if (!users) {
                    cases.add(Arguments.of("KEY_OWNER", operation, accounts, false, issuers));
                }
                if (!accounts) {
                    cases.add(Arguments.of("ACCOUNT_ADMIN", operation, false, users, issuers));
                }
            }
        }
        return cases.stream();
    }

    @ParameterizedTest
    @MethodSource("invalidSelectors")
    void rejectsInvalidSelectorsBeforeDiscoveryOrMutation(String dimension, List<String> values) {
        var request = PlatformAdminKeysOperationRequest.builder().operation(DELETE)
                .accounts(ACCOUNTS).users(USERS).issuerServices(ISSUERS).build();
        switch (dimension) {
            case "accounts" -> request.setAccounts(values);
            case "users" -> request.setUsers(values);
            case "issuerServices" -> request.setIssuerServices(values);
            default -> throw new IllegalArgumentException(dimension);
        }

        assertThrows(BadRequestException.class, () -> service.operateAsPlatformAdmin(request));

        verifyNoInteractions(selection, keys);
    }

    static Stream<Arguments> invalidSelectors() {
        List<List<String>> invalid = Arrays.asList(null, List.of(), List.of("*", "id"),
                List.of("id", "*"), List.of("id", "id"), List.of("*", "*"),
                Arrays.asList("id", null));
        return Stream.of("accounts", "users", "issuerServices")
                .flatMap(dimension -> invalid.stream().map(values -> Arguments.of(dimension, values)));
    }

    @ParameterizedTest
    @CsvSource({"KEY_OWNER", "ACCOUNT_ADMIN"})
    void fixedRouteDimensionsCannotBeWildcards(String actor) {
        assertThrows(BadRequestException.class, () -> operate(actor, DELETE, ALL, ALL, ALL, null));
        verifyNoInteractions(selection, keys);
    }

    @ParameterizedTest
    @MethodSource("missingPlatformConfirmations")
    void deploymentWideSelectionRequiresExplicitTrue(KeysOperationType operation, Boolean confirmation) {
        assertThrows(BadRequestException.class,
                () -> operate("PLATFORM_ADMIN", operation, ALL, ALL, ALL, confirmation));
        verifyNoInteractions(selection, keys);
    }

    static Stream<Arguments> missingPlatformConfirmations() {
        return Stream.of(DELETE, REVOKED).flatMap(operation ->
                Arrays.asList((Boolean) null, false).stream()
                        .map(confirmation -> Arguments.of(operation, confirmation)));
    }

    @ParameterizedTest
    @ValueSource(ints = {1, 2, 3, 4, 5, 6})
    void partialWildcardsDoNotRequirePlatformWideConfirmation(int mask) {
        stubSelection(List.of(row("a1", "u1", "s1", "key")));
        var response = operate("PLATFORM_ADMIN", DELETE,
                (mask & 1) != 0 ? ALL : ACCOUNTS,
                (mask & 2) != 0 ? ALL : USERS,
                (mask & 4) != 0 ? ALL : ISSUERS, false);
        assertThat(response.completedCount()).isOne();
        assertThat(mutations).containsExactly(new Mutation(DELETE, "a1", "u1", "s1", "key"));
    }

    @ParameterizedTest
    @CsvSource({"accounts,a*,u1,s1", "users,a1,user*,s1", "issuerServices,a1,u1,issuer*"})
    void embeddedAsterisksStayLiteralWhenOtherDimensionsAreWildcards(
            String dimension, String account, String user, String issuer) {
        var exact = row(account, user, issuer, "exact");
        stubSelection(List.of(exact, row(account.replace("*", "suffix"), user.replace("*", "suffix"),
                issuer.replace("*", "suffix"), "different")));
        var response = operate("PLATFORM_ADMIN", DELETE,
                dimension.equals("accounts") ? List.of(account) : ALL,
                dimension.equals("users") ? List.of(user) : ALL,
                dimension.equals("issuerServices") ? List.of(issuer) : ALL, null);
        assertThat(response.completedCount()).isOne();
        assertThat(mutations).containsExactly(mutation(DELETE, exact));
    }

    @ParameterizedTest
    @EnumSource(KeysOperationType.class)
    void appliesSubmissionCutoffThroughoutTheStreamAndClosesIt(KeysOperationType operation) {
        var before = row("a1", "u1", "s1", "before").toBuilder()
                .createdAt(CUTOFF.minusMillis(1)).build();
        var at = row("a2", "u2", "s2", "at");
        var after = row("a3", "u3", "s3", "after").toBuilder()
                .createdAt(CUTOFF.plusMillis(1)).build();
        var closed = new AtomicBoolean();
        when(selection.select(null, null, null, CUTOFF)).thenAnswer(call ->
                Stream.of(List.of(before), List.of(at, after)).flatMap(List::stream)
                        .onClose(() -> closed.set(true)));

        var response = operate("PLATFORM_ADMIN", operation, ALL, ALL, ALL, true);

        assertThat(response.completedCount()).isEqualTo(2);
        assertThat(mutations).containsExactly(mutation(operation, before), mutation(operation, at));
        assertThat(closed).isTrue();
    }

    @Test
    void emptyWildcardSelectionCompletesWithoutMutations() {
        when(selection.select(null, null, null, CUTOFF)).thenReturn(Stream.empty());
        var response = operate("PLATFORM_ADMIN", DELETE, ALL, ALL, ALL, true);
        assertThat(response.completedCount()).isZero();
        assertThat(response.correlationId()).isNotBlank();
        verifyNoInteractions(keys);
    }

    @ParameterizedTest
    @MethodSource("outOfScopeKeys")
    void rejectsDiscoveredKeysOutsideTheRequestedScope(String actor, List<String> accounts,
            List<String> users, List<String> issuers, KeysByOwnerAndAccountAndServiceVo wrongKey) {
        when(selection.select(any(), any(), any(), any())).thenAnswer(call -> Stream.of(wrongKey));
        var failure = assertThrows(RuntimeException.class,
                () -> operate(actor, DELETE, accounts, users, issuers, null));
        assertThat(failure instanceof BulkOperationException ? failure.getCause() : failure)
                .isInstanceOf(IllegalStateException.class);
        assertThat(mutations).isEmpty();
    }

    static Stream<Arguments> outOfScopeKeys() {
        return Stream.of(
                Arguments.of("KEY_OWNER", ALL, List.of("u1"), ALL, row("a1", "other", "s1", "key")),
                Arguments.of("ACCOUNT_ADMIN", List.of("a1"), ALL, ALL, row("other", "u1", "s1", "key")),
                Arguments.of("PLATFORM_ADMIN", ACCOUNTS, ALL, ALL, row("other", "u1", "s1", "key")),
                Arguments.of("PLATFORM_ADMIN", ALL, USERS, ALL, row("a1", "other", "s1", "key")),
                Arguments.of("PLATFORM_ADMIN", ALL, ALL, ISSUERS, row("a1", "u1", "other", "key")),
                Arguments.of("ACCOUNT_ADMIN", List.of("a1"), ALL, ALL,
                        row("a1", "u1", "s1", "key").toBuilder().ownerType(null).build()));
    }

    @ParameterizedTest
    @ValueSource(strings = {"account", "issuer"})
    void ownerWildcardPreflightRejectsSuspensionBeforeAnyMutation(String suspendedScope) {
        stubSelection(List.of(row("a1", "u1", "s1", "allowed"), row("a2", "u1", "s2", "blocked")));
        when(keys.getOwnerStatusByAccount("a1", USER, "u1")).thenReturn(Optional.empty());
        when(keys.getOwnerStatusByAccountAndService("a1", USER, "u1", "s1"))
                .thenReturn(Optional.empty());
        if (suspendedScope.equals("account")) {
            when(keys.getOwnerStatusByAccount("a2", USER, "u1")).thenReturn(Optional.of(
                    OwnerStatusByAccountModel.builder().ncaId("a2").ownerType(USER).ownerId("u1")
                            .ownerStatus(KeyOwnerStatus.SUSPENDED).build()));
            lenient().when(keys.getOwnerStatusByAccountAndService("a2", USER, "u1", "s2"))
                    .thenReturn(Optional.of(OwnerStatusByAccountAndServiceModel.builder()
                            .ncaId("a2").ownerType(USER).ownerId("u1").issuerServiceId("s2")
                            .ownerStatus(KeyOwnerStatus.ACTIVE).build()));
        } else {
            when(keys.getOwnerStatusByAccount("a2", USER, "u1")).thenReturn(Optional.empty());
            when(keys.getOwnerStatusByAccountAndService("a2", USER, "u1", "s2"))
                    .thenReturn(Optional.of(OwnerStatusByAccountAndServiceModel.builder()
                            .ncaId("a2").ownerType(USER).ownerId("u1").issuerServiceId("s2")
                            .ownerStatus(KeyOwnerStatus.SUSPENDED).build()));
        }
        assertThrows(ForbiddenException.class,
                () -> operate("KEY_OWNER", DELETE, ALL, List.of("u1"), ALL, null));
        assertThat(mutations).isEmpty();
    }

    @ParameterizedTest
    @CsvSource({"ACCOUNT_ADMIN,DELETE", "ACCOUNT_ADMIN,REVOKED", "PLATFORM_ADMIN,DELETE", "PLATFORM_ADMIN,REVOKED"})
    void administratorsCanManageSuspendedUsersWithWildcards(String actor, KeysOperationType operation) {
        var key = row("a1", "u1", "s1", "key");
        stubSelection(List.of(key));
        lenient().when(keys.getOwnerStatusByAccount(anyString(), eq(USER), anyString()))
                .thenReturn(Optional.of(OwnerStatusByAccountModel.builder()
                        .ncaId("a1").ownerType(USER).ownerId("u1")
                        .ownerStatus(KeyOwnerStatus.SUSPENDED).build()));
        lenient().when(keys.getOwnerStatusByAccountAndService(anyString(), eq(USER), anyString(), anyString()))
                .thenReturn(Optional.of(OwnerStatusByAccountAndServiceModel.builder()
                        .ncaId("a1").ownerType(USER).ownerId("u1").issuerServiceId("s1")
                        .ownerStatus(KeyOwnerStatus.SUSPENDED).build()));
        var response = operate(actor, operation, actor.equals("ACCOUNT_ADMIN") ? List.of("a1") : ALL,
                ALL, ALL, actor.equals("PLATFORM_ADMIN") ? true : null);
        assertThat(response.completedCount()).isOne();
        assertThat(mutations).containsExactly(mutation(operation, key));
    }

    private void stubSelection(List<KeysByOwnerAndAccountAndServiceVo> fixtures) {
        when(selection.select(any(), any(), any(), eq(CUTOFF))).thenAnswer(call -> {
            String account = call.getArgument(0);
            String user = call.getArgument(1);
            String issuer = call.getArgument(2);
            return fixtures.stream().filter(key -> (account == null || account.equals(key.getNcaId()))
                    && (user == null || user.equals(key.getOwnerId()))
                    && (issuer == null || issuer.equals(key.getIssuerServiceId())));
        });
    }

    private SyncKeysOperationResponse operate(String actor, KeysOperationType operation,
            List<String> accounts, List<String> users, List<String> issuers, Boolean confirmation) {
        return switch (actor) {
            case "KEY_OWNER" -> service.operateAsKeyOwner(users.getFirst(), KeyOwnerKeysOperationRequest.builder()
                    .operation(operation).accounts(accounts).issuerServices(issuers).build());
            case "ACCOUNT_ADMIN" -> service.operateAsAccountAdmin(accounts.getFirst(),
                    AccountAdminKeysOperationRequest.builder().operation(operation)
                            .users(users).issuerServices(issuers).build());
            case "PLATFORM_ADMIN" -> service.operateAsPlatformAdmin(PlatformAdminKeysOperationRequest.builder()
                    .operation(operation).accounts(accounts).users(users).issuerServices(issuers)
                    .confirmPlatformWide(confirmation).build());
            default -> throw new IllegalArgumentException(actor);
        };
    }

    private static boolean includes(List<String> selection, String value) {
        return selection.equals(ALL) || selection.contains(value);
    }

    private static KeysByOwnerAndAccountAndServiceVo row(String account, String user, String issuer, String keyId) {
        return KeysByOwnerAndAccountAndServiceVo.builder().ncaId(account).ownerType(USER).ownerId(user)
                .issuerServiceId(issuer).keyId(keyId).createdAt(CUTOFF).keyStatus(KeyStatus.ACTIVE).build();
    }

    private static Mutation mutation(KeysOperationType operation, KeysByOwnerAndAccountAndServiceVo key) {
        return new Mutation(operation, key.getNcaId(), key.getOwnerId(), key.getIssuerServiceId(), key.getKeyId());
    }

    private record Mutation(KeysOperationType operation, String account, String user, String issuer, String keyId) {
    }
}
