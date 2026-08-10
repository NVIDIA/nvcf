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
package com.nvidia.ess.services;

import static com.nvidia.ess.constants.OpenTelemetryAttributes.LWT_WRITE_FAILURE_OPERATION_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.PARTIAL_CREATE_TYPE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.SECRET_CAS_ERROR_ACTUAL_VERSION_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.SECRET_CAS_ERROR_PROVIDED_VERSION_KEY;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY;
import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;
import static com.nvidia.ess.util.TestConstants.TEST_NEK_ID;
import static com.nvidia.ess.util.TestConstants.TEST_PROBLEM_SUMMARY;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_DATA_CIPHERTEXT;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_PATH;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_VERSION;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.constants.OpenTelemetryAttributes.PartialCreateType;
import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetriesExhaustedTooManyRequestsException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.metrics.CustomMetricsRegistry.LwtOperation;
import com.nvidia.ess.persistence.models.SecretVersionModel;
import com.nvidia.ess.persistence.repositories.SecretVersionRepository;
import com.nvidia.ess.persistence.services.SecretVersionService;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.util.ResultOrErrorOrEmpty;
import java.time.Instant;
import java.util.Arrays;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.context.ContextView;

@ExtendWith(MockitoExtension.class)
public class SecretVersionServiceTest {
  
  @Mock
  private SecretVersionRepository repository;

  @Mock
  private CustomMetricsRegistry customMetricsRegistry;

  @Mock
  private TelemetryComponents telemetryComponents;

  @InjectMocks
  private SecretVersionService secretVersionService;

  private static final SecretVersionModel TEST_SECRET = SecretVersionModel.builder()
      .namespace(TEST_NAMESPACE)
      .entity(TEST_ENTITY)
      .secretPath(TEST_SECRET_PATH)
      .version(TEST_SECRET_VERSION)
      .currentVersion(TEST_SECRET_VERSION)
      .createdAt(Instant.ofEpochMilli(Uuids.unixTimestamp(TEST_SECRET_VERSION)))
      .encryptedAt(Instant.ofEpochMilli(Uuids.unixTimestamp(TEST_SECRET_VERSION)))
      .encryptedByKid(TEST_NEK_ID)
      .value(TEST_SECRET_DATA_CIPHERTEXT)
      .build();

  @Test
  void testGetSpecificSecretVersion_repositoryYieldsEmptyResult_notFoundException() {

    doReturn(Mono.empty()).when(repository).findByNamespaceAndEntityAndSecretPathAndVersion(eq(TEST_NAMESPACE),
        eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(TEST_SECRET_VERSION));

    StepVerifier.create(secretVersionService.getSecretVersion(TEST_NAMESPACE, TEST_ENTITY,
            TEST_SECRET_PATH, TEST_SECRET_VERSION, NotFoundException.class))
        .expectError(NotFoundException.class)
        .verify();
    
    verify(repository, times(1)).findByNamespaceAndEntityAndSecretPathAndVersion(eq(TEST_NAMESPACE),
        eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(TEST_SECRET_VERSION));
  }

  @Test
  void testGetSpecificSecretVersion_repositoryYieldsResult_echoResult() {
    doReturn(Mono.just(TEST_SECRET)).when(repository).findByNamespaceAndEntityAndSecretPathAndVersion(eq(TEST_NAMESPACE),
        eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(TEST_SECRET_VERSION));

    StepVerifier.create(secretVersionService.getSecretVersion(TEST_NAMESPACE, TEST_ENTITY,
            TEST_SECRET_PATH, TEST_SECRET_VERSION, NotFoundException.class))
        .expectNextMatches(actualSecret -> TEST_SECRET.equals(actualSecret))
        .expectComplete()
        .verify();

    verify(repository, times(1)).findByNamespaceAndEntityAndSecretPathAndVersion(eq(TEST_NAMESPACE),
        eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(TEST_SECRET_VERSION));
  }

