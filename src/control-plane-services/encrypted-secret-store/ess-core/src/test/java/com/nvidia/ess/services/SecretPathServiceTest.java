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
import static com.nvidia.ess.util.PathDeletionTestUtils.formSecretAndAncestorPathsForSecrets;
import static com.nvidia.ess.util.PathWriteArgsTestInstance.expectedPathWriteArgs;
import static com.nvidia.ess.util.PathWriteArgsTestInstance.newPathFactory;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATHS_WITH_DIFF_ROOT;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH_FIRST_COUSINS_LEFT;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH_FIRST_COUSINS_RIGHT;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH_GGP;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH_GP;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH_PARENT;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH_ROOT;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH_SECOND_COUSINS_LEFT;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH_SECOND_COUSINS_RIGHT;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH_SIBLINGS_LEFT;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH_SIBLINGS_RIGHT;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY;
import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;
import static com.nvidia.ess.util.TestConstants.TEST_PROBLEM_SUMMARY;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_PATH;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_PATH_PARENT;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_PATH_ROOT;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.mockStatic;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetriesExhaustedTooManyRequestsException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.metrics.CustomMetricsRegistry.LwtOperation;
import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.persistence.models.SecretPathPartitionModel;
import com.nvidia.ess.persistence.repositories.SecretPathPartitionRepository;
import com.nvidia.ess.persistence.repositories.SecretPathRepository;
import com.nvidia.ess.persistence.services.SecretPathService;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.util.CustomObjectMatchers;
import com.nvidia.ess.util.PathWriteArgsTestInstance;
import com.nvidia.ess.util.ResultOrErrorOrEmpty;
import com.nvidia.ess.utils.SecretPathUtils;
import com.nvidia.ess.utils.namedtuples.SecretPathWriteArgs;
import jakarta.annotation.Nullable;
import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.function.Function;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.NonNull;
import org.apache.commons.lang3.tuple.Pair;
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
public class SecretPathServiceTest {
  
  @Mock
  private SecretPathRepository repository;

  @Mock
  private SecretPathPartitionRepository partitionRepository;

  @Mock
  private CustomMetricsRegistry customMetricsRegistry;

  @Mock
  private TelemetryComponents telemetryComponents;

  @InjectMocks
  private SecretPathService secretPathService;

