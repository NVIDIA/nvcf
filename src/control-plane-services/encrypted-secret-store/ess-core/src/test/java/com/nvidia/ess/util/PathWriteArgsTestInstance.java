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
package com.nvidia.ess.util;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.Mockito.doReturn;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.utils.namedtuples.SecretPathWriteArgs;
import java.util.List;
import java.util.Objects;
import java.util.UUID;
import java.util.function.BiFunction;
import java.util.stream.Collectors;
import lombok.Builder;
import lombok.Builder.Default;
import lombok.Getter;
import lombok.ToString;
import org.mockito.Mockito;
import reactor.core.publisher.Flux;

@Builder(toBuilder = true)
@Getter
@ToString
public class PathWriteArgsTestInstance {

  @Builder(toBuilder = true)
  @Getter
  @ToString
  public static class Path {
    private final String path;

    private final Boolean isDir;

    private final UUID prevEntityVersion;

    public SecretPathModel toModel(String namespace, String entity, UUID entityVersion) {
      // Sometimes, entity-partition in DB is empty but in a scenario where all previously existing secret-paths
      // in an entity were deleted, the fetch-all-paths-in-entity operation returns a single row with
      // all non-static, non-partition-key columns set to NULL. This row should be skipped and the
      // end-result should be the same as where the fetch returns no rows at all.
      //
      // This is represented in this test-case as null-valued `path`.
      if (Objects.isNull(path)) {

        // Use `Mockito.mock(...)` to create a `SecretPathModel` instance with a `null`
        // path-value so that the @NonNull validation on the path-field (which curiously enough
        // doesn't work when `SecretPathModel` gets deserialized from repository-output) doesn't
        // prevent instantiating such a `SecretPathModel` in tests. 
        var rowToIgnoreInEmptyPartition = Mockito.mock(SecretPathModel.class);
        doReturn(namespace).when(rowToIgnoreInEmptyPartition).getNamespace();
        doReturn(entity).when(rowToIgnoreInEmptyPartition).getEntity();
        doReturn(entityVersion).when(rowToIgnoreInEmptyPartition).getEntityVersion();
        doReturn(null).when(rowToIgnoreInEmptyPartition).getPath();
        doReturn(null).when(rowToIgnoreInEmptyPartition).getUpdatedAt();
        doReturn(isDir).when(rowToIgnoreInEmptyPartition).getIsDir();

        return rowToIgnoreInEmptyPartition;
      }

      // Otherwise, construct the `SecretPathModel` equivalent of this `Path` testcase in the
      // usual way.
      return SecretPathModel.builder()
          .namespace(namespace)
          .entity(entity)
          .path(path)
          .entityVersion(entityVersion)
          .updatedAt(entityVersion)
          .isDir(isDir)
          .build();
    }
  }

  @Builder(toBuilder = true)
  @Getter
  @ToString
  public static class ExpectedWriteArgs {
    private final List<Path> expectedPathsToWrite;

    private final UUID expectedPrevEntityVersion;

    public SecretPathWriteArgs toWriteArgs(String namespace, String entity) {
      var newEntityVersion = Uuids.timeBased();

      var pathsToWrite = expectedPathsToWrite.stream()
          .map(path -> path.toModel(namespace, entity, newEntityVersion))
          .toList();
      
      return SecretPathWriteArgs.builder()
          .prevEntityVersionForCAS(expectedPrevEntityVersion)
          .newEntityVersion(newEntityVersion)
          .pathsToWrite(pathsToWrite)
          .build();
    }

    public void assertMatch(String namespace, String entity, SecretPathWriteArgs actual, boolean isDeletion) {

      // Verify that `SecretPathWriteArgs.prevEntityVersionForCAS` has the expected value.
      assertEquals(
          getExpectedPrevEntityVersion(),
          actual.getPrevEntityVersionForCAS()
      );
      // Verify that `SecretPathWriteArgs.newEntityVersion` has a valid non-null timeuuid.
      assertNotNull(actual.getNewEntityVersion());

      var expectedPathsToWrite = getExpectedPathsToWrite()
          .stream()
          .collect(Collectors.toMap(
              Path::getPath,
              Path::getIsDir
          ));

      // Check that we have the expected number of path-prefixes determined as
      // requiring insertion or deletion.
      assertNotNull(actual.getPathsToWrite());
      assertEquals(
          getExpectedPathsToWrite().size(),
          actual.getPathsToWrite().size()
      );
      
      // Verify that each path-prefix row (`SecretPathModel`) has its attributes
      // set to the expected values.
      actual.getPathsToWrite().stream().forEach(actualPathToWrite -> {

        assertEquals(namespace, actualPathToWrite.getNamespace());
        assertEquals(entity, actualPathToWrite.getEntity());
        if (isDeletion) {
          // Verify that the `entity_version` in each row to be deleted is the
          // same as `SecretPathWriteArgs.prevEntityVersionForCAS`
          assertEquals(
            actual.getPrevEntityVersionForCAS(),
            actualPathToWrite.getEntityVersion()
          );

        } else {
          // Verify that the `entity_version` in each row to be inserted is the
          // same as `SecretPathWriteArgs.newEntityVersion`.
          assertEquals(
              actual.getNewEntityVersion(),
              actualPathToWrite.getEntityVersion()
          );
        }
        var pathToWriteIsDirExpected = expectedPathsToWrite.get(actualPathToWrite.getPath());
        assertNotNull(pathToWriteIsDirExpected);
        assertEquals(pathToWriteIsDirExpected, actualPathToWrite.getIsDir());
      });
    }
  }

  private final String secretPath;

  private final Flux<Path> pathsFetchedFromDB;

  @Default
  private final UUID entityVersion = Uuids.timeBased();
  
  @Default
  private final Exception expectedError = null;

  @Default
  private final ExpectedWriteArgs expectedResult = null;

  public static BiFunction<String, Boolean, Path> newPathFactory(UUID entityVersion) {
    return (String path, Boolean isDir) -> Path.builder()
        .prevEntityVersion(entityVersion)
        .path(path)
        .isDir(isDir)
        .build();
  }

  public static ExpectedWriteArgs expectedPathWriteArgs(List<Path> expectedPathsToWrite,
      UUID expectedPrevEntityVersion) {
    return ExpectedWriteArgs.builder()
        .expectedPathsToWrite(expectedPathsToWrite)
        .expectedPrevEntityVersion(expectedPrevEntityVersion)
        .build();
  }

  public SecretPathWriteArgs toWriteArgs(String namespace, String entity) {
    if (expectedResult == null) {
      return null;
    }
    return expectedResult.toWriteArgs(namespace, entity);
  }

  public static void assertMatch(String namespace, String entity, PathWriteArgsTestInstance expected,
      SecretPathWriteArgs actual, boolean isDeletion) {

    assertNotNull(expected.getExpectedResult());
    expected.getExpectedResult().assertMatch(namespace, entity, actual, isDeletion);
  }
}
