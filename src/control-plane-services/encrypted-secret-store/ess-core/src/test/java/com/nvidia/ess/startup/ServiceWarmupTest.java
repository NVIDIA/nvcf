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
package com.nvidia.ess.startup;

import static java.lang.String.format;
import static org.awaitility.Awaitility.await;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.doAnswer;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.spy;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.jwk.JWKSet;
import com.nvidia.ess.auth.jwk.JwkCacheProperties;
import com.nvidia.ess.auth.jwk.JwkSetService;
import com.nvidia.ess.persistence.models.AuthorizationUdt;
import com.nvidia.ess.persistence.models.NamespaceModel;
import com.nvidia.ess.persistence.services.NamespaceService;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.crypto.key.predicate.MulticallErrHandlingPredicate.ErrorReportingPredicate;
import com.nvidia.ess.encryption.exceptions.KeyFetchError;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import java.time.Duration;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentMatchers;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.boot.health.contributor.Status;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

@ExtendWith({
        OutputCaptureExtension.class,
        MockitoExtension.class
})
class ServiceWarmupTest {

    @Mock
    private NamespaceService namespaceService;

    @Mock
    private JwkSetService jwkSetService;

    @Mock
    private CrudEncryptionKeyService crudEncryptionKeyService;

    private final JwkCacheProperties jwkCacheProperties = new JwkCacheProperties();
    private final EncryptionProperties encryptionProperties = new EncryptionProperties();

    private ServiceWarmup serviceWarmup;

    private static final Duration TEST_TIMEOUT_DURATION = Duration.ofSeconds(2);

    AtomicReference<Exception> jwksCacheWarmupError = new AtomicReference<>();
    AtomicReference<Exception> nekCacheWarmupError = new AtomicReference<>();

    @BeforeEach
    public void setup() {
        // Reset error tracking to avoid test pollution
        jwksCacheWarmupError.set(null);
        nekCacheWarmupError.set(null);

        serviceWarmup =
                spy(new ServiceWarmup(true, TEST_TIMEOUT_DURATION, jwkSetService, namespaceService,
                        jwkCacheProperties, crudEncryptionKeyService, encryptionProperties));
        serviceWarmup.setSelf(serviceWarmup);
    }

    private void recordJwksCacheWarmupError() {
        doAnswer(invocationOnMock -> {
            try {
                return invocationOnMock.callRealMethod();
            } catch (Exception e) {
                jwksCacheWarmupError.set(e);
                throw e;
            }
        }).when(serviceWarmup).jwksCacheWarmup();
    }

    private void recordNekCacheWarmupError() {
        doAnswer(invocationOnMock -> {
            try {
                return invocationOnMock.callRealMethod();
            } catch (Exception e) {
                nekCacheWarmupError.set(e);
                throw e;
            }
        }).when(serviceWarmup).nekCacheWarmup();
    }


    @Test
    void onApplicationEvent_warmupTransitionsHealthFromDownToUp() {
        var namespaceModelWithNonNotaryAuth = mock(NamespaceModel.class);
        var namespaceModelWithNotary = mock(NamespaceModel.class);
        var encryptionKeyModel = mock(EncryptionKeyModel.class);

        when(namespaceModelWithNonNotaryAuth.getOauthAuthorizations())
                .thenReturn(Map.of(UUID.randomUUID().toString(), AuthorizationUdt.builder()
                        .id(UUID.randomUUID().toString())
                        .jwksUrl(UUID.randomUUID().toString())
                        .build()));
        when(namespaceModelWithNonNotaryAuth.getNotaryAuthorizations())
                .thenReturn(Map.of(UUID.randomUUID().toString(), AuthorizationUdt.builder()
                        .id(UUID.randomUUID().toString())
                        .jwksUrl(UUID.randomUUID().toString())
                        .build()));

        when(namespaceService.getNamespaces())
                .thenReturn(Flux.just(namespaceModelWithNotary, namespaceModelWithNonNotaryAuth));

        when(jwkSetService.getJwkSet(anyString()))
                .thenReturn(Mono.just(mock(JWKSet.class)));

        when(namespaceModelWithNonNotaryAuth.getNamespace())
                .thenReturn(UUID.randomUUID().toString());
        when(namespaceModelWithNotary.getNamespace())
                .thenReturn(UUID.randomUUID().toString());
        when(encryptionKeyModel.getNamespace())
                .thenReturn(UUID.randomUUID().toString());
        when(encryptionKeyModel.getKid())
                .thenReturn(UUID.randomUUID().toString());

        when(crudEncryptionKeyService.getKey(anyString(), ArgumentMatchers.<ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError>>any()))
                .thenReturn(Mono.just(encryptionKeyModel));

        when(crudEncryptionKeyService.getKey(anyString(), anyString(), ArgumentMatchers.<ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError>>any()))
                .thenReturn(Mono.just(encryptionKeyModel));

        recordJwksCacheWarmupError();
        recordNekCacheWarmupError();

        assertFalse(serviceWarmup.isWarmingComplete());
        assertEquals(Status.DOWN, serviceWarmup.health().getStatus());
        serviceWarmup.onApplicationEvent(mock(ApplicationReadyEvent.class));
        assertTrue(serviceWarmup.isWarmingComplete());
        assertEquals(Status.UP, serviceWarmup.health().getStatus());

        assertNull(jwksCacheWarmupError.get());
        assertNull(nekCacheWarmupError.get());
    }