  private static Stream<Arguments> argsTestWriteAllPathsForSecret() {

    /*
     *
     * Operations performed within SecretPathService.writeAllPathsForSecret(...), with possible outcomes:
     * [X = does not execute]
     *  
     * prefetch-version:
     *     partitionRepository.findFirstByNamespaceAndEntity(namespace, entity) -> error, empty, non-empty.
     * 
     * fetch-existing-path-prefixes:
     *     repository.findAllByNamespaceAndEntityAndPathIn(namespace, entity, any()) -> X, error, empty, non-empty
     * 
     * form-missing-path-prefixes:
     *     SecretPathUtils.validateAndGetInsertionArgs(namespace, entity, any(), any()) -> X, error, non-empty.
     * 
     * insert-missing-path-prefixes:
     *     repository.batchWriteSecretPathsWithEntityVersionLWT(namespace, entity, args) -> X, error,
     *                                                                                      empty-write-LWT-failure(false),
     *                                                                                      successful-write(true).
     * 
     */


    // Test-cases: [X = should not execute].
    //
    // 6-tuple: [
    //
    //     Arg1: Namespace,
    //     Arg2: Entity,
    //     Arg3: secretPath,
    //
    //     Arg4: ResultOrErrorOrEmpty(prefetch-version)
    //
    //     Arg5: NullIfNotExecuted(
    //               PathInsertionArgsTestCase(
    //                   secretPath,
    //                   FluxResultOrError(paths-fetched-from-db),
    //                   ErrorIfExpected(form-missing-path-prefixes),
    //                   ResultIfExpected(form-missing-path-prefixes)
    //               )
    //           ),
    //
    //     Arg6: NullIfNotExecuted(
    //               ResultOrError(insert-missing-path-prefixes)
    //           )
    // ]
    //

    var nonNullEntityVersionFromPartitionFetch = Uuids.timeBased();
    var nonNullEntityVersionFromPathFetch = Uuids.timeBased();

    // Convenience function to construct a `Path` in a test-case.
    var newPath = newPathFactory(nonNullEntityVersionFromPathFetch);

    // If `fetch-existing-path-prefixes` returned empty output, then `expectedPrevEntityVersion` (for LWT) could not
    // have been inferred from `SecretPathModel` rows (there were none fetched). Therefore, if a value of `entity_version`
    // was fetched from a successful `prefetch-version` operation, that would have to be the value used for the
    // LWT-guarded-write accompanying the `insert-missing-path-prefixes` operation (if it succeeds).
    //
    Function<PathWriteArgsTestInstance, PathWriteArgsTestInstance> fallbackToPrefetchedEntityVersionIfNeeded = 
        (PathWriteArgsTestInstance testCase) -> {
          // Call this when `prefetch-version` has non-empty output.
          var expectedResult = testCase.getExpectedResult();
          if (expectedResult == null) {
            // Either `fetch-existing-path-prefixes` failed with an error or `form-missing-path-prefixes`
            // failed with an error.
            return testCase;
          }
          var currentlySetExpectedEntityVersion = expectedResult.getExpectedPrevEntityVersion();
          if (currentlySetExpectedEntityVersion == null) {
            // `fetch-existing-path-prefixes` returned empty output. Apply the `entity_version` value obtained
            // from the successful non-empty `prefetch-version` operation to `expectedPrevEntityVersion`.
            var newExpectedResult = expectedResult.toBuilder()
                .expectedPrevEntityVersion(nonNullEntityVersionFromPartitionFetch)
                .build();
            var newTestCase = testCase.toBuilder()
                .expectedResult(newExpectedResult)
                .build();
            return newTestCase;
          }
          // `fetch-existing-path-prefixes` returned non-empty output and the value of `entity_version` obtained
          // from that fetch will be used in the LWT-guarded write accompanying `insert-missing-path-prefixes`
          // (if it succeeds).
          return testCase;
        };
    
    var allTestCases = Stream.of(
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = error,
       *  form-missing-path-prefixes = X, insert-missing-path-prefixes = X]
       */
      Pair.of(
          // form-missing-path-prefixes skipped due to error from fetch-existing-path-prefixes
          PathWriteArgsTestInstance.builder()
              // Secret-path to be inserted.
              .secretPath(TEST_SECRET_PATH)
              // fetch-existing-path-prefixes from DB threw an exception.
              .pathsFetchedFromDB(Flux.error(
                  new RuntimeException("error during fetch of existing path-prefixes.")))
              // Expect `validateAndGetInsertionArgs()` to echo the DB-fetch error.
              .expectedError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")))
              .build(),
          // insert-missing-path-prefixes skipped.
          null
      ),
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = empty,
       *  form-missing-path-prefixes = non-empty(all), insert-missing-path-prefixes = error]
       */
      Pair.of(
          // form-missing-path-prefixes ran successfully and returned a result
          PathWriteArgsTestInstance.builder()
              .secretPath(TEST_SECRET_PATH)
              // fetch-existing-path-prefixes returned no rows from DB.
              .pathsFetchedFromDB(Flux.empty())
              .expectedResult(
                  expectedPathWriteArgs(
                      // Expected list of paths to insert.
                      List.of(
                          newPath.apply(TEST_SECRET_PATH_ROOT, true),
                          newPath.apply(TEST_SECRET_PATH_PARENT, true),
                          newPath.apply(TEST_SECRET_PATH, false)
                      ),
                      // Expected `entity_version` value to be used in LWT-condition for insertion
                      // (unless the prefetch-version step returned a non-null entity_version).
                      null
                  )
              )
              .build(),
          // insert-missing-path-prefixes executed and returned an error even though
          // form-missing-path-prefixes executed successfully..
          ResultOrErrorOrEmpty.fromError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")))
      ),
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = empty,
       *  form-missing-path-prefixes = non-empty(all), insert-missing-path-prefixes = empty-write-LWT-failure]
       */
      Pair.of(
          // form-missing-path-prefixes ran successfully and returned a result
          PathWriteArgsTestInstance.builder()
              .secretPath(TEST_SECRET_PATH)
              // fetch-existing-path-prefixes returned no rows from DB.
              .pathsFetchedFromDB(Flux.empty())
              .expectedResult(
                  expectedPathWriteArgs(
                      // Expected list of paths to insert.
                      List.of(
                          newPath.apply(TEST_SECRET_PATH_ROOT, true),
                          newPath.apply(TEST_SECRET_PATH_PARENT, true),
                          newPath.apply(TEST_SECRET_PATH, false)
                      ),
                      // Expected `entity_version` value to be used in LWT-condition for insertion
                      // (unless the prefetch-version step returned a non-null entity_version).
                      null
                  )
              )
              .build(),
          // insert-missing-path-prefixes executed and nothing was written due to an LWT failure.
          ResultOrErrorOrEmpty.fromResultOrEmpty(false)
      ),
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = empty,
       *  form-missing-path-prefixes = non-empty(all), insert-missing-path-prefixes = successful-write]
       */
      Pair.of(
          // form-missing-path-prefixes ran successfully and returned a result
          PathWriteArgsTestInstance.builder()
              .secretPath(TEST_SECRET_PATH)
              // fetch-existing-path-prefixes returned no rows from DB.
              .pathsFetchedFromDB(Flux.empty())
              .expectedResult(
                  expectedPathWriteArgs(
                      // Expected list of paths to insert.
                      List.of(
                          newPath.apply(TEST_SECRET_PATH_ROOT, true),
                          newPath.apply(TEST_SECRET_PATH_PARENT, true),
                          newPath.apply(TEST_SECRET_PATH, false)
                      ),
                      // Expected `entity_version` value to be used in LWT-condition for insertion
                      // (unless the prefetch-version step returned a non-null entity_version).
                      null
                  )
              )
              .build(),
          // insert-missing-path-prefixes executed successfully and returned a result
          // (inserted secret-paths described in the previous tuple-argument).
          ResultOrErrorOrEmpty.fromResultOrEmpty(true)
      ),
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = non-empty(conflict),
       *  form-missing-path-prefixes = error(conflict), insert-missing-path-prefixes = X]
       */
      Pair.of(
          // form-missing-path-prefixes failed due to a conflict found between the secret-path to
          // be inserted and the paths returned from fetch-existing-path-prefixes.
          PathWriteArgsTestInstance.builder()
              // Secret-path to be inserted.
              .secretPath(TEST_SECRET_PATH)
              // fetch-existing-path-prefixes returned rows from DB.
              .pathsFetchedFromDB(Flux.fromIterable(List.of(
                newPath.apply(TEST_SECRET_PATH_ROOT, true),
                newPath.apply(TEST_SECRET_PATH_PARENT, true),
                newPath.apply(TEST_SECRET_PATH, true)  // This path is a directory in the DB (conflict).
              )))
              // Expect `validateAndGetInsertionArgs()` to throw an `ConflictException` (HTTP 409 conflict).
              .expectedError(new ConflictException("conflict"))
              .build(),
          // insert-missing-path-prefixes was skipped due to form-missing-path-prefixes returning with
          // an error (conflict).
          null
      ),
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = non-empty(some),
       *  form-missing-path-prefixes = non-empty(some), insert-missing-path-prefixes = error]
       */
      Pair.of(
          PathWriteArgsTestInstance.builder()
              // Secret-path to be inserted.
              .secretPath(TEST_SECRET_PATH)
              // Existing-path-prefixes-fetch returned one row from DB [first ancestral directory of secret-path].
              .pathsFetchedFromDB(Flux.fromIterable(List.of(newPath.apply(TEST_SECRET_PATH_ROOT, true))))
              .expectedResult(
                  expectedPathWriteArgs(
                      // Expected list of paths to insert.
                      List.of(
                          newPath.apply(TEST_SECRET_PATH_PARENT, true),
                          newPath.apply(TEST_SECRET_PATH, false)
                      ),
                      // Expected `entity_version` value to be used in LWT-condition for insertion.
                      nonNullEntityVersionFromPathFetch
                  )
              )
              .build(),
          // insert-missing-path-prefixes executed and returned an error even though
          // form-missing-path-prefixes executed successfully.
          ResultOrErrorOrEmpty.fromError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")))
      ),
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = non-empty(some),
       *  form-missing-path-prefixes = non-empty(some), insert-missing-path-prefixes = empty-write-LWT-failure]
       */
      Pair.of(
          PathWriteArgsTestInstance.builder()
              // Secret-path to be inserted.
              .secretPath(TEST_SECRET_PATH)
              // Existing-path-prefixes-fetch returned one row from DB [first ancestral directory of secret-path].
              .pathsFetchedFromDB(Flux.fromIterable(List.of(newPath.apply(TEST_SECRET_PATH_ROOT, true))))
              .expectedResult(
                  expectedPathWriteArgs(
                      // Expected list of paths to insert.
                      List.of(
                          newPath.apply(TEST_SECRET_PATH_PARENT, true),
                          newPath.apply(TEST_SECRET_PATH, false)
                      ),
                      // Expected `entity_version` value to be used in LWT-condition for insertion.
                      nonNullEntityVersionFromPathFetch
                  )
              )
              .build(),
          // insert-missing-path-prefixes executed but nothing was written due to an LWT failure.
          ResultOrErrorOrEmpty.fromResultOrEmpty(false)
      ),
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = non-empty(all),
       *  form-missing-path-prefixes = non-empty(none), insert-missing-path-prefixes = error]
       */
      Pair.of(
          PathWriteArgsTestInstance.builder()
          // Secret-path to be inserted.
          .secretPath(TEST_SECRET_PATH)
          // fetch-existing-path-prefixes returned all path-prefixes from DB.
          .pathsFetchedFromDB(Flux.fromIterable(List.of(
              newPath.apply(TEST_SECRET_PATH_ROOT, true),
              newPath.apply(TEST_SECRET_PATH_PARENT, true),
              newPath.apply(TEST_SECRET_PATH, false)
          )))
          .expectedResult(
              expectedPathWriteArgs(
                  // Expected list of paths to insert (empty).
                  List.of(),
                  // Expected `entity_version` value to be used in LWT-condition for insertion.
                  nonNullEntityVersionFromPathFetch
              )
          )
          .build(),
          // insert-missing-path-prefixes failed.
          ResultOrErrorOrEmpty.fromError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")))
      ),
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = non-empty(all),
       *  form-missing-path-prefixes = non-empty(none), insert-missing-path-prefixes = empty-write-LWT-failure]
       */
      Pair.of(
          PathWriteArgsTestInstance.builder()
          // Secret-path to be inserted.
          .secretPath(TEST_SECRET_PATH)
          // fetch-existing-path-prefixes returned all path-prefixes from DB.
          .pathsFetchedFromDB(Flux.fromIterable(List.of(
              newPath.apply(TEST_SECRET_PATH_ROOT, true),
              newPath.apply(TEST_SECRET_PATH_PARENT, true),
              newPath.apply(TEST_SECRET_PATH, false)
          )))
          .expectedResult(
              expectedPathWriteArgs(
                  // Expected list of paths to insert (empty).
                  List.of(),
                  // Expected `entity_version` value to be used in LWT-condition for insertion.
                  nonNullEntityVersionFromPathFetch
              )
          )
          .build(),
          // insert-missing-path-prefixes executed but nothing was written due to an LWT failure.
          ResultOrErrorOrEmpty.fromResultOrEmpty(false)
      ),
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = non-empty(some),
       *  form-missing-path-prefixes = non-empty(some), insert-missing-path-prefixes = successful-write]
       */
      Pair.of(
          PathWriteArgsTestInstance.builder()
          // Secret-path to be inserted.
          .secretPath(TEST_SECRET_PATH)
          // fetch-existing-path-prefixes returned one row from DB [first ancestral directory of secret-path].
          .pathsFetchedFromDB(Flux.fromIterable(List.of(newPath.apply(TEST_SECRET_PATH_ROOT, true))))
          .expectedResult(
              expectedPathWriteArgs(
                  // Expected list of paths to insert.
                  List.of(
                      newPath.apply(TEST_SECRET_PATH_PARENT, true),
                      newPath.apply(TEST_SECRET_PATH, false)
                  ),
                  // Expected `entity_version` value to be used in LWT-condition for insertion.
                  nonNullEntityVersionFromPathFetch
              )
          )
          .build(),
          // insert-missing-path-prefixes executed successfully.
          ResultOrErrorOrEmpty.fromResultOrEmpty(true)
      ),
      /*
       * [prefetch-version = <empty-or-fetched>, fetch-existing-path-prefixes = non-empty(all),
       *  form-missing-path-prefixes = non-empty(none), insert-missing-path-prefixes = non-empty]
       */
      Pair.of(
        PathWriteArgsTestInstance.builder()
        // Secret-path to be inserted.
        .secretPath(TEST_SECRET_PATH)
        // fetch-existing-path-prefixes returned all path-prefixes from DB.
        .pathsFetchedFromDB(Flux.fromIterable(List.of(
            newPath.apply(TEST_SECRET_PATH_ROOT, true),
            newPath.apply(TEST_SECRET_PATH_PARENT, true),
            newPath.apply(TEST_SECRET_PATH, false)
        )))
        .expectedResult(
            expectedPathWriteArgs(
                // Expected list of paths to insert (empty).
                List.of(),
                // Expected `entity_version` value to be used in LWT-condition for insertion.
                nonNullEntityVersionFromPathFetch
            )
        )
        .build(),
        // insert-missing-path-prefixes was executed but there were no paths to be inserted (noop).
        // Noop is regarded as a success and so, `Mono.just(true)` is expected to be returned from
        // the underlying repository call in this scenario.
        ResultOrErrorOrEmpty.fromResultOrEmpty(true)
      )

    ).flatMap(args -> Stream.of(

          // prefetch-version = empty
          Arguments.of(
              TEST_NAMESPACE,
              TEST_ENTITY,
              TEST_SECRET_PATH,
              ResultOrErrorOrEmpty.fromResultOrEmpty(null),
              args.getLeft(),
              args.getRight()
          ),
          // prefetch-version = fetched
          Arguments.of(
              TEST_NAMESPACE,
              TEST_ENTITY,
              TEST_SECRET_PATH,
              ResultOrErrorOrEmpty.fromResultOrEmpty(
                  SecretPathPartitionModel.builder()
                      .namespace(TEST_NAMESPACE)
                      .entity(TEST_ENTITY)
                      .entityVersion(nonNullEntityVersionFromPartitionFetch)
                      .build()
              ),
              // If the value of `expectedPrevEntityVersion` is `null` in this
              // `PathInsertionArgsTestCase` (because the output of `fetch-existing-path-prefixes`
              // is empty), replace it with the value from `prefetch-version` (it exists here).
              //
              // The value from `prefetch-version` is the entity_version value expected to
              // be used for the `insert-missing-path-prefixes` write if `fetch-existing-path-prefixes`
              // returned empty output.
              fallbackToPrefetchedEntityVersionIfNeeded.apply(args.getLeft()),
              args.getRight()
          )
      )
    )
    .collect(Collectors.toCollection(() -> new ArrayList<>()));

    /*
     * [prefetch-version = error, fetch-existing-path-prefixes = X,
     *  form-missing-path-prefixes = X, insert-missing-path-prefixes = X]
     */
    allTestCases.add(
        Arguments.of(
            TEST_NAMESPACE,
            TEST_ENTITY,
            TEST_SECRET_PATH,
            ResultOrErrorOrEmpty.fromError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test"))),
            null,
            null
        )
    );

    return allTestCases.stream();
  }

  private ResultOrErrorOrEmpty<SecretPathWriteArgs> mockRepositoryCalls(String namespace, String entity,
      String secretPath,
      ResultOrErrorOrEmpty<SecretPathPartitionModel> prefetchVersionResult,
      @Nullable PathWriteArgsTestInstance fetchExistingAndFormMissingPathPrefixesResult,
      @Nullable ResultOrErrorOrEmpty<Boolean> insertMissingPathPrefixesSuccess) {

    doReturn(prefetchVersionResult.asMono())
        .when(partitionRepository)
        .findFirstByNamespaceAndEntity(eq(namespace), eq(entity));

    if (prefetchVersionResult.isError()) {

      // prefetch-version returns an error. Nothing else should execute.

      // Expected result: Mono.error(new RetryableException(new RetriesExhaustedInternalErrorException()))
      return ResultOrErrorOrEmpty.fromError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")));
    }
  
    // Since prefetch-version did not return an error, fetch-existing-path-prefixes
    // must execute (followed by form-missing-path-prefixes if fetch-existing-path-prefixes
    // did not return an error).
    assertNotNull(fetchExistingAndFormMissingPathPrefixesResult);

    // Mock result of fetch-existing-path-prefixes (error / empty / non-empty)
    doReturn(fetchExistingAndFormMissingPathPrefixesResult.getPathsFetchedFromDB()
                .map(path -> path.toModel(namespace, entity, path.getPrevEntityVersion())))
        .when(repository)
        .findAllByNamespaceAndEntityAndPathIn(eq(namespace), eq(entity), any());

    if (!Objects.isNull(fetchExistingAndFormMissingPathPrefixesResult.getExpectedError())) {
      // Either fetch-existing-path-prefixes or form-missing-path-prefixes returned an
      // error. `insert-missing-path-prefixes` should be skipped.

      // Expected result: Mono.error(new RetryableException(new RetriesExhaustedInternalErrorException()))
      return ResultOrErrorOrEmpty.fromError(
          fetchExistingAndFormMissingPathPrefixesResult.getExpectedError());
    }

    // fetch-existing-path-prefixes and form-missing-path-prefixes finished execution
    // and returned non-error results. Therefore, `insert-missing-path-prefixes` must
    // attempt execution (irrespective of the result).
    assertNotNull(insertMissingPathPrefixesSuccess);

    // insert-missing-path-prefixes must have been called with an equivalent `SecretPathWriteArgs` instance
    // (except for the value of `newEntityVersion` and the `entityVersion` values within each inserted
    // `SecretPathModel` -> these are generated timeuuid values).
    //
    var expectedInsertionArgs = fetchExistingAndFormMissingPathPrefixesResult.toWriteArgs(namespace, entity);

    if (insertMissingPathPrefixesSuccess.isError()) {
      // insert-missing-path-prefixes returned an error.
      doReturn(Mono.error(new RetryableException(new RetriesExhaustedInternalErrorException(
              TEST_PROBLEM_SUMMARY, "error during path-write"))))
          .when(repository)
          .batchWriteSecretPathsWithEntityVersionLWT(
              eq(namespace),
              eq(entity),
              argThat(CustomObjectMatchers.writeArgsMatchExceptTimestamps(expectedInsertionArgs))
          );

      // Expected result: Mono.error(new RetryableException(new RetriesExhaustedInternalErrorException()))
      return ResultOrErrorOrEmpty.fromError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")));
    }

    // Sanity-check of test-cases.
    assertNotNull(insertMissingPathPrefixesSuccess.getResultOrEmpty());

    // insert-missing-path-prefixes either succeeded, or executed but wrote nothing due to an LWT failure.
    doReturn(insertMissingPathPrefixesSuccess.asMono())
        .when(repository)
        .batchWriteSecretPathsWithEntityVersionLWT(
            eq(namespace),
            eq(entity),
            argThat(CustomObjectMatchers.writeArgsMatchExceptTimestamps(expectedInsertionArgs))
        );

    return insertMissingPathPrefixesSuccess.getResultOrEmpty()
        // Successful execution of insert-missing-path-prefixes: Expected result Mono.just(expectedInsertionArgs)
        ? ResultOrErrorOrEmpty.fromResultOrEmpty(expectedInsertionArgs)
        // No writes in insert-missing-path-prefixes (LWT failure) despite completing execution.
        // Expected result: Mono.error(new RetryableException(new RetriesExhaustedTooManyRequestsException(...)))
        : ResultOrErrorOrEmpty.fromError(new RetryableException(new RetriesExhaustedTooManyRequestsException(
              TEST_PROBLEM_SUMMARY, "test")));
  }

  private void verifyTestCaseExecution(String namespace, String entity,
      String secretPath,
      ResultOrErrorOrEmpty<SecretPathPartitionModel> prefetchVersionResult,
      PathWriteArgsTestInstance fetchExistingAndFormMissingPathPrefixesResult,
      ResultOrErrorOrEmpty<Boolean> insertMissingPathPrefixesSuccess) {

    if (insertMissingPathPrefixesSuccess != null
        && Boolean.FALSE.equals(insertMissingPathPrefixesSuccess.getResultOrEmpty())) {
      verify(telemetryComponents, times(1)).setSpanAttribute(
          any(ContextView.class), eq(LWT_WRITE_FAILURE_OPERATION_KEY),
          eq(LwtOperation.PATH_CREATION.name()));
      verifyNoMoreInteractions(telemetryComponents);
    } else {
      verifyNoInteractions(telemetryComponents);
    }

    verify(partitionRepository, times(1)).findFirstByNamespaceAndEntity(eq(namespace), eq(entity));

    if (prefetchVersionResult.isError()) {
      // prefetch-version returns an error. Skip everything else.
      verify(repository, never()).findAllByNamespaceAndEntityAndPathIn(any(), any(), any());
      verify(repository, never()).batchWriteSecretPathsWithEntityVersionLWT(any(), any(), any());
      return;
    }

    assertNotNull(fetchExistingAndFormMissingPathPrefixesResult);

    verify(repository, times(1)).findAllByNamespaceAndEntityAndPathIn(eq(namespace), eq(entity), any());

    if (!Objects.isNull(fetchExistingAndFormMissingPathPrefixesResult.getExpectedError())) {
      // Either fetch-existing-path-prefixes or form-missing-path-prefixes returned an error.
      // `insert-missing-path-prefixes` should be skipped.
      verify(repository, never()).batchWriteSecretPathsWithEntityVersionLWT(any(), any(), any());
      return;
    }

    assertNotNull(insertMissingPathPrefixesSuccess);

    // Verify that `insert-missing-path-prefixes` was executed.
    var expectedInsertionArgs = fetchExistingAndFormMissingPathPrefixesResult.toWriteArgs(namespace, entity);
    verify(repository, times(1)).batchWriteSecretPathsWithEntityVersionLWT(
        eq(namespace),
        eq(entity),
        argThat(CustomObjectMatchers.writeArgsMatchExceptTimestamps(expectedInsertionArgs))
    );
  }

  @ParameterizedTest
  @MethodSource("argsTestWriteAllPathsForSecret")
  void testWriteAllPathsForSecret(String namespace, String entity, String secretPath,
      ResultOrErrorOrEmpty<SecretPathPartitionModel> prefetchVersionResult,
      PathWriteArgsTestInstance fetchExistingAndFormMissingPathPrefixesResult,
      ResultOrErrorOrEmpty<Boolean> insertMissingPathPrefixesSuccess) {

    var expectedTestResult = mockRepositoryCalls(namespace, entity, secretPath, prefetchVersionResult,
        fetchExistingAndFormMissingPathPrefixesResult, insertMissingPathPrefixesSuccess);
    
    var verifier = StepVerifier.create(
        secretPathService.writeAllPathsForSecret(namespace, entity, secretPath)
    );
    if (expectedTestResult.isError()) {
      verifier.expectErrorMatches(ex -> {
        var expectedEx = expectedTestResult.getError();
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
      assertNotNull(fetchExistingAndFormMissingPathPrefixesResult);
      verifier.expectNextMatches(actualArgs -> {
        PathWriteArgsTestInstance.assertMatch(
            namespace, entity, fetchExistingAndFormMissingPathPrefixesResult, actualArgs, false
        );
        return true;
      })
      .expectComplete()
      .verify();
    }
    
    verifyTestCaseExecution(namespace, entity, secretPath, prefetchVersionResult,
        fetchExistingAndFormMissingPathPrefixesResult, insertMissingPathPrefixesSuccess);
  }

  // The 3 operations of path-deletion & empty-directory cleanup:
  //
  // [1] fetch-paths-in-entity: SecretPathRepository call to fetch all secret-paths within the given NS & Entity
  //                            within which the secret-path-to-be-deleted (supposedly) exists.
  //
  // [2] form-all-paths-to-delete: Call to SecretPathUtils.getSecretPathAndEmptyDirectoriesToDelete(...). The
  //                               result is a `SecretPathWriteArgs` instance containing the SecretPathModel-s
  //                               corresponding to the secret-path-to-be-deleted (if it exists in the DB),
  //                               any ancestor-directory-paths in the DB that would be rendered empty if the
  //                               secret-path-to-be-deleted is actually deleted, and the current value of
  //                               `entity_version` (for use in the LWT-guarded delete operation) and a
  //                               freshly generated, new value of `entity_version` to set as part of the
  //                               delete transaction.
  //
  // [3] delete-secret-and-empty-dirs: Deletion operation for the secret-path-to-be-deleted and its
  //                                   ancestor dirs that would be rendered empty upon its deletion,
  //                                   with an LWT on `entity_version`.

  private static Arguments singleDeletionTestCase(
      ResultOrErrorOrEmpty<List<SecretPathModel>> mockPathsInEntity,
      ResultOrErrorOrEmpty<SecretPathWriteArgs> mockPathsToDelete,
      ResultOrErrorOrEmpty<Boolean> mockDeletionResult,
      ResultOrErrorOrEmpty<Boolean> expectedResult) {

    // [1] Mock output of `fetch-paths-in-entity`.
    //
    // Wrap the mock entity-paths repository fetch-result into a Flux<SecretPathModel>.
    var pathsInEntityFetchResultPub = mockPathsInEntity.asMono().flatMapIterable(Function.identity());

    // [2] Mock output of `form-all-paths-to-delete` conditioned on `fetch-paths-in-entity` successfully
    //     completing and returning a non-empty result.
    //
    // Create a Mono<SecretPathWriteArgs> that is downstream of the Flux<SecretPathModel> (and is
    // therefore `empty()` if the former is, or echoes any `error()` from the former), and returns
    // the mock `form-all-paths-to-delete` result if the former is successful.
    var pathsToDeleteResultPub = pathsInEntityFetchResultPub.then(mockPathsToDelete.asMono());

    // [3] Mock output of `delete-secret-and-empty-dirs` conditioned on `form-all-paths-to-delete`
    //     successfully completing and returning a non-empty result.
    //
    // Create a Mono<Boolean> that is downstream of the Mono<SecretPathInsertionArgs>, and returns
    // the mock `delete-secret-and-empty-dirs` success-flag if the former is successful.
    //
    // If [1] or [2] did not execute successfully, [3] will never run (it's called within a
    // `flatMap(...)`) and therefore, [3] must be mocked only if [1] & [2] succeed. To represent
    // the case where [3] shouldn't be mocked, `deletionResultPub` is a `null` in such cases.
    var deletionResultPub = !mockPathsInEntity.hasResult() || !mockPathsToDelete.hasResult()
        ? null
        : pathsToDeleteResultPub.then(mockDeletionResult.asMono());

    // [R] `expectedResult`: Expected return-value (on success) or error from calling
    //     `SecretPathService.deleteSecretPathAndEmptyAncestorDirs()` given [1], [2] & [3] above.
    //
    // Return the trio of publishers to use in mocks of the relevant interfaces as part of this test-case,
    // along with the expected result of calling `SecretPathService.deleteSecretPathAndEmptyAncestorDirs()`.
    return Arguments.of(pathsInEntityFetchResultPub, pathsToDeleteResultPub, deletionResultPub, expectedResult);
  }

  private static Stream<Arguments> argsTestDeleteSecretPathAndEmptyAncestorDirs() {

    var prevEntityVersion = Uuids.timeBased();

    var newPath = newPathFactory(prevEntityVersion);

    var JUST_THE_SECRET_PATH_IN_DB = List.of(DELETION_TEST_SECRET_PATH);

    // Test-cases where [1] `fetch-paths-in-entity` and [2] `form-all-paths-to-delete` successfully
    // returned non-empty results. Test multiple scenarios of [3] `delete-secret-and-empty-dirs`.
    var testCases = Stream.<ResultOrErrorOrEmpty<Boolean>>of(
        // `delete-secret-and-empty-dirs` threw an exception during execution.
        ResultOrErrorOrEmpty.fromError(new RuntimeException()),
        // `delete-secret-and-empty-dirs` did not throw an exception but the transaction failed
        // (e.g. LWT-condition-failure).
        ResultOrErrorOrEmpty.fromResultOrEmpty(false),
        // `delete-secret-and-empty-dirs` executed successfully.
        ResultOrErrorOrEmpty.fromResultOrEmpty(true)
    )
        .map(pathDeletionOutcome ->
            singleDeletionTestCase(
                // [1] Non-empty result from `fetch-paths-in-entity`, containing only the secret-path-to-be-deleted
                //     and its ancestor directory-paths.
                ResultOrErrorOrEmpty.fromResultOrEmpty(
                    formSecretAndAncestorPathsForSecrets(
                        List.of(JUST_THE_SECRET_PATH_IN_DB),
                        prevEntityVersion,
                        false
                    )
                        .map(path -> path.toModel(TEST_NAMESPACE, TEST_ENTITY, prevEntityVersion))
                        .toList()
                ),
                // [2] `form-all-paths-to-delete` should return the secret-path-to-be-deleted and all its
                //     ancestor directory-paths (all of them would become empty if the secret-path is deleted).
                ResultOrErrorOrEmpty.fromResultOrEmpty(
                    expectedPathWriteArgs(
                        List.of(
                            newPath.apply(DELETION_TEST_SECRET_PATH, false),
                            newPath.apply(DELETION_TEST_SECRET_PATH_PARENT, true),
                            newPath.apply(DELETION_TEST_SECRET_PATH_GP, true),
                            newPath.apply(DELETION_TEST_SECRET_PATH_GGP, true),
                            newPath.apply(DELETION_TEST_SECRET_PATH_ROOT, true)
                        ),
                        prevEntityVersion
                    )
                        .toWriteArgs(TEST_NAMESPACE, TEST_ENTITY)
                ),
                // [3] `delete-secret-and-empty-dirs` outcome:
                pathDeletionOutcome,
                // [R] The expected result should echo the outcome of [3]. Errors should be wrapped in a
                //     `RetryableException`.
                pathDeletionOutcome.isError()
                    ? ResultOrErrorOrEmpty.<Boolean>fromError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")))
                    : pathDeletionOutcome
            )
        )
        .collect(Collectors.toCollection(() -> new ArrayList<>()));

    // Test-cases where either [1] `fetch-paths-in-entity` or [2] `form-all-paths-to-delete` threw an error
    // or returned an empty result. Downstream operations ([3] `delete-secret-and-empty-dirs`) should not
    // execute in these test-cases and the end-result should be `RetryableException` if [1] or [2] threw
    // and error, or `true` if [1] or [2] were empty (noop success).
    testCases.addAll(List.of(
        singleDeletionTestCase(
            // [1] Error in `fetch-paths-in-entity`.
            ResultOrErrorOrEmpty.fromError(new RuntimeException()),
            // [2] Result of `form-all-paths-to-delete` should echo error in [1]. This mock is irrelevant.
            ResultOrErrorOrEmpty.fromResultOrEmpty(null),
            // [3] Result of `delete-secret-and-empty-dirs` shouldn't execute as [1] didn't succeed.
            //     This mock is irrelevant.
            ResultOrErrorOrEmpty.fromResultOrEmpty(null),
            // [R] Expect the error from [1] to be wrapped in a `RetryableException` in the final result.
            ResultOrErrorOrEmpty.fromError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")))
        ),
        singleDeletionTestCase(
            // [1] Empty result from `fetch-paths-in-entity`.
            ResultOrErrorOrEmpty.fromResultOrEmpty(null),
            // [2] `form-all-paths-to-delete` should be empty as [1] is empty. This mock is irrelevant.
            ResultOrErrorOrEmpty.fromResultOrEmpty(null),
            // [3] `delete-secret-and-empty-dirs` shouldn't execute as [1] is empty. This mock is irrelevant.
            ResultOrErrorOrEmpty.fromResultOrEmpty(null),
            // [R] This is a noop (nothing to delete). The returned result should be `true`.
            ResultOrErrorOrEmpty.fromResultOrEmpty(true)
        ),
        singleDeletionTestCase(
            // [1] Non-empty result from `fetch-paths-in-entity`. But it doesn't contain the secret-path-to-be-deleted.
            ResultOrErrorOrEmpty.fromResultOrEmpty(
                formSecretAndAncestorPathsForSecrets(
                    List.of(
                        DELETION_TEST_SECRET_PATH_SIBLINGS_LEFT,
                        DELETION_TEST_SECRET_PATH_SIBLINGS_RIGHT,
                        DELETION_TEST_SECRET_PATH_FIRST_COUSINS_LEFT,
                        DELETION_TEST_SECRET_PATH_FIRST_COUSINS_RIGHT,
                        DELETION_TEST_SECRET_PATH_SECOND_COUSINS_LEFT,
                        DELETION_TEST_SECRET_PATH_SECOND_COUSINS_RIGHT,
                        DELETION_TEST_SECRET_PATHS_WITH_DIFF_ROOT
                    ),
                    prevEntityVersion,
                    false
                )
                    .map(path -> path.toModel(TEST_NAMESPACE, TEST_ENTITY, prevEntityVersion))
                    .toList()
            ),
            // [2] `form-all-paths-to-delete` should return an empty path-list as [1] did not return the
            // secret-path-to-be-deleted.
            ResultOrErrorOrEmpty.fromResultOrEmpty(null),
            // [3] `delete-secret-and-empty-dirs` shouldn't execute as [2] is empty. This mock is irrelevant.
            ResultOrErrorOrEmpty.fromResultOrEmpty(null),
            // [R] This is a noop (nothing to delete). The returned result should be `true`.
            ResultOrErrorOrEmpty.fromResultOrEmpty(true)
        )
    ));    

    return testCases.stream();
  }

  @ParameterizedTest
  @MethodSource("argsTestDeleteSecretPathAndEmptyAncestorDirs")
  void testDeleteSecretPathAndEmptyAncestorDirs(@NonNull Flux<SecretPathModel> mockPathsInEntity,
      @NonNull Mono<SecretPathWriteArgs> mockPathsToDelete, @Nullable Mono<Boolean> mockDeletionResult,
      ResultOrErrorOrEmpty<Boolean> expectedResult) {

    try (var mockSecretPathUtils =  mockStatic(SecretPathUtils.class)) {

      doReturn(mockPathsInEntity)
          .when(repository)
          .findAllByNamespaceAndEntity(eq(TEST_NAMESPACE), eq(TEST_ENTITY));

      mockSecretPathUtils.when(() -> SecretPathUtils.getSecretPathAndEmptyDirectoriesToDelete(
          any(), eq(DELETION_TEST_SECRET_PATH)
      ))
          .thenReturn(mockPathsToDelete);

      if (!Objects.isNull(mockDeletionResult)) {
        doReturn(mockDeletionResult)
            .when(repository)
            .deletePathsByVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), any(), any(), any());
      }

      var verifier = StepVerifier.create(secretPathService.deleteSecretPathAndEmptyAncestorDirs(
          TEST_NAMESPACE, TEST_ENTITY, DELETION_TEST_SECRET_PATH
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
      } else if (expectedResult.hasResult()) {
        verifier.expectNext(expectedResult.getResultOrEmpty()).expectComplete().verify();
      } else {
        verifier.expectNextCount(0).expectComplete().verify();
      }

      verifyNoInteractions(telemetryComponents);
    }
  }
}
