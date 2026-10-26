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
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.dto.keysV2.AccountAdminKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.PlatformAdminKeysOperationRequest;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.services.SyncKeysV2OperationsService.BulkOperationException;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.web.context.request.RequestContextHolder;
import org.springframework.web.context.request.ServletRequestAttributes;

@ExtendWith(MockitoExtension.class)
class SyncKeysV2OperationsServiceTest {

    private static final Instant CUTOFF = Instant.parse("2026-10-21T12:00:00Z");
    private static final String ISSUER = "cloud-functions";

    @Mock
    private KeysV2Service keys;
    @Mock
    private ServicesService services;
    @Mock
    private KeysV2BulkSelectionService selection;
    private SyncKeysV2OperationsService service;

    @BeforeEach
    void setUp() {
        var legacy = new LegacyAccountTranslator(NakProperties.builder()
                .ncaId("legacy").serviceIdMap(Map.of(ISSUER, "legacy-issuer")).build());
        var clock = new MillisecondPrecisionClock(Clock.fixed(CUTOFF, ZoneOffset.UTC));
        service = new SyncKeysV2OperationsService(keys, services, legacy, clock, 10, selection);
        lenient().when(services.getServiceById(ISSUER))
                .thenReturn(Optional.of(ServiceVo.builder().serviceId(ISSUER).build()));
    }

    @ParameterizedTest
    @MethodSource("invalidSelections")
    void validatesEverySelectorBeforeDiscoveryOrMutation(List<String> users) {
        var request = accountRequest().toBuilder().users(users).build();
        assertThatThrownBy(() -> service.operateAsAccountAdmin("a", request))
                .isInstanceOf(BadRequestException.class);
        verifyNoInteractions(keys);
    }

    static Stream<List<String>> invalidSelections() {
        return Stream.of(null, List.of(), List.of("u", "*"),
                List.of("u", "u"), List.of(" "), Arrays.asList("u", null),
                java.util.stream.IntStream.range(0, 11).mapToObj(i -> "u" + i).toList());
    }

    @ParameterizedTest
    @CsvSource({
            "KEY_OWNER, a*, u, cloud-functions",
            "KEY_OWNER, a, user*, cloud-functions",
            "KEY_OWNER, a, u, issuer*",
            "ACCOUNT_ADMIN, a*, u, cloud-functions",
            "ACCOUNT_ADMIN, a, user*, cloud-functions",
            "ACCOUNT_ADMIN, a, u, issuer*",
            "PLATFORM_ADMIN, a*, u, cloud-functions",
            "PLATFORM_ADMIN, a, user*, cloud-functions",
            "PLATFORM_ADMIN, a, u, issuer*"
    })
    void treatsIdsContainingAsterisksAsLiteralValues(
            String actor, String account, String user, String issuer) {
        when(services.getServiceById(issuer))
                .thenReturn(Optional.of(ServiceVo.builder().serviceId(issuer).build()));
        when(selection.select(account, user, issuer, CUTOFF))
                .thenReturn(Stream.of(row(account, user, "key", CUTOFF).toBuilder()
                        .issuerServiceId(issuer).build()));

        var response = switch (actor) {
            case "KEY_OWNER" -> service.operateAsKeyOwner(user,
                    KeyOwnerKeysOperationRequest.builder().operation(DELETE)
                            .accounts(List.of(account)).issuerServices(List.of(issuer)).build());
            case "ACCOUNT_ADMIN" -> service.operateAsAccountAdmin(account,
                    AccountAdminKeysOperationRequest.builder().operation(DELETE)
                            .users(List.of(user)).issuerServices(List.of(issuer)).build());
            case "PLATFORM_ADMIN" -> service.operateAsPlatformAdmin(
                    PlatformAdminKeysOperationRequest.builder().operation(DELETE)
                            .accounts(List.of(account)).users(List.of(user))
                            .issuerServices(List.of(issuer)).build());
            default -> throw new IllegalArgumentException(actor);
        };

        assertThat(response.completedCount()).isOne();
        verify(keys).deleteKeyById(account, USER, user, issuer, "key");
    }