  @Test
  void testGetLatestSecretVersion_repositoryYieldsEmptyResult_notFoundException() {

    doReturn(Mono.empty()).when(repository).findFirstByNamespaceAndEntityAndSecretPath(eq(TEST_NAMESPACE),
            eq(TEST_ENTITY), eq(TEST_SECRET_PATH));

    StepVerifier.create(secretVersionService.getSecretVersion(TEST_NAMESPACE, TEST_ENTITY,
            TEST_SECRET_PATH, NotFoundException.class))
        .expectError(NotFoundException.class)
        .verify();

    verify(repository, times(1)).findFirstByNamespaceAndEntityAndSecretPath(eq(TEST_NAMESPACE),
        eq(TEST_ENTITY), eq(TEST_SECRET_PATH));
  }

  @Test
  void testGetLatestSecretVersion_repositoryYieldsResult_echoResult() {

    doReturn(Mono.just(TEST_SECRET)).when(repository).findFirstByNamespaceAndEntityAndSecretPath(eq(TEST_NAMESPACE),
            eq(TEST_ENTITY), eq(TEST_SECRET_PATH));

    StepVerifier.create(secretVersionService.getSecretVersion(TEST_NAMESPACE, TEST_ENTITY,
            TEST_SECRET_PATH, NotFoundException.class))
        .expectNextMatches(actualSecret -> TEST_SECRET.equals(actualSecret))
        .expectComplete()
        .verify();

    verify(repository, times(1)).findFirstByNamespaceAndEntityAndSecretPath(eq(TEST_NAMESPACE),
        eq(TEST_ENTITY), eq(TEST_SECRET_PATH));
  }

  /*
   * === Repository operations as part of secretVersionService.validateCurrentVersionAndGenValidNewVersion() ===
   * 
   * If CAS requested, fetch-and-check-cas-secret-version:
   *     -- repository.findFirstByNamespaceAndEntityAndSecretPath(namespace, entity, secretPath) to get the
   *        most recent secret-payload version corresponding to this secret-path (if stored).
   *     -- Check whether casVersion is the same as `version` in the fetched `SecretVersionModel`.
   *     
   *     [Possible outcomes: error, empty-output, non-empty-output-but-recent-diff-from-cas,
   *                         non-empty-output-but-recent-diff-from-current, non-empty-output-and-recent-same-as-current]
   * 
   * 
   * If CAS not requested, fetch-current-secret-version:
   *     -- repository.findFirstByNamespaceAndEntityAndSecretPath(namespace, entity, secretPath) to get the
   *        most recent secret-payload version corresponding to this secret-path (if stored).
   * 
   *     [Possible outcomes: error, empty-output, non-empty-output-but-recent-diff-from-current,
   *      non-empty-output-and-recent-same-as-current]
   * 
   * gen-and-check new-version:
   *     -- Generate a new `timeuuid` value for the new version of the secret-payload to be inserted.
   *     -- Check whether the newly generated value for current_version is more recent than the value
   *        of the most recent secret-payload `version` fetched from the previous step.
   * 
   *     [Possible outcomes: recency-check-failure, recency-check-passed]
   * 
   * If empty-output:
   *     If CAS requested: error.
   *     If CAS not requested: Pair(NULL, generated-new-current_version)
   * Else:
   *     Pair(fetched-current_version, generated-new-current_version)
   * 
   */

  private static enum VersionFetchResultStatus {
    // DB-fetch failed for either `fetch-and-check-cas-secret-version` (CAS)
    // or `fetch-current-secret-version` (non-CAS)
    FETCH_ERROR,

    // DB-fetch returned an empty result for `fetch-and-check-cas-secret-version` (CAS)
    // or `fetch-current-secret-version` (non-CAS)
    FETCH_EMPTY,
  
    // DB-fetch returned a result for `fetch-and-check-cas-secret-version` (CAS)
    // but `version[CAS] != version[fetched]` i.e. CAS-version is not the most recent
    // secret-version.
    //
    // Not applicable for non-CAS test-cases.
    FETCH_NONEMPTY_RECENT_DIFF_FROM_CAS,

    // DB-fetch returned a result for `fetch-and-check-cas-secret-version` (CAS)
    // and `version[CAS] == version[fetched]` but `version[fetched] != current_version`
    // i.e. CAS-version is the most recent secret-version but the value of `current_version`
    // is out of sync. But this should not block the CAS-update (the present value of
    // `current_version` should still be used in the optimistic-locking condition of the LWT
    // write that inserts the new secret-payload, while also changing the value of `current_version`
    // to this new secret-payload's version {after checking that the generated new-secret-payload-version
    // is more recent than `version[fetched]`; see `GenNewVersionResultStatus` below}).
    //
    // For non-CAS test-cases, DB-fetch returned a non-empty `current_version` but
    // `version[fetched] != current_version` (out of sync). As with CAS-updates above, this
    // should not block a non-CAS update and the new value of `current_version` should reflect
    // the value of the newly written secret-payload's version.
    //
    FETCH_NONEMPTY_RECENT_DIFF_FROM_CURRENT,

