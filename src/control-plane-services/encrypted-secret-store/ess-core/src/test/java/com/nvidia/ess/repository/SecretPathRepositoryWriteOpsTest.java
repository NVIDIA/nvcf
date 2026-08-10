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
package com.nvidia.ess.repository;

import static com.nvidia.ess.util.RepositoryWriteOpsTestUtils.mockBatchStatementExecution;
import static com.nvidia.ess.util.RepositoryWriteOpsTestUtils.verifyReactiveWriteExecution;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY;
import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_PATH;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_PATH_PARENT;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_PATH_ROOT;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyIterable;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.persistence.models.SecretPathPartitionModel;
import com.nvidia.ess.persistence.repositories.SecretPathRepositoryCustomWriteOpsImpl;
import com.nvidia.ess.util.CustomObjectMatchers;
import com.nvidia.ess.util.RepositoryWriteOpsTestUtils.StatementExecResult;
import com.nvidia.ess.utils.namedtuples.SecretPathWriteArgs;
import java.util.Arrays;
import java.util.List;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.data.cassandra.core.ReactiveCassandraBatchOperations;
import org.springframework.data.cassandra.core.ReactiveCassandraTemplate;
import org.springframework.data.cassandra.core.cql.WriteOptions;
import org.springframework.data.cassandra.core.query.Criteria;
import org.springframework.data.cassandra.core.query.Filter;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
public class SecretPathRepositoryWriteOpsTest {

  @Mock
  private ReactiveCassandraBatchOperations batchOps;

  @Mock
  private ReactiveCassandraTemplate reactiveCassandraTemplate;

  @InjectMocks
  private SecretPathRepositoryCustomWriteOpsImpl repositoryWriteOps;

  @Test
  void testInsertSecretPaths_noSecretPaths_noop() {

    var res = assertDoesNotThrow(() ->
        repositoryWriteOps.batchWriteSecretPathsWithEntityVersionLWT(TEST_NAMESPACE, TEST_ENTITY, SecretPathWriteArgs.builder()
            .prevEntityVersionForCAS(Uuids.timeBased())
            .newEntityVersion(Uuids.timeBased())
            .pathsToWrite(List.of())
            .build()));

    StepVerifier.create(res).expectNext(true).expectComplete().verify();

    verifyNoInteractions(batchOps);
  }

  private static Stream<Arguments> argsTestInsertAtLeastOneSecretPath() {
    return Stream.of(false, true).flatMap(
      oldEntityVersionNotNull -> Arrays.asList(StatementExecResult.values()).stream().map(result ->
          Arguments.of(oldEntityVersionNotNull, result))
    );
  }

  @ParameterizedTest
  @MethodSource("argsTestInsertAtLeastOneSecretPath")
  void testInsertSecretPaths_atLeastOneSecretPath_batchExec_returnResultOrEchoError(boolean oldEntityVersionNonNull,
      StatementExecResult batchStatementExecResult) {

    var newEntityVersion = Uuids.timeBased();
    var insertionArgs = SecretPathWriteArgs.builder()
        .prevEntityVersionForCAS(oldEntityVersionNonNull ? Uuids.timeBased() : null)
        .newEntityVersion(newEntityVersion)
        .pathsToWrite(List.of(
            SecretPathModel.builder()
                .namespace(TEST_NAMESPACE)
                .entity(TEST_ENTITY)
                .path(TEST_SECRET_PATH_ROOT)
                .entityVersion(newEntityVersion)
                .updatedAt(Uuids.timeBased())
                .isDir(true)
                .build(),
            SecretPathModel.builder()
                .namespace(TEST_NAMESPACE)
                .entity(TEST_ENTITY)
                .path(TEST_SECRET_PATH_PARENT)
                .entityVersion(newEntityVersion)
                .updatedAt(Uuids.timeBased())
                .isDir(true)
                .build()
        ))
        .build();

    doReturn(batchOps).when(reactiveCassandraTemplate).batchOps();
    doReturn(batchOps).when(batchOps).insert(anyIterable());
    doReturn(batchOps).when(batchOps).update(anyIterable(), any(WriteOptions.class));
    mockBatchStatementExecution(batchOps, batchStatementExecResult);

    var res = assertDoesNotThrow(() ->
        repositoryWriteOps.batchWriteSecretPathsWithEntityVersionLWT(TEST_NAMESPACE, TEST_ENTITY, insertionArgs));

    verifyReactiveWriteExecution(res, batchStatementExecResult, RetryableException.class);

    var expectedUpdateLWTCondition = Filter.from(Criteria.where(SecretPathModel.COLUMN_ENTITY_VERSION)
        .is(insertionArgs.getPrevEntityVersionForCAS()));
    
    var expectedUpdate = SecretPathPartitionModel.builder()
        .namespace(TEST_NAMESPACE)
        .entity(TEST_ENTITY)
        .entityVersion(newEntityVersion)
        .build();

    verify(batchOps, times(1)).update(
        eq(List.of(expectedUpdate)),
        argThat(CustomObjectMatchers.updateOptionsIfConditionMatches(expectedUpdateLWTCondition))
    );
    verify(batchOps, times(1)).insert(eq(insertionArgs.getPathsToWrite()));
    verify(batchOps, times(1)).execute();
  }

  private static Stream<Arguments> argsTestDeleteSecretPath() {
    // Same args.
    return argsTestInsertAtLeastOneSecretPath();
  }

  @ParameterizedTest
  @MethodSource("argsTestDeleteSecretPath")
  void testDeleteSecretPath_batchExec_returnResultOrEchoError(boolean oldEntityVersionNonNull,
      StatementExecResult batchStatementExecResult) {

    var oldEntityVersion = oldEntityVersionNonNull ? Uuids.timeBased() : null;
    var newEntityVersion = Uuids.timeBased();

    doReturn(batchOps).when(reactiveCassandraTemplate).batchOps();
    doReturn(batchOps).when(batchOps).delete(anyIterable());
    doReturn(batchOps).when(batchOps).update(anyIterable(), any(WriteOptions.class));
    mockBatchStatementExecution(batchOps, batchStatementExecResult);

    var res = assertDoesNotThrow(() ->
        repositoryWriteOps.deletePathsByVersion(TEST_NAMESPACE, TEST_ENTITY, List.of(TEST_SECRET_PATH),
            oldEntityVersion, newEntityVersion));
    
    verifyReactiveWriteExecution(res, batchStatementExecResult, RuntimeException.class);

    var expectedUpdateLWTCondition = Filter.from(Criteria.where(SecretPathModel.COLUMN_ENTITY_VERSION)
        .is(oldEntityVersion));
    
    var expectedUpdate = SecretPathPartitionModel.builder()
        .namespace(TEST_NAMESPACE)
        .entity(TEST_ENTITY)
        .entityVersion(newEntityVersion)
        .build();

    verify(batchOps, times(1)).update(
        eq(List.of(expectedUpdate)),
        argThat(CustomObjectMatchers.updateOptionsIfConditionMatches(expectedUpdateLWTCondition))
    );
    verify(batchOps, times(1)).delete(
        argThat(CustomObjectMatchers.primaryKeyMatchesAnySecretPathModel(TEST_NAMESPACE, TEST_ENTITY, TEST_SECRET_PATH))
    );
    verify(batchOps, times(1)).execute();
  }
}