    @Test
    void invalidLaterDimensionDoesNotPartiallyApplyEarlierValues() {
        var request = accountRequest().toBuilder().issuerServices(List.of(ISSUER, "*")).build();
        assertThatThrownBy(() -> service.operateAsAccountAdmin("a", request))
                .isInstanceOf(BadRequestException.class);
        verifyNoInteractions(keys);
    }

    @Test
    void rejectsUnknownAndLegacyIssuerIdsBeforeDiscovery() {
        for (var issuer : List.of("unknown", "legacy-issuer")) {
            assertThatThrownBy(() -> service.operateAsAccountAdmin("a",
                    accountRequest().toBuilder().issuerServices(List.of(issuer)).build()))
                    .isInstanceOf(BadRequestException.class);
        }
        verifyNoInteractions(keys);
    }

    @Test
    void validatesOperationAndFixedRouteDimension() {
        assertThatThrownBy(() -> service.operateAsAccountAdmin("a",
                accountRequest().toBuilder().operation(null).build()))
                .isInstanceOf(BadRequestException.class);
        var invalidAccount = assertThrows(BadRequestException.class,
                () -> service.operateAsAccountAdmin("*", accountRequest()));
        assertThat(invalidAccount.getBody().getDetail()).isEqualTo("ncaId contains an invalid ID");
        var invalidUser = assertThrows(BadRequestException.class,
                () -> service.operateAsKeyOwner("*", KeyOwnerKeysOperationRequest.builder()
                        .operation(DELETE).accounts(List.of("a")).issuerServices(List.of(ISSUER)).build()));
        assertThat(invalidUser.getBody().getDetail()).isEqualTo("userId contains an invalid ID");
        verifyNoInteractions(keys);
    }

    @ParameterizedTest
    @MethodSource("optionalReasons")
    void acceptsOptionalReasonWithoutAdditionalRestrictions(String reason) {
        assertThat(service.operateAsAccountAdmin("a",
                accountRequest().toBuilder().reason(reason).build())
                .completedCount()).isZero();
    }

    static Stream<String> optionalReasons() {
        return Stream.of(null, " ", "bad\nreason", "x".repeat(1025));
    }

    @ParameterizedTest
    @ValueSource(strings = {" user ", "user\nid"})
    void preservesNonblankLiteralIds(String user) {
        when(selection.select("a", user, ISSUER, CUTOFF))
                .thenReturn(Stream.of(row("a", user, "key", CUTOFF)));

        assertThat(service.operateAsAccountAdmin("a", accountRequest().toBuilder()
                .operation(DELETE).users(List.of(user)).build()).completedCount()).isOne();

        verify(keys).deleteKeyById("a", USER, user, ISSUER, "key");
    }

    @Test
    void auditIdentityDoesNotAddAHeaderLengthLimit() {
        var request = new MockHttpServletRequest();
        request.addHeader("NVCF-ACTOR-ID", "a".repeat(257));
        RequestContextHolder.setRequestAttributes(new ServletRequestAttributes(request));
        try {
            assertThat(service.operateAsAccountAdmin("a", accountRequest()).completedCount()).isZero();
        } finally {
            RequestContextHolder.resetRequestAttributes();
        }
    }

    @Test
    void noMatchesReturnsCompletedNoOp() {
        var response = service.operateAsAccountAdmin("a", accountRequest());
        assertThat(response.completedCount()).isZero();
        assertThat(response.cutoffAt()).isEqualTo(CUTOFF);
        assertThat(response.correlationId()).isNotBlank();
    }

