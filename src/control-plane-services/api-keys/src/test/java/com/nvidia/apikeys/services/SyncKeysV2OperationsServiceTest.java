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

import com.nvidia.apikeys.caching.CachingKeysLoader;
import com.nvidia.apikeys.caching.IntrospectionLocalCache;
import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.dto.keysV2.AccountAdminKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.KeyOperationResponse;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.PlatformAdminKeysOperationRequest;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.services.SyncKeysV2OperationsService.BulkOperationException;
import com.nvidia.apikeys.utils.JsonUtils;
import com.nvidia.apikeys.vo.KeyOperationStatus;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import java.time.Instant;
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
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.web.context.request.RequestContextHolder;
import org.springframework.web.context.request.ServletRequestAttributes;

@ExtendWith({MockitoExtension.class, OutputCaptureExtension.class})
class SyncKeysV2OperationsServiceTest {

    @Mock
    private CachingKeysLoader keyCache;
    @Mock
    private IntrospectionLocalCache introspectionCache;

    private static final Instant CUTOFF = Instant.parse("2026-10-21T12:00:00Z");
    private static final String ISSUER = "cloud-functions";

    @Mock
    private KeysV2Service keys;
    @Mock
    private ServicesService services;
    @Mock
    private KeysV2BulkSelectionService selection;
    @Mock
    private MillisecondPrecisionClock clock;
    private SyncKeysV2OperationsService service;