    // DB-fetch returned a result for `fetch-and-check-cas-secret-version` (CAS)
    // and `version[CAS] == version[fetched] == current_version`.
    //
    // For non-CAS test-cases, DB-fetch returned a non-empty `current_version` and
    // `version[fetched] == current_version`.
    FETCH_NONEMPTY_CURRENT
  }

  private static enum GenNewVersionResultStatus {
    // (Applies for both CAS and non-CAS): The fetched `current_version` from DB
    // is more recent than the generated `timeuuid` on the app-server (replacement
    // current_version).
    //
    // Does not apply when the fetch (see above) was empty or failed with an error.
    RECENCY_CHECK_FAILED,
    
    // (Applies for both CAS and non-CAS): The generated `timeuuid` on the app-server
    // (replacement current_version) is more recent than the fetched `current_version`
    // from DB.
    //
    // Does not apply when the fetch (see above) was empty or failed with an error.
    RECENCY_CHECK_PASSED,

    // When `fetch-and-check-cas-secret-version` (CAS) or `fetch-current-secret-version`
    // (non-CAS) return an empty result, a new `timeuuid` value of current_version is
    // generated but its relative recency isn't check (nothing to compare against).
    RECENCY_CHECK_NOT_ATTEMPTED
  }

  private static Stream<Arguments> argsTestValidateCurrentVersionAndGenValidNewVersion() {

    return Stream.of(false, true).flatMap(isCasRequest ->

      Arrays.asList(VersionFetchResultStatus.values()).stream()
          // Disallow nonsensical test-case: (not-CAS-request AND FETCH_NONEMPTY_RECENT_DIFF_FROM_CAS).
          // Check code-comments above.
          .filter(fetchStatus -> isCasRequest ||
                                fetchStatus != VersionFetchResultStatus.FETCH_NONEMPTY_RECENT_DIFF_FROM_CAS)
          .flatMap(fetchStatus ->
  
            Arrays.asList(GenNewVersionResultStatus.values()).stream()
                // Allow RECENCY_CHECK_[PASSED|FAILED] only when fetch returned a successful non-empty
                // result i.e. fetchStatus == FETCH_NONEMPTY_CURRENT, and allow RECENCY_CHECK_NOT_ATTEMPTED
                // only when fetch did not return a successful non-empty result.
                .filter(genStatus ->
                          (genStatus == GenNewVersionResultStatus.RECENCY_CHECK_NOT_ATTEMPTED) ^
                              (fetchStatus == VersionFetchResultStatus.FETCH_NONEMPTY_CURRENT ||
                               fetchStatus == VersionFetchResultStatus.FETCH_NONEMPTY_RECENT_DIFF_FROM_CURRENT))
                .map(genStatus -> {

                  var currentVersionStaticColInDB =
                      genStatus == GenNewVersionResultStatus.RECENCY_CHECK_FAILED
                          // `current_version` from the DB is in the future relative to the app-server's
                          // clock-time (the 300-second offset is a contrivance to simulate a recency-check
                          // failure scenario once the replacement-current_version timeuuid is generated).
                          ? Uuids.endOf(Instant.now().plusSeconds(300).toEpochMilli())
                          // `current_version` from the DB should be in the past relative to the app-server's
                          // clock-time at the time of generation of the replacement-current_version timeuuid).
                          // Use 5 seconds to avoid flakiness when Uuids.timeBased() runs in the service (same ms or low resolution).
                          : Uuids.startOf(Instant.now().minusSeconds(5).toEpochMilli());

                  var mostRecentVersionInDB =
                      fetchStatus == VersionFetchResultStatus.FETCH_NONEMPTY_RECENT_DIFF_FROM_CURRENT
                          // (Relevant to CAS requests) To simulate the retrieval of a `SecretVersionModel` row corresponding
                          // to the most recent secret-payload version (if at least one is stored) that is more recent than
                          // `current_version` due to a difference between the commit-order of concurrent (non-CAS) updates to
                          // this secret and the respective version timeuuid values generated within the ESS service prior to
                          // writing, generate a `timeuuid` that is *positively* offset from `currentVersionInDB`.
                          //
                          // When a subsequent CAS update is attempted, it is expected that the CAS update still succeeds
                          // as long as the specified CAS version is the same as the most-recent-version (and not
                          // `current_version`).
                          //
                          // Non-CAS updates should still succeed irrespective of this discrepancy between most-recent-version
                          // and `current_version`.
                          //
                          // Irrespective of the type of secret-update (CAS or non-CAS), the value of `current_version` after
                          // the successful update should be the same as the version of the newly written secret-payload.
                          //
                          ? Uuids.startOf(
                                Instant.ofEpochMilli(Uuids.unixTimestamp(currentVersionStaticColInDB))
                                    .plusSeconds(1)
                                    .toEpochMilli()
                            )
                          // Otherwise, `versionInDB` for the retrieved `SecretVersionModel`
                          // is equal to `currentVersionInDB` and the `fetch-and-check-cas-secret-version`
                          // can pass.
                          : currentVersionStaticColInDB;

                  ResultOrErrorOrEmpty<SecretVersionModel> mostRecentSecretFromDB =
                      fetchStatus != VersionFetchResultStatus.FETCH_ERROR
                          ? ResultOrErrorOrEmpty.fromResultOrEmpty(
                                fetchStatus != VersionFetchResultStatus.FETCH_EMPTY
                                    ? SecretVersionModel.builder()
                                        .namespace(TEST_NAMESPACE)
                                        .entity(TEST_ENTITY)
                                        .secretPath(TEST_SECRET_PATH)
                                        .createdAt(Instant.ofEpochMilli(Uuids.unixTimestamp(mostRecentVersionInDB)))
                                        .encryptedAt(Instant.ofEpochMilli(Uuids.unixTimestamp(mostRecentVersionInDB)))
                                        .encryptedByKid(TEST_NEK_ID)
                                        .value("TEST")
                                        .version(mostRecentVersionInDB)
                                        .currentVersion(currentVersionStaticColInDB)
                                        .build()
                                    : null
                            )
                          : ResultOrErrorOrEmpty.fromError(new RuntimeException());

                  var requestedCasVersion = Optional.ofNullable(
                      isCasRequest
                          ? (fetchStatus == VersionFetchResultStatus.FETCH_NONEMPTY_RECENT_DIFF_FROM_CAS
                                ? Uuids.startOf(
                                      Instant.ofEpochMilli(Uuids.unixTimestamp(mostRecentVersionInDB))
                                          .minusSeconds(10)
                                          .toEpochMilli()
                                  )
                                : mostRecentVersionInDB
                            ) 
                          : null
                  );

                  // First element of the Pair<UUID, UUID> returned (second is generated and therefore
                  // cannot be checked directly).
                  //
                  ResultOrErrorOrEmpty<UUID> expectedResult;

                  if (fetchStatus == VersionFetchResultStatus.FETCH_ERROR) {
                    // When the initial DB fetch threw an error, a `RetryableException` is expected
                    // (if an outer-retry-loop exists and has retries left, the whole execution-pipeline
                    // is retried from scratch; otherwise a 500 error is returned).
                    expectedResult = ResultOrErrorOrEmpty.fromError(
                        new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test"))
                    );

                  } else if (fetchStatus == VersionFetchResultStatus.FETCH_NONEMPTY_RECENT_DIFF_FROM_CAS) {
                    // When CAS is requested and the CAS version exists but is not the most recent secret-version
                    // a 409 error is expected.
                    expectedResult = ResultOrErrorOrEmpty.fromError(new ConflictException("conflict"));

                  } else if (isCasRequest && fetchStatus == VersionFetchResultStatus.FETCH_EMPTY) {
                    // When CAS is requested and no payload exists corresponding to this secret, a 409 error is expected.
                    expectedResult = ResultOrErrorOrEmpty.fromError(new ConflictException("conflict"));

                  } else if (genStatus == GenNewVersionResultStatus.RECENCY_CHECK_FAILED) {
                    // If the generated `timeuuid` value of the replacement current_version
                    // failed a recency-check, a `RetryableException` is returned.
                    expectedResult = ResultOrErrorOrEmpty.fromError(
                        new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test"))
                    );

                  } else {
                    // Non-CAS: FETCH_EMPTY, FETCH_NONEMPTY_RECENT_DIFF_FROM_CURRENT, FETCH_NONEMPTY_CURRENT
                    // CAS: FETCH_NONEMPTY_RECENT_DIFF_FROM_CURRENT, FETCH_NONEMPTY_CURRENT
                    expectedResult = ResultOrErrorOrEmpty.fromResultOrEmpty(
                        fetchStatus == VersionFetchResultStatus.FETCH_EMPTY ? null : currentVersionStaticColInDB
                    );
                  }

                  return Arguments.of(
                      isCasRequest,
                      TEST_NAMESPACE,
                      TEST_ENTITY,
                      TEST_SECRET_PATH,
                      requestedCasVersion,
                      mostRecentSecretFromDB,
                      expectedResult
                  );
                })
          )
    );
  }