    @Test
    void appliesCutoffAndDeduplicatesWithinExactAccountBoundary() {
        var row = row("a", "u", "old", CUTOFF);
        when(selection.select("a", "u", ISSUER, CUTOFF)).thenReturn(Stream.of(row, row,
                row("a", "u", "later", CUTOFF.plusMillis(1))));
        var response = service.operateAsAccountAdmin("a", accountRequest());
        assertThat(response.completedCount()).isOne();
        verify(keys).updateKeyStatus("a", USER, "u", ISSUER, "old", KeyStatus.REVOKED);
        verify(keys, never()).updateKeyStatus("a", USER, "u", ISSUER, "later", KeyStatus.REVOKED);
        verify(keys, never()).getOwnerStatusByAccount(anyString(), any(), anyString());
    }

    @Test
    void platformSelectionUsesExactAccountUserIssuerIntersectionsIncludingLegacyAccount() {
        when(selection.select("a", "u", ISSUER, CUTOFF))
                .thenReturn(Stream.of(row("a", "u", "new-key", CUTOFF)));
        when(selection.select("legacy", "u", ISSUER, CUTOFF))
                .thenReturn(Stream.of(row("legacy", "u", "legacy-key", CUTOFF)));
        var response = service.operateAsPlatformAdmin(PlatformAdminKeysOperationRequest.builder()
                .operation(DELETE).accounts(List.of("a", "legacy")).users(List.of("u"))
                .issuerServices(List.of(ISSUER)).build());
        assertThat(response.completedCount()).isEqualTo(2);
        verify(keys).deleteKeyById("a", USER, "u", ISSUER, "new-key");
        verify(keys).deleteKeyById("legacy", USER, "u", ISSUER, "legacy-key");
    }

    @Test
    void checksEveryOwnerScopeBeforeAnyChanges() {
        when(keys.getOwnerStatusByAccount("a", USER, "u")).thenReturn(Optional.empty());
        when(keys.getOwnerStatusByAccount("blocked", USER, "u"))
                .thenReturn(Optional.of(OwnerStatusByAccountModel.builder()
                        .ncaId("blocked").ownerType(USER).ownerId("u")
                        .ownerStatus(KeyOwnerStatus.SUSPENDED).build()));
        assertThatThrownBy(() -> service.operateAsKeyOwner("u",
                KeyOwnerKeysOperationRequest.builder().operation(DELETE)
                        .accounts(List.of("a", "blocked")).issuerServices(List.of(ISSUER)).build()))
                .isInstanceOf(ForbiddenException.class);
        verifyNoInteractions(selection);
    }

    @Test
    void ownerCannotOperateWhenIssuerStatusIsSuspended() {
        when(keys.getOwnerStatusByAccountAndService("a", USER, "u", ISSUER))
                .thenReturn(Optional.of(OwnerStatusByAccountAndServiceModel.builder()
                        .ncaId("a").ownerType(USER).ownerId("u").issuerServiceId(ISSUER)
                        .ownerStatus(KeyOwnerStatus.SUSPENDED).build()));
        assertThatThrownBy(() -> service.operateAsKeyOwner("u", ownerRequest()))
                .isInstanceOf(ForbiddenException.class);
        verifyNoInteractions(selection);
    }

    @Test
    void ownerRouteKeepsUserFixed() {
        when(selection.select("a", "u", ISSUER, CUTOFF))
                .thenReturn(Stream.of(row("a", "u", "key", CUTOFF)));
        assertThat(service.operateAsKeyOwner("u", ownerRequest()).completedCount()).isOne();
        verify(keys).deleteKeyById("a", USER, "u", ISSUER, "key");
    }

    @Test
    void rejectsMismatchedStoredScopeWithoutMutation() {
        when(selection.select("a", "u", ISSUER, CUTOFF))
                .thenReturn(Stream.of(row("different-account", "u", "key", CUTOFF)));
        var failure = assertThrows(BulkOperationException.class,
                () -> service.operateAsAccountAdmin("a", accountRequest()));
        assertThat(failure.getBody().getProperties()).containsEntry("partialApplication", false);
        verify(keys, never()).updateKeyStatus(anyString(), any(), anyString(), anyString(),
                anyString(), any());
    }

