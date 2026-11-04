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

import com.nvidia.ess.persistence.models.EntityModel;
import com.nvidia.ess.persistence.models.NamespaceModel;
import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.utils.namedtuples.SecretPathWriteArgs;
import java.util.Objects;
import java.util.function.Predicate;
import java.util.stream.Collectors;
import java.util.stream.StreamSupport;
import org.apache.commons.lang3.tuple.Pair;
import org.mockito.ArgumentMatcher;
import org.springframework.data.cassandra.core.UpdateOptions;
import org.springframework.data.cassandra.core.cql.WriteOptions;
import org.springframework.data.cassandra.core.query.Filter;

public class CustomObjectMatchers {

  private static class CustomObjectMatcher<CustomObject> implements ArgumentMatcher<CustomObject> {

    private final Predicate<CustomObject> expected;

    public CustomObjectMatcher(Predicate<CustomObject> expected) {
      this.expected = expected;
    }

    @Override
    public boolean matches(CustomObject actual) {
      return expected.test(actual);
    }
  }

  public static CustomObjectMatcher<Iterable<SecretPathModel>> primaryKeyMatchesAnySecretPathModel(String namespace,
      String entity, String path) {
    return new CustomObjectMatcher<Iterable<SecretPathModel>>(models -> {
      return StreamSupport.stream(models.spliterator(), false).anyMatch(model ->
          model.getNamespace().equals(namespace) && model.getEntity().equals(entity) && model.getPath().equals(path)
      );
    });
  }

  public static CustomObjectMatcher<EntityModel> entityModelMatchesExceptTimestamps(EntityModel expected) {
    return new CustomObjectMatcher<EntityModel>(actual ->
        Objects.equals(expected.getNamespace(), actual.getNamespace()) &&
        Objects.equals(expected.getEntityType(), actual.getEntityType()) &&
        Objects.equals(expected.getEntityId(), actual.getEntityId()) &&
        Objects.equals(expected.getHashBucket(), actual.getHashBucket())
    );
  }

  public static CustomObjectMatcher<WriteOptions> updateOptionsIfConditionMatches(Filter cond) {
    return new CustomObjectMatcher<WriteOptions>(writeOptions -> {
      if (writeOptions instanceof UpdateOptions updateOptions) {
        var ifCond = updateOptions.getIfCondition();
        if (ifCond != null) {
          var criteriaSet = ifCond.get().map(criterion ->
              Pair.of(criterion.getColumnName(), criterion.getPredicate())
          )
          .collect(Collectors.toSet());

          return cond.filter(criterion ->
                  !criteriaSet.contains(Pair.of(criterion.getColumnName(), criterion.getPredicate()))
              )
              .isEmpty();
        }
      }
      return false;
    });
  }

  public static CustomObjectMatcher<NamespaceModel> nsModelMatchesExceptTimestamps(NamespaceModel expected) {
    return new CustomObjectMatcher<NamespaceModel>(actual -> {
      return Objects.equals(expected.getNamespace(), actual.getNamespace())
          && Objects.equals(expected.getEntityTypes(), actual.getEntityTypes())
          && Objects.equals(expected.getNotaryAuthorizations(), actual.getNotaryAuthorizations())
          && Objects.equals(expected.getOauthAuthorizations(), actual.getOauthAuthorizations())
          && Objects.equals(expected.getEntityHashSize(), actual.getEntityHashSize())
          && Objects.equals(expected.getPreviousEntityHashSize(), actual.getPreviousEntityHashSize())
          && Objects.equals(expected.getRequireLWTForSecretVersionWrites(), actual.getRequireLWTForSecretVersionWrites());
    });
  }

  public static CustomObjectMatcher<SecretPathWriteArgs> writeArgsMatchExceptTimestamps(
      SecretPathWriteArgs expected) {
    return new CustomObjectMatcher<SecretPathWriteArgs>(args -> {

      if (!Objects.equals(args.getPrevEntityVersionForCAS(), expected.getPrevEntityVersionForCAS())) {
        // Both `SecretPathWriteArgs` objects should have the same `prevEntityVersionForCAS`
        // value.
        return false;
      }

      if (args.getPathsToWrite().size() != expected.getPathsToWrite().size()) {
        // Both `SecretPathWriteArgs` objects should have the same number of paths-to-insert.
        return false;
      }

      var actualPathsToInsert = args.getPathsToWrite().stream().collect(
          Collectors.toMap(SecretPathModel::getPath, v -> v)
      );
      var expectedPathsToInsert = expected.getPathsToWrite().stream().collect(
          Collectors.toMap(SecretPathModel::getPath, v -> v)
      );

      if (actualPathsToInsert.size() != expectedPathsToInsert.size() ||
          actualPathsToInsert.size() != args.getPathsToWrite().size()) {
        return false;
      }

      for (var actualPathEntry : actualPathsToInsert.entrySet()) {
        // For each path-to-insert in either `SecretPathWriteArgs` object:
        var actualPath = actualPathEntry.getValue();
        var expectedPath = expectedPathsToInsert.get(actualPathEntry.getKey());
        if (expectedPath == null) {
          return false;
        }

        // Check equality of all attributes except `entityVersion` and `updatedAt`
        // (these are generated timestamps).
        if (!Objects.equals(expectedPath.getNamespace(), actualPath.getNamespace()) ||
                !Objects.equals(expectedPath.getEntity(), actualPath.getEntity()) ||
                !Objects.equals(expectedPath.getPath(), actualPath.getPath()) ||
                !Objects.equals(
                    Boolean.TRUE.equals(expectedPath.getIsDir()),
                    Boolean.TRUE.equals(actualPath.getIsDir())
                )) {
          return false;
        }
      }

      return true;
    });
  }
}