  @ParameterizedTest
  @MethodSource("argsTestValidateCurrentVersionAndGenValidNewVersion")
  void testValidateCurrentVersionAndGenValidNewVersion(boolean isCasRequest, String namespace, String entity, String secretPath,
      Optional<UUID> requestedCasVersion, ResultOrErrorOrEmpty<SecretVersionModel> mostRecentSecretFromDB,
      ResultOrErrorOrEmpty<UUID> expectedResult) {

    boolean expectCasErrorTelemetry = isCasRequest && expectedResult.isError() &&
        ((requestedCasVersion.isPresent() && !mostRecentSecretFromDB.hasResult() && !mostRecentSecretFromDB.isError()) ||
         (requestedCasVersion.isPresent() && mostRecentSecretFromDB.getResultOrEmpty() != null &&
              !requestedCasVersion.get().equals(mostRecentSecretFromDB.getResultOrEmpty().getVersion())));

    doReturn(mostRecentSecretFromDB.asMono())
        .when(repository)
        .findFirstByNamespaceAndEntityAndSecretPath(namespace, entity, secretPath);

    var verifier = StepVerifier.create(assertDoesNotThrow(() ->
        secretVersionService.validateCurrentVersionAndGenValidNewVersion(
            namespace, entity, secretPath, requestedCasVersion
        )
    ));

    if (expectedResult.isError()) {
      verifier.expectErrorMatches(ex -> {
        var expectedEx = expectedResult.getError();
        if (!ex.getClass().equals(expectedEx.getClass())) {
          return false;
        }
        if (ex instanceof RetryableException retryableEx) {
          var innerEx = retryableEx.getRetriesExhaustedFallbackError();
          return !Objects.isNull(innerEx) && innerEx.getClass().equals(
              ((RetryableException) expectedEx).getRetriesExhaustedFallbackError().getClass());
        }
        return true;
      })
      .verify();

    } else {
      verifier
          .expectNextMatches(currentAndNewVersion ->
              Objects.equals(expectedResult.getResultOrEmpty(), currentAndNewVersion.getLeft())
          )
          .expectComplete()
          .verify();
    }

    if (expectedResult.isError() && isCasRequest) {
      if (requestedCasVersion.isPresent() && !mostRecentSecretFromDB.hasResult() && !mostRecentSecretFromDB.isError()) {
        verify(telemetryComponents).setSpanAttribute(
                any(ContextView.class), eq(SECRET_CAS_ERROR_PROVIDED_VERSION_KEY),
                eq(requestedCasVersion.get().toString()));
        verify(customMetricsRegistry).recordSecretCreateCasError(namespace);
      } else if (requestedCasVersion.isPresent() && mostRecentSecretFromDB.getResultOrEmpty() != null && !requestedCasVersion.get().equals(mostRecentSecretFromDB.getResultOrEmpty().getVersion())) {
        verify(telemetryComponents).setSpanAttribute(
                any(ContextView.class), eq(SECRET_CAS_ERROR_PROVIDED_VERSION_KEY),
                eq(requestedCasVersion.get().toString()));
        verify(telemetryComponents).setSpanAttribute(
                any(ContextView.class), eq(SECRET_CAS_ERROR_ACTUAL_VERSION_KEY),
                eq(mostRecentSecretFromDB.getResultOrEmpty().getVersion().toString()));
        verify(customMetricsRegistry).recordSecretCreateCasError(namespace);
      }
    }

    if (!expectCasErrorTelemetry) {
      verifyNoInteractions(telemetryComponents);
    } else {
      verifyNoMoreInteractions(telemetryComponents);
    }

    verify(repository, times(1))
        .findFirstByNamespaceAndEntityAndSecretPath(namespace, entity, secretPath);
  }