    @BeforeEach
    void setUp() {
        var legacy = new LegacyAccountTranslator(NakProperties.builder()
                .ncaId("legacy").serviceIdMap(Map.of(ISSUER, "legacy-issuer")).build());
        lenient().when(clock.instant()).thenReturn(CUTOFF);
        service = new SyncKeysV2OperationsService(keys, services, legacy, clock, 10, selection, keyCache, introspectionCache);
        lenient().when(services.getServiceById(ISSUER))
                .thenReturn(Optional.of(ServiceVo.builder().serviceId(ISSUER).build()));
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

        assertThat(response.getCompletedCount()).isOne();
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
                .getCompletedCount()).isZero();
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
                .operation(DELETE).users(List.of(user)).build()).getCompletedCount()).isOne();

        verify(keys).deleteKeyById("a", USER, user, ISSUER, "key");
    }

    @Test
    void auditIdentityDoesNotAddAHeaderLengthLimit() {
        var request = new MockHttpServletRequest();
        request.addHeader("NVCF-ACTOR-ID", "a".repeat(257));
        RequestContextHolder.setRequestAttributes(new ServletRequestAttributes(request));
        try {
            assertThat(service.operateAsAccountAdmin("a", accountRequest()).getCompletedCount()).isZero();
        } finally {
            RequestContextHolder.resetRequestAttributes();
        }
    }

    @ParameterizedTest
    @ValueSource(ints = {0, 2})
    void returnsCompletedSharedResponse(int keyCount) {
        var finishedAt = CUTOFF.plusSeconds(2);
        when(clock.instant()).thenReturn(CUTOFF, finishedAt);
        when(selection.select("a", "u", ISSUER, CUTOFF)).thenReturn(
                java.util.stream.IntStream.range(0, keyCount)
                        .mapToObj(i -> row("a", "u", "key-" + i, CUTOFF)));

        var response = service.operateAsAccountAdmin("a", accountRequest());

        assertThat(response.getOperationId()).isNotNull();
        assertThat(response.getOperation()).isEqualTo(REVOKED);
        assertThat(response.getStatus()).isEqualTo(KeyOperationStatus.COMPLETED);
        assertThat(response.getMatchedCount()).isEqualTo((long) keyCount);
        assertThat(response.getCompletedCount()).isEqualTo((long) keyCount);
        assertThat(response.getFailedCount()).isZero();
        assertThat(response.getSubmittedAt()).isEqualTo(CUTOFF);
        assertThat(response.getUpdatedAt()).isEqualTo(finishedAt);
        if (keyCount == 0) {
            verifyNoInteractions(keyCache, introspectionCache);
        } else {
            verify(keyCache).invalidate();
            verify(introspectionCache).invalidate();
        }
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
        assertThat(failure.getBody().getProperties()).containsEntry("partialApplication", true);
        var result = failedResult(failure);
        assertThat(result.getMatchedCount()).isEqualTo(2);
        assertThat(result.getCompletedCount()).isOne();
        assertThat(result.getFailedCount()).isOne();
        verify(keyCache).invalidate();
        verify(introspectionCache).invalidate();
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
        var result = failedResult(failure);
        assertThat(result.getMatchedCount()).isOne();
        assertThat(result.getCompletedCount()).isZero();
        assertThat(result.getFailedCount()).isOne();
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
        var result = failedResult(failure);
        assertThat(result.getMatchedCount()).isOne();
        assertThat(result.getCompletedCount()).isOne();
        assertThat(result.getFailedCount()).isZero();
        verify(keys).updateKeyStatus("a", USER, "u", ISSUER, "key", KeyStatus.REVOKED);
    }

    @Test
    void selectedKeyDeletedConcurrentlyIsAlreadyComplete() {
        when(selection.select("a", "u", ISSUER, CUTOFF))
                .thenReturn(Stream.of(row("a", "u", "deleted", CUTOFF)));
        doThrow(new NotFoundException("Key not found")).when(keys)
                .deleteKeyById("a", USER, "u", ISSUER, "deleted");

        var result = service.operateAsAccountAdmin("a", accountRequest().toBuilder().operation(DELETE).build());

        assertThat(result.getStatus()).isEqualTo(KeyOperationStatus.COMPLETED);
        assertThat(result.getMatchedCount()).isOne();
        assertThat(result.getCompletedCount()).isOne();
        assertThat(result.getFailedCount()).isZero();
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void emitsCompleteAuditOutcomeWithEscapedIdentity(boolean fail, CapturedOutput output) {
        var request = new MockHttpServletRequest();
        request.addHeader("NVCF-ACTOR-ID", "actor\nid");
        RequestContextHolder.setRequestAttributes(new ServletRequestAttributes(request));
        when(selection.select("a", "u", ISSUER, CUTOFF))
                .thenReturn(Stream.of(row("a", "u", "key", CUTOFF)));
        if (fail) {
            when(keys.updateKeyStatus("a", USER, "u", ISSUER, "key", KeyStatus.REVOKED))
                    .thenThrow(new IllegalStateException("write failed"));
        }
        try {
            var result = fail
                    ? failedResult(assertThrows(BulkOperationException.class,
                            () -> service.operateAsAccountAdmin("a", accountRequest())))
                    : service.operateAsAccountAdmin("a", accountRequest());
            var prefix = "Synchronous bulk operation finished: ";
            var events = output.getOut().lines().filter(line -> line.contains(prefix)).toList();
            assertThat(events).hasSize(1);
            var event = events.getFirst();
            var mapper = JsonUtils.getRequestResponseJsonMapper();
            var json = mapper.readTree(event.substring(event.indexOf(prefix) + prefix.length()));
            assertThat(json.get("actor").asString()).isEqualTo("ACCOUNT_ADMIN");
            assertThat(json.get("actorId").asString()).isEqualTo("actor\nid");
            assertThat(json.get("accounts")).isEqualTo(mapper.valueToTree(List.of("a")));
            assertThat(json.get("users")).isEqualTo(mapper.valueToTree(List.of("u")));
            assertThat(json.get("issuerServices")).isEqualTo(mapper.valueToTree(List.of(ISSUER)));
            assertThat(mapper.treeToValue(json.get("bulkOperation"), KeyOperationResponse.class)).isEqualTo(result);
        } finally {
            RequestContextHolder.resetRequestAttributes();
        }
    }

    private static KeyOperationResponse failedResult(BulkOperationException failure) {
        var result = (KeyOperationResponse) failure.getBody().getProperties().get("bulkOperation");
        assertThat(result.getOperationId()).isNotNull();
        assertThat(result.getStatus()).isEqualTo(KeyOperationStatus.FAILED);
        assertThat(result.getSubmittedAt()).isEqualTo(CUTOFF);
        assertThat(result.getUpdatedAt()).isEqualTo(CUTOFF);
        return result;
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