    @Test
    void laterMutationFailureRetainsCauseAndStopsFurtherKeys() {
        when(selection.select("a", "u", ISSUER, CUTOFF)).thenReturn(Stream.of(
                row("a", "u", "first", CUTOFF), row("a", "u", "failure", CUTOFF),
                row("a", "u", "last", CUTOFF)));
        when(keys.updateKeyStatus("a", USER, "u", ISSUER, "first", KeyStatus.REVOKED))
                .thenReturn(KeyV2Vo.builder().ncaId("a").ownerType(USER).ownerId("u")
                        .issuerServiceId(ISSUER).keyId("first").keyStatus(KeyStatus.REVOKED).build());
        var cause = new ConflictException("terminal key status");
        when(keys.updateKeyStatus("a", USER, "u", ISSUER, "failure", KeyStatus.REVOKED))
                .thenThrow(cause);
        var failure = assertThrows(BulkOperationException.class,
                () -> service.operateAsAccountAdmin("a", accountRequest()));
        assertThat(failure.getStatusCode().value()).isEqualTo(409);
        assertThat(failure.getCause()).isSameAs(cause);
        assertThat(failure.getBody().getProperties()).containsEntry("partialApplication", true)
                .containsKey("correlationId");
        verify(keys).updateKeyStatus("a", USER, "u", ISSUER, "first", KeyStatus.REVOKED);
        verify(keys, never()).updateKeyStatus("a", USER, "u", ISSUER, "last", KeyStatus.REVOKED);
    }

    @Test
    void firstMutationWithLostAcknowledgementIsAlsoPartial() {
        when(selection.select("a", "u", ISSUER, CUTOFF))
                .thenReturn(Stream.of(row("a", "u", "key", CUTOFF)));
        doThrow(new IllegalStateException("acknowledgement lost")).when(keys)
                .deleteKeyById("a", USER, "u", ISSUER, "key");
        var failure = assertThrows(BulkOperationException.class,
                () -> service.operateAsAccountAdmin("a",
                        accountRequest().toBuilder().operation(DELETE).build()));
        assertThat(failure.getBody().getProperties()).containsEntry("partialApplication", true);
    }

    @Test
    void laterDiscoveryFailureCanFollowSuccessfulMutation() {
        when(selection.select("a", "u", ISSUER, CUTOFF))
                .thenReturn(Stream.of(row("a", "u", "key", CUTOFF)));
        when(selection.select("a", "v", ISSUER, CUTOFF))
                .thenThrow(new IllegalStateException("selection unavailable"));
        var failure = assertThrows(BulkOperationException.class,
                () -> service.operateAsAccountAdmin("a",
                        accountRequest().toBuilder().users(List.of("u", "v")).build()));
        assertThat(failure.getBody().getProperties()).containsEntry("partialApplication", true);
        verify(keys).updateKeyStatus("a", USER, "u", ISSUER, "key", KeyStatus.REVOKED);
    }

    private static AccountAdminKeysOperationRequest accountRequest() {
        return AccountAdminKeysOperationRequest.builder().operation(REVOKED)
                .users(List.of("u")).issuerServices(List.of(ISSUER)).build();
    }

    private static KeyOwnerKeysOperationRequest ownerRequest() {
        return KeyOwnerKeysOperationRequest.builder().operation(DELETE)
                .accounts(List.of("a")).issuerServices(List.of(ISSUER)).build();
    }

    private static KeysByOwnerAndAccountAndServiceVo row(
            String account, String user, String id, Instant created) {
        return KeysByOwnerAndAccountAndServiceVo.builder().ncaId(account).ownerType(USER)
                .ownerId(user).issuerServiceId(ISSUER).keyId(id).createdAt(created)
                .keyStatus(KeyStatus.ACTIVE).build();
    }
}