  private static Stream<Arguments> argsTestCreateSecretVersion() {

    // TestCase(
    //     repositoryWriteResult[ErrorOrBoolean],
    //     isLWTWrite[Boolean],
    //     prevVersionIfLWTWrite[timeuuid],
    //     isCASRequest[Boolean],
    //     expectedResult[ErrorOrBoolean],
    // )
    return Stream.of(false, true).flatMap(isLWTWrite ->
        Stream.of(false, true)
            // If `isLWTWrite` is `false`, then `isCASRequest` has to have been `false`.
            .filter(isCASRequest -> isLWTWrite || !isCASRequest)
            .flatMap(isCASRequest ->
                Stream.of(
                    ResultOrErrorOrEmpty.fromError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test"))),
                    ResultOrErrorOrEmpty.fromResultOrEmpty(false),
                    ResultOrErrorOrEmpty.fromResultOrEmpty(true)
                )
                .map(repositoryWriteResult ->
                    Arguments.of(
                        repositoryWriteResult,
                        isLWTWrite,
                        isLWTWrite ? Uuids.timeBased() : null,
                        isCASRequest,
                        repositoryWriteResult.isError()
                            // Errors from the write should result in a RetryableException(status_code = 500)
                            ? ResultOrErrorOrEmpty.fromError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")))
                            : (Boolean.FALSE.equals(repositoryWriteResult.getResultOrEmpty())
                                  // Unsuccessful writes without errors (empty writes) during CAS requests
                                  // should result in an ConflictException(status_code = 409). In non-CAS requests
                                  // (irrespective of whether the write was an LWT write), a
                                  // RetryableException(status_code = 500) is expected as the request can be retried.
                                  ? ResultOrErrorOrEmpty.fromError(
                                        isCASRequest
                                            ? new ConflictException("conflict")
                                            : new RetryableException(
                                                  isLWTWrite
                                                      // For secret-version writes that use an LWT, a failure causes a
                                                      // RetryableException that wraps a TOO_MANY_REQUESTS(429) error to
                                                      // be used when all available outer-loop-retries are exhausted.
                                                      ? new RetriesExhaustedTooManyRequestsException(TEST_PROBLEM_SUMMARY, "test")
                                                      // For secret-version writes that don't use an LWT, a failure causes a
                                                      // RetryableException that wraps an INTERNAL_SERVER_ERROR(500) to be
                                                      // used when all available outer-loop-retries are exhausted.
                                                      : new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")
                                              )
                                    )
                                  // Successful writes should yield a `Mono.just(true)`.
                                  : ResultOrErrorOrEmpty.fromResultOrEmpty(true))
                    )
                )
            )
    );
  }