    @Test
    void onApplicationEvent_onLongExecutionTimeAndPartialFailure_warmupIncompleteAndHealthUp() {
        var namespaceModelWithNonNotaryAuth = mock(NamespaceModel.class);
        var namespaceModelWithNotary = mock(NamespaceModel.class);
        String jwksUrl1 = UUID.randomUUID().toString();
        String jwksUrl2 = UUID.randomUUID().toString();

        when(namespaceModelWithNonNotaryAuth.getOauthAuthorizations())
                .thenReturn(Map.of(UUID.randomUUID().toString(), AuthorizationUdt.builder()
                        .id(UUID.randomUUID().toString())
                        .jwksUrl(jwksUrl1)
                        .build()));
        when(namespaceModelWithNonNotaryAuth.getNotaryAuthorizations())
                .thenReturn(Map.of(UUID.randomUUID().toString(), AuthorizationUdt.builder()
                        .id(UUID.randomUUID().toString())
                        .jwksUrl(jwksUrl2)
                        .build()));

        when(namespaceService.getNamespaces())
                .thenReturn(Flux.just(namespaceModelWithNotary, namespaceModelWithNonNotaryAuth));

        when(jwkSetService.getJwkSet(jwksUrl1))
                .thenReturn(Mono.delay(TEST_TIMEOUT_DURATION.multipliedBy(4))
                        .thenReturn(mock(JWKSet.class)));

        when(jwkSetService.getJwkSet(jwksUrl2))
                .thenReturn(Mono.error(() -> new RuntimeException("some error")));

        when(namespaceModelWithNonNotaryAuth.getNamespace())
                .thenReturn(UUID.randomUUID().toString());
        when(namespaceModelWithNotary.getNamespace())
                .thenReturn(UUID.randomUUID().toString());
        when(crudEncryptionKeyService.getKey(anyString(), ArgumentMatchers.<ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError>>any()))
                .thenReturn(Mono.empty());
        recordJwksCacheWarmupError();
        recordNekCacheWarmupError();

        assertFalse(serviceWarmup.isWarmingComplete());
        assertEquals(Status.DOWN, serviceWarmup.health().getStatus());

        // run warmup on another thread
        CompletableFuture.runAsync(() -> serviceWarmup.onApplicationEvent(mock(ApplicationReadyEvent.class)));
        // Wait for timeout to pass. The timeout (BootWarmupBase.timeoutExceeded) was set in
        // @BeforeEach as Instant.now() + TEST_TIMEOUT_DURATION. We need to wait for enough time
        // for that instant to pass but not long enough for the warmup-tasks themselves to finish.
        // Adding 2000ms buffer to account for any clock-jitter (i.e. if the call to Instant.now() to
        // set `BootWarmupBase.timeoutExceeded` yielded a later timestamp than the call to the clock
        // to set the poll-delay's end-time even though the latter call occurred after the former).
        await().pollDelay(TEST_TIMEOUT_DURATION.plus(Duration.ofMillis(2000))).until(() -> true);

        // Warmup not complete yet as the warmup-tasks shouldn't have finished yet.
        // status has no meaning (even after `maxTimeout` has passed) until the first
        // call to `health()`.
        assertFalse(
                serviceWarmup.isWarmingComplete());
        // The first call to `health()` after the timeout (or completion of all warmup-tasks)
        // returns the status as `UP`. If the warmup tasks are still incomplete but the timeout
        // has passed, this first call to `health()` also flips `isWarmingComplete()` from `false`
        // to `true`. Thus, it is important to not flip the order of these two assertions.
        assertEquals(Status.UP, serviceWarmup.health().getStatus());
        // no error since the execution did not end yet
        assertNull(jwksCacheWarmupError.get());
        assertNull(nekCacheWarmupError.get());
    }


