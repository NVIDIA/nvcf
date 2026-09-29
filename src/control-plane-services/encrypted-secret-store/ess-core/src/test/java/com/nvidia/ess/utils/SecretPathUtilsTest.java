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
package com.nvidia.ess.utils;

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
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.ess.exceptions.AnomalyException;
import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.util.PathWriteArgsTestInstance;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import reactor.core.publisher.Flux;
import reactor.test.StepVerifier;

public class SecretPathUtilsTest {

  @Test
  void getAllSecretPathPrefixes_pathsWithAndWithoutLeadingSlash() {

    assertEquals(Map.of("/", false), assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes("/")));
    assertEquals(Map.of("hello", false), assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes("hello")));
    assertEquals(Map.of("hello", false), assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes("hello/")));
    assertEquals(Map.of("/hello", false), assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes("/hello/")));
    assertEquals(Map.of("hello", true, "hello/world", false), assertDoesNotThrow(
        () -> SecretPathUtils.getAllSecretPathPrefixes("hello/world")));
    assertEquals(Map.of("/hello", true, "/hello/world", false), assertDoesNotThrow(
        () -> SecretPathUtils.getAllSecretPathPrefixes("/hello/world")));
  }

  @Test
  void getAllSecretPathPrefixes_pathsWithWhitespaceAndConsecutiveSlashes() {

    assertEquals(Map.of(), assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes("")));
    assertEquals(Map.of(), assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes("   ")));
    assertEquals(Map.of("/", false), assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes(" /  ")));
    assertEquals(Map.of("/", false), assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes(" /////  ")));
    assertEquals(Map.of("/  ", false), assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes(" /  /")));
    assertEquals(Map.of("/  ", false), assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes(" ///  // ")));
    assertEquals(Map.of("/  ", true, "/  /...", false), assertDoesNotThrow(() ->
        SecretPathUtils.getAllSecretPathPrefixes(" ///  //... ")));
    assertEquals(Map.of("hello", true, "hello/ world ", true, "hello/ world /...", false), assertDoesNotThrow(() ->
        SecretPathUtils.getAllSecretPathPrefixes("hello/// world //... ")));
    assertEquals(Map.of("/ hello", true, "/ hello/ world ", true, "/ hello/ world /... ", false),
        assertDoesNotThrow(() -> SecretPathUtils.getAllSecretPathPrefixes(" // hello/// world //... // ")));
  }

  @Test
  void testIsPathPrefixOf() {

    assertTrue(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/", "/")));
    assertTrue(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/", "/hello")));
    assertTrue(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/", "/hello/world/")));
    assertTrue(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("hello", "hello/world")));
    assertTrue(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("hello", "hello/world/")));
    assertTrue(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("hello/", "hello/world")));
    assertTrue(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("foo/bar", "foo/bar/baz")));
    assertTrue(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/foo/bar/", "/foo/bar/baz")));

    assertFalse(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/", "   ")));
    assertFalse(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("   ", "/")));
    assertFalse(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/", "hello")));
    assertFalse(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/", "hello/")));
    assertFalse(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/hello", "hello/world")));
    assertFalse(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/hello", "/helloworld")));
    assertFalse(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/hello/world", "/helloworld")));
    assertFalse(assertDoesNotThrow(() -> SecretPathUtils.isPathPrefixOf("/helloworld", "/hello/world")));
  }

  private static final String UNEXPECTED_SECRET_PATH_ROW = "dir1/dir3";

  private static Stream<Arguments> pathInsertionArgsTestCases() {
    var nonNullOldEntityVersion = Uuids.timeBased();

    var newPath = newPathFactory(nonNullOldEntityVersion);

    return Stream.of(
      Arguments.of(
          PathWriteArgsTestInstance.builder()
              // Secret-path to be inserted.
              .secretPath(TEST_SECRET_PATH)
              // Existing-path-prefixes-fetch from DB threw an exception.
              .pathsFetchedFromDB(Flux.error(new RetryableException(
                  new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "error during read"))))
              // Expect `validateAndGetInsertionArgs()` to echo the DB-fetch error.
              .expectedError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "error during read")))
              .build()
      ),
      Arguments.of(
          PathWriteArgsTestInstance.builder()
              // Secret-path to be inserted.
              .secretPath(TEST_SECRET_PATH)
              // Existing-path-prefixes-fetch returned rows from DB.
              .pathsFetchedFromDB(Flux.fromIterable(List.of(
                  newPath.apply(TEST_SECRET_PATH_ROOT, true),
                  newPath.apply(UNEXPECTED_SECRET_PATH_ROW, true),  // Unexpected row in DB-fetch output (anomaly).
                  newPath.apply(TEST_SECRET_PATH_PARENT, true),
                  newPath.apply(TEST_SECRET_PATH, false)
              )))
              // Expect `validateAndGetInsertionArgs()` to throw an `AnomalyException`.
              .expectedError(new AnomalyException("test"))
              .build()
      ),
      Arguments.of(
          PathWriteArgsTestInstance.builder()
              // Secret-path to be inserted.
              .secretPath(TEST_SECRET_PATH)
              // Existing-path-prefixes-fetch returned rows from DB.
              .pathsFetchedFromDB(Flux.fromIterable(List.of(
                newPath.apply(TEST_SECRET_PATH_ROOT, true),
                newPath.apply(TEST_SECRET_PATH_PARENT, true),
                newPath.apply(TEST_SECRET_PATH, true)  // This path is a directory in the DB (conflict).
              )))
              // Expect `validateAndGetInsertionArgs()` to throw an `ConflictException` (HTTP 409 conflict).
              .expectedError(new ConflictException("conflict"))
              .build()
      ),
      Arguments.of(
          PathWriteArgsTestInstance.builder()
              // Secret-path to be inserted.
              .secretPath(TEST_SECRET_PATH)
              // Existing-path-prefixes-fetch returned rows from DB.
              .pathsFetchedFromDB(Flux.fromIterable(List.of(
                newPath.apply(TEST_SECRET_PATH_ROOT, true),
                newPath.apply(TEST_SECRET_PATH_PARENT, false) // This ancestor-directory-path is a secret in the DB (conflict).
              )))
              // Expect `validateAndGetInsertionArgs()` to throw an `ConflictException` (HTTP 409 conflict).
              .expectedError(new ConflictException("conflict"))
              .build()
      ),
      Arguments.of(
          PathWriteArgsTestInstance.builder()
              // Secret-path to be inserted.
              .secretPath(TEST_SECRET_PATH)
              // Existing-path-prefixes-fetch returned no rows from DB.
              .pathsFetchedFromDB(Flux.empty())
              .expectedResult(
                  expectedPathWriteArgs(
                      // Expected list of paths to insert.
                      List.of(
                          newPath.apply(TEST_SECRET_PATH_ROOT, true),
                          newPath.apply(TEST_SECRET_PATH_PARENT, true),
                          newPath.apply(TEST_SECRET_PATH, false)
                      ),
                      // Expected `entity_version` value to be used in LWT-condition for insertion.
                      // (A `null` value implies that a separate [earlier] fetch of `entity_version`
                      //  would be required, separate from any path-fetches).
                      null
                  )
              )
              .build()
      ),
      Arguments.of(
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
                      nonNullOldEntityVersion
                  )
              )
              .build()
      ),
      Arguments.of(
          PathWriteArgsTestInstance.builder()
              // Secret-path to be inserted.
              .secretPath(TEST_SECRET_PATH)
              // Existing-path-prefixes-fetch returned all path-prefixes from DB.
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
                      nonNullOldEntityVersion
                  )
              )
              .build()
      )
    );
  }

  @ParameterizedTest
  @MethodSource("pathInsertionArgsTestCases")
  void validateAndGetInsertionArgs_multipleTestCases(PathWriteArgsTestInstance testCase) {

    var verifier = StepVerifier.create(assertDoesNotThrow(() -> SecretPathUtils.validateAndGetInsertionArgs(
        TEST_NAMESPACE,
        TEST_ENTITY,
        testCase.getPathsFetchedFromDB()
            .map(fetchedPath -> fetchedPath.toModel(TEST_NAMESPACE, TEST_ENTITY, fetchedPath.getPrevEntityVersion())),
        SecretPathUtils.getAllSecretPathPrefixes(testCase.getSecretPath())
    )));

    if (!Objects.isNull(testCase.getExpectedResult())) {

      assertNull(testCase.getExpectedError());
      verifier.expectNextMatches(actualResult -> {
        PathWriteArgsTestInstance.assertMatch(TEST_NAMESPACE, TEST_ENTITY, testCase, actualResult, false);
        return true;
      })
      .expectComplete()
      .verify();

    } else {

      // Verify that the returned error matches the expected error-type.
      assertNotNull(testCase.getExpectedError());

      verifier.expectErrorMatches(ex -> {
        var expectedEx = testCase.getExpectedError();
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

    }
    
  }

  private static final int NUM_RANDOMIZED_INPUT_TESTCASES = 10;

  private static Stream<Arguments> argsTestGetSecretPathAndEmptyDirectoriesToDelete() {

    var prevEntityVersion = Uuids.timeBased();

    var newPath = newPathFactory(prevEntityVersion);

    var randomize = new ArrayList<Boolean>();
    randomize.add(false);
    for (int i = 0; i < NUM_RANDOMIZED_INPUT_TESTCASES; ++i) {
      randomize.add(true);
    }

    List<Arguments> testCases =  randomize.stream().flatMap(r -> {
        return Stream.of(
            Arguments.of(
              PathWriteArgsTestInstance.builder()
                  .secretPath(DELETION_TEST_SECRET_PATH)
                  .pathsFetchedFromDB(Flux.fromStream(formSecretAndAncestorPathsForSecrets(
                      // Entity has a lot of secret-paths in it, but the secret-path-to-be-deleted
                      // is itself absent.
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
                      r
                  )))
                  // Expect SecretPathUtil.getSecretPathAndEmptyDirectoriesToDelete(...) to return a
                  // Mono.empty() [nothing to be deleted, path-wise].
                  .expectedResult(null)
                  .build()
            ),
            Arguments.of(
              PathWriteArgsTestInstance.builder()
                  .secretPath(DELETION_TEST_SECRET_PATH)
                  .pathsFetchedFromDB(Flux.fromStream(formSecretAndAncestorPathsForSecrets(
                      // Entity has the secret-path-to-be-deleted and sibling secret-paths (secret-paths
                      // under the same parent-directory) to its lexicographical "left-hand side",
                      // besides other paths in it. 
                      List.of(
                          DELETION_TEST_SECRET_PATH_SECOND_COUSINS_LEFT,
                          DELETION_TEST_SECRET_PATH_FIRST_COUSINS_LEFT,
                          DELETION_TEST_SECRET_PATH_SIBLINGS_LEFT,
                          List.of(DELETION_TEST_SECRET_PATH)
                      ),
                      prevEntityVersion,
                      r
                  )))
                  .expectedResult(
                      // Because the secret-path-to-be-deleted has sibling paths in the
                      // same entity, its deletion cannot result in deletion of any ancestor
                      // directory-paths including its immediate parent-directory-path.
                      expectedPathWriteArgs(
                          List.of(newPath.apply(DELETION_TEST_SECRET_PATH, false)),
                          prevEntityVersion
                      )
                  )
                  .build()
            ),
            Arguments.of(
              PathWriteArgsTestInstance.builder()
                  .secretPath(DELETION_TEST_SECRET_PATH)
                  .pathsFetchedFromDB(Flux.fromStream(formSecretAndAncestorPathsForSecrets(
                      // Entity has the secret-path-to-be-deleted and sibling secret-paths (secret-paths
                      // under the same parent-directory) to its lexicographical "right-hand side",
                      // besides other paths in it. 
                      List.of(
                          List.of(DELETION_TEST_SECRET_PATH),
                          DELETION_TEST_SECRET_PATH_SIBLINGS_RIGHT,
                          DELETION_TEST_SECRET_PATH_FIRST_COUSINS_RIGHT,
                          DELETION_TEST_SECRET_PATH_SECOND_COUSINS_RIGHT
                      ),
                      prevEntityVersion,
                      r
                  )))
                  .expectedResult(
                      // Because the secret-path-to-be-deleted has sibling paths in the
                      // same entity, its deletion cannot result in deletion of any ancestor
                      // directory-paths including its immediate parent-directory-path.
                      expectedPathWriteArgs(
                          List.of(newPath.apply(DELETION_TEST_SECRET_PATH, false)),
                          prevEntityVersion
                      )
                  )
                  .build()
            ),
            Arguments.of(
              PathWriteArgsTestInstance.builder()
                  .secretPath(DELETION_TEST_SECRET_PATH)
                  .pathsFetchedFromDB(Flux.fromStream(formSecretAndAncestorPathsForSecrets(
                      // Entity has the secret-path-to-be-deleted and no sibling secret-paths (secret-paths
                      // under the same parent-directory), but it has secret-paths that share
                      // the same parent's-parent-directory (a.k.a. "first-cousin" secret-paths).
                      List.of(
                          DELETION_TEST_SECRET_PATH_SECOND_COUSINS_LEFT,
                          List.of(DELETION_TEST_SECRET_PATH),
                          DELETION_TEST_SECRET_PATH_FIRST_COUSINS_RIGHT,
                          DELETION_TEST_SECRET_PATH_SECOND_COUSINS_RIGHT
                      ),
                      prevEntityVersion,
                      r
                  )))
                  .expectedResult(
                      expectedPathWriteArgs(
                          // Since the secret-path-to-be-deleted has no sibling secret-paths,
                          // but has secret-paths descending from its parent's-parent-directory-path,
                          // the deletion of the secret-path renders its parent-directory-path empty
                          // and therefore that directory-path can be cleaned up.
                          List.of(
                              newPath.apply(DELETION_TEST_SECRET_PATH, false),
                              newPath.apply(DELETION_TEST_SECRET_PATH_PARENT, true)
                          ),
                          prevEntityVersion
                      )
                  )
                  .build()
            ),
            Arguments.of(
              PathWriteArgsTestInstance.builder()
                  .secretPath(DELETION_TEST_SECRET_PATH)
                  .pathsFetchedFromDB(Flux.fromStream(formSecretAndAncestorPathsForSecrets(
                      // Entity has the secret-path-to-be-deleted and no sibling secret-paths or "first-cousin"
                      // secret-paths (paths with the same grandparent), but does have "second-cousin"
                      // secret-paths (paths with the same great-grandparent), besides other paths
                      // with a different root-directory-path.
                      List.of(
                          DELETION_TEST_SECRET_PATH_SECOND_COUSINS_LEFT,
                          List.of(DELETION_TEST_SECRET_PATH),
                          DELETION_TEST_SECRET_PATH_SECOND_COUSINS_RIGHT,
                          DELETION_TEST_SECRET_PATHS_WITH_DIFF_ROOT
                      ),
                      prevEntityVersion,
                      r
                  )))
                  .expectedResult(
                      expectedPathWriteArgs(
                          // Since the secret-path-to-be-deleted has no sibling secret-paths
                          // or "first-cousin" secret-paths, but does have "second-cousin" secret
                          // paths, the deletion of the secret-path renders its parent directory-path
                          // and grandparent directory-path empty and therefore those paths can be
                          // cleaned up.
                          List.of(
                              newPath.apply(DELETION_TEST_SECRET_PATH, false),
                              newPath.apply(DELETION_TEST_SECRET_PATH_PARENT, true),
                              newPath.apply(DELETION_TEST_SECRET_PATH_GP, true)
                          ),
                          prevEntityVersion
                      )
                  )
                  .build()
            ),
            Arguments.of(
              PathWriteArgsTestInstance.builder()
                  .secretPath(DELETION_TEST_SECRET_PATH)
                  .pathsFetchedFromDB(Flux.fromStream(formSecretAndAncestorPathsForSecrets(
                      // Entity has the secret-path-to-be-deleted and all other secret-paths in the
                      // entity have an entirely different root.
                      List.of(
                          List.of(DELETION_TEST_SECRET_PATH),
                          DELETION_TEST_SECRET_PATHS_WITH_DIFF_ROOT
                      ),
                      prevEntityVersion,
                      r
                  )))
                  .expectedResult(
                      expectedPathWriteArgs(
                          // Upon deletion of the secret-path-to-be-deleted, all its ancestor
                          // directory-paths become empty directory-paths (all other secret-paths
                          // have a different root) and therefore, the empty-ancestor-directory-paths
                          // should be cleaned up along with the secret-path-to-be-deleted.
                          List.of(
                              newPath.apply(DELETION_TEST_SECRET_PATH, false),
                              newPath.apply(DELETION_TEST_SECRET_PATH_PARENT, true),
                              newPath.apply(DELETION_TEST_SECRET_PATH_GP, true),
                              newPath.apply(DELETION_TEST_SECRET_PATH_GGP, true),
                              newPath.apply(DELETION_TEST_SECRET_PATH_ROOT, true)
                          ),
                          prevEntityVersion
                      )
                  )
                  .build()
            )
        );
    })
    .collect(
        Collectors.toCollection(() -> new ArrayList<>())
    );

    final var JUST_THE_SECRET_PATH = List.of(List.of(DELETION_TEST_SECRET_PATH));

    testCases.addAll(List.of(
        Arguments.of(
          PathWriteArgsTestInstance.builder()
              .secretPath(DELETION_TEST_SECRET_PATH)
              // Error while fetching all paths in entity from DB.
              .pathsFetchedFromDB(Flux.error(new RetryableException(
                  new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "error during DB fetch"))))
              // Forward the error downstream.
              .expectedError(new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY,
                  "error during DB fetch")))
              .build()
        ),
        Arguments.of(
            PathWriteArgsTestInstance.builder()
                .secretPath(DELETION_TEST_SECRET_PATH)
                // Empty result from DB for the query to fetch all paths in the entity.
                .pathsFetchedFromDB(Flux.empty())
                // Return Mono.empty() as the secret-path wasn't found in the DB.
                .expectedResult(null)
                .build()
        ),
        Arguments.of(
            PathWriteArgsTestInstance.builder()
                .secretPath(DELETION_TEST_SECRET_PATH)
                // Entity-partition in DB is empty after deletion of all paths that previously existed within that
                // partition but a fetch-paths-in-entity operation pulls a single "row" with non-partition-key,
                // non-static columns set to null. This row must be skipped from processing and the result must
                // be the same as if the fetch returned nothing.
                .pathsFetchedFromDB(Flux.fromIterable(List.of(newPathFactory(prevEntityVersion).apply(null, null))))
                // Return Mono.empty() as the secret-path wasn't found in the DB.
                .expectedResult(null)
                .build()
        ),
        Arguments.of(
            PathWriteArgsTestInstance.builder()
                .secretPath(DELETION_TEST_SECRET_PATH)
                // The only secret-path persisted in the DB for the entity is the
                // secret-path-to-be-deleted.
                .pathsFetchedFromDB(Flux.fromStream(formSecretAndAncestorPathsForSecrets(
                    JUST_THE_SECRET_PATH,
                    prevEntityVersion,
                    false
                )))
                .expectedResult(
                    // Upon deletion of the secret-path-to-be-deleted, all its ancestor directory-paths
                    // become empty directory-paths and therefore, they should be cleaned up along with
                    // the secret-path-to-be-deleted.
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
                )
                .build()
        )
    ));

    return testCases.stream();
  }

  @ParameterizedTest
  @MethodSource("argsTestGetSecretPathAndEmptyDirectoriesToDelete")
  void testGetSecretPathAndEmptyDirectoriesToDelete(PathWriteArgsTestInstance deletionTestCase) {

    var verifier = StepVerifier.create(assertDoesNotThrow(() -> SecretPathUtils.getSecretPathAndEmptyDirectoriesToDelete(
        deletionTestCase.getPathsFetchedFromDB()
            .map(fetchedPath -> fetchedPath.toModel(TEST_NAMESPACE, TEST_ENTITY, fetchedPath.getPrevEntityVersion())),
        DELETION_TEST_SECRET_PATH
    )));

    if (!Objects.isNull(deletionTestCase.getExpectedResult())) {

      assertNull(deletionTestCase.getExpectedError());
      verifier.expectNextMatches(actualResult -> {
        PathWriteArgsTestInstance.assertMatch(TEST_NAMESPACE, TEST_ENTITY, deletionTestCase, actualResult, true);
        return true;
      })
      .expectComplete()
      .verify();

    } else if (Objects.isNull(deletionTestCase.getExpectedError())) {

      // We expect empty output.
      verifier.expectNextCount(0).expectComplete().verify();

    } else {

      // Verify that the returned error matches the expected error-type.
      verifier.expectErrorMatches(ex -> {
        var expectedEx = deletionTestCase.getExpectedError();
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

    }

  }
}