  @ParameterizedTest
  @MethodSource("argsTestCreateSecretVersion")
  void testCreateSecretVersion(ResultOrErrorOrEmpty<Boolean> repositoryWriteResult, boolean isLWTWrite,
      UUID prevVersionIfLWTWrite, boolean isCASRequest, ResultOrErrorOrEmpty<Boolean> expectedResult) {

    boolean expectTelemetryInteraction =
        !repositoryWriteResult.isError() && Boolean.FALSE.equals(repositoryWriteResult.getResultOrEmpty());

    var newSecretVersion = Uuids.timeBased();

    var secretVersionModel = SecretVersionModel.builder()
          .namespace(TEST_NAMESPACE)
          .entity(TEST_ENTITY)
          .secretPath(TEST_SECRET_PATH)
          .version(newSecretVersion)
          .currentVersion(newSecretVersion)
          .createdAt(Instant.now())
          .encryptedAt(Instant.now())
          .encryptedByKid(TEST_NEK_ID)
          .value("TEST")
          .build();
    
    if (isLWTWrite) {
      doReturn(repositoryWriteResult.asMono())
          .when(repository)
          .saveNewVersionWithLWT(secretVersionModel, prevVersionIfLWTWrite);
    } else {
      doReturn(repositoryWriteResult.asMono())
          .when(repository)
          .saveNewVersionWithoutLWT(secretVersionModel);
    }

    var verifier = StepVerifier.create(
        secretVersionService.createSecretVersion(secretVersionModel, isLWTWrite, prevVersionIfLWTWrite, isCASRequest)
    );

    if (expectedResult.isError()) {

      verifier.expectErrorMatches(ex -> {
        var expectedEx = expectedResult.getError();
        if (!ex.getClass().equals(expectedEx.getClass())) {
          return false;
        }
        if (ex instanceof RetryableException retryableEx) {
          var innerEx = retryableEx.getRetriesExhaustedFallbackError();
          return !Objects.isNull(innerEx) && innerEx.getClass().equals(
              ((RetryableException) expectedEx).getRetriesExhaustedFallbackError().getClass());
        }
        return true;
      })
      .verify();

    } else {
      verifier
          .expectNextMatches(res -> Objects.equals(res, expectedResult.getResultOrEmpty()))
          .expectComplete()
          .verify();
    }

    if (isLWTWrite) {
      verify(repository, times(1)).saveNewVersionWithLWT(secretVersionModel, prevVersionIfLWTWrite);
      verify(repository, never()).saveNewVersionWithoutLWT(any());
    } else {
      verify(repository, times(1)).saveNewVersionWithoutLWT(secretVersionModel);
      verify(repository, never()).saveNewVersionWithLWT(any(), any());
    }

    if (expectTelemetryInteraction) {
      if (isCASRequest) {

        verify(customMetricsRegistry, times(1))
            .recordNonRetryableLwtFailure(TEST_NAMESPACE, LwtOperation.SECRET_CREATION);
        verify(customMetricsRegistry).recordSecretCreateCasError(TEST_NAMESPACE);
        verify(telemetryComponents).setSpanAttribute(
                any(ContextView.class), eq(SECRET_CAS_ERROR_PROVIDED_VERSION_KEY),
                eq(prevVersionIfLWTWrite.toString()));

      } else {

        if (isLWTWrite) {
          verify(customMetricsRegistry, times(1))
              .recordRetryableLwtFailure(TEST_NAMESPACE, LwtOperation.SECRET_CREATION);

        } else {
          verify(customMetricsRegistry, times(1))
            .recordRetryablePartialSecretCreationOnVersion(TEST_NAMESPACE);
        }
      }
    }

    if (expectTelemetryInteraction) {

      verify(telemetryComponents, times(1)).setSpanAttribute(
              any(ContextView.class), eq(PARTIAL_CREATE_TYPE_KEY),
              eq(PartialCreateType.SECRET_VERSION_AFTER_PATH_BATCH.name()));
      verify(telemetryComponents, times(1)).setSpanAttribute(
              any(ContextView.class), eq(LWT_WRITE_FAILURE_OPERATION_KEY),
              eq(LwtOperation.SECRET_CREATION.name()));
      verifyNoMoreInteractions(telemetryComponents);

    } else {
      verifyNoInteractions(telemetryComponents);
    }
  }

  @Test
  void testDeleteSecretVersions() {
    when(repository.deleteByNamespaceAndEntityAndSecretPath(TEST_NAMESPACE, TEST_ENTITY, TEST_SECRET_PATH))
            .thenReturn(Mono.empty());

    StepVerifier.create(secretVersionService.deleteSecretVersions(TEST_NAMESPACE, TEST_ENTITY,
            TEST_SECRET_PATH))
            .expectNext(true)
            .verifyComplete();
  }


  @Test
  void testGetSecretVersions() {
    when(repository.findAllByNamespaceAndEntityAndSecretPath(TEST_NAMESPACE, TEST_ENTITY, TEST_SECRET_PATH))
            .thenReturn(Flux.just(TEST_SECRET));

    StepVerifier.create(secretVersionService.getSecretVersions(TEST_NAMESPACE, TEST_ENTITY,
                    TEST_SECRET_PATH))
            .expectNext(TEST_SECRET)
            .verifyComplete();
  }
}