    @Test
    void onApplicationEvent_onPartialFailure_warmupCompleteAndHealthUpWithLoggedFailure(CapturedOutput output) {
        String warmupError = "failed task " + UUID.randomUUID();
        var namespaceModel = mock(NamespaceModel.class);

        when(namespaceModel.getOauthAuthorizations())
                .thenReturn(Map.of(UUID.randomUUID().toString(), AuthorizationUdt.builder()
                        .id(UUID.randomUUID().toString())
                        .jwksUrl(UUID.randomUUID().toString())
                        .build()));
        when(namespaceModel.getNamespace())
                .thenReturn(UUID.randomUUID().toString());

        when(namespaceService.getNamespaces())
                .thenReturn(Flux.just(namespaceModel));

        when(jwkSetService.getJwkSet(anyString()))
                .thenReturn(Mono.error(() -> new RuntimeException(warmupError)));

        when(crudEncryptionKeyService.getKey(anyString(), ArgumentMatchers.<ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError>>any()))
                .thenReturn(Mono.error(() -> new RuntimeException(warmupError)));
        recordJwksCacheWarmupError();
        recordNekCacheWarmupError();

        assertFalse(serviceWarmup.isWarmingComplete());
        assertEquals(Status.DOWN, serviceWarmup.health().getStatus());
        serviceWarmup.onApplicationEvent(mock(ApplicationReadyEvent.class));
        assertTrue(serviceWarmup.isWarmingComplete());
        assertEquals(Status.UP, serviceWarmup.health().getStatus());

        assertNotNull(jwksCacheWarmupError.get());
        assertNotNull(nekCacheWarmupError.get());
        var outputString = output.getAll();

        // cannot match warmupError, pipeline test run does not log suppressed errors
        assertTrue(outputString.contains("warmup with 1 errors"));
        assertTrue(outputString.contains(format("%s warmup completed", serviceWarmup.getClass().getName())));
    }

    @Test
    void onApplicationEvent_onHardFailure_warmupCompleteAndHealthUp(CapturedOutput output) {
        String warmupError = "failed task " + UUID.randomUUID();
        when(namespaceService.getNamespaces())
                .thenReturn(Flux.error(() -> new RuntimeException(warmupError)));

        recordJwksCacheWarmupError();
        recordNekCacheWarmupError();

        assertFalse(serviceWarmup.isWarmingComplete());
        assertEquals(Status.DOWN, serviceWarmup.health().getStatus());
        serviceWarmup.onApplicationEvent(mock(ApplicationReadyEvent.class));
        assertTrue(serviceWarmup.isWarmingComplete());
        assertEquals(Status.UP, serviceWarmup.health().getStatus());

        assertNotNull(jwksCacheWarmupError.get());
        assertNotNull(nekCacheWarmupError.get());

        var outputString = output.getAll();

        assertTrue(outputString.contains(warmupError));
        assertTrue(outputString.contains(format("%s warmup completed", serviceWarmup.getClass().getName())));
    }


    @Test
    void onApplicationEvent_onNekWarmupMissingKeyException_warmupCompleteAndHealthUp() {
        var namespaceModel = mock(NamespaceModel.class);
        var encryptionKeyModel = mock(EncryptionKeyModel.class);
        when(namespaceService.getNamespaces())
                .thenReturn(Flux.just(namespaceModel));

        doNothing().when(serviceWarmup).jwksCacheWarmup();

        when(namespaceModel.getNamespace())
                .thenReturn(UUID.randomUUID().toString());
        when(encryptionKeyModel.getNamespace())
                .thenReturn(UUID.randomUUID().toString());
        when(encryptionKeyModel.getKid())
                .thenReturn(UUID.randomUUID().toString());

        when(crudEncryptionKeyService.getKey(anyString(), ArgumentMatchers.<ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError>>any()))
                .thenReturn(Mono.just(encryptionKeyModel));

        when(crudEncryptionKeyService.getKey(anyString(), anyString(), ArgumentMatchers.<ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError>>any()))
                .thenReturn(Mono.error(() -> new MissingKeyException("some error")));


        recordNekCacheWarmupError();

        assertFalse(serviceWarmup.isWarmingComplete());
        assertEquals(Status.DOWN, serviceWarmup.health().getStatus());
        serviceWarmup.onApplicationEvent(mock(ApplicationReadyEvent.class));
        assertTrue(serviceWarmup.isWarmingComplete());
        assertEquals(Status.UP, serviceWarmup.health().getStatus());

        assertNull(nekCacheWarmupError.get());
    }

}
