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
package com.nvidia.nvcf.persistence.function;

import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.deployment.dto.HelmValidationPolicyDto.KubernetesType.builder;
import static com.nvidia.nvcf.rest.function.deployment.dto.ValidationPolicyNameEnum.DEFAULT;
import static com.nvidia.nvcf.rest.function.deployment.dto.ValidationPolicyNameEnum.UNRESTRICTED;
import static com.nvidia.nvcf.util.TestConstants.A10G;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_4;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_5;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_6;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;
import static java.util.concurrent.Future.State.RUNNING;
import static java.util.concurrent.Future.State.SUCCESS;
import static org.assertj.core.api.Assertions.assertThat;

import com.google.common.collect.Sets;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentKey;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.rest.function.deployment.dto.HelmValidationPolicyDto;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.stream.Collectors;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class FunctionsRepositoryTest {
    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private ReactiveFunctionsRepository reactiveFunctionsRepository;

    @Autowired
    private FunctionsDeploymentRepository functionsDeploymentRepository;

    @Autowired
    private DeploymentBatchWriter deploymentBatchWriter;

    @Autowired
    private FunctionDeploymentLookupService functionDeploymentLookupService;

    @Autowired
    private JsonMapper jsonMapper;

    private Set<String> functionLevelAuthzParties = Set.of(TEST_AUTHORIZED_NCA_ID_1,
                                                           TEST_AUTHORIZED_NCA_ID_2);
    private Set<String> versionLevelAuthzParties = Set.of(TEST_AUTHORIZED_NCA_ID_4,
                                                          TEST_AUTHORIZED_NCA_ID_5);

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
    }

    @AfterAll
    void cleanup() {
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @BeforeEach
    void init() {
        functionsRepository.deleteAll();
        functionsDeploymentRepository.deleteAll();
    }

    @AfterEach
    void reset() {
        functionsRepository.deleteAll();
        functionsDeploymentRepository.deleteAll();
    }

    @Test
    void testFunctionRepositories() {
        var entity1 = getTestEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME);
        var entity2 = getTestEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_2, TEST_NCA_ID, TEST_FUNCTION_NAME_2);
        var entity3 = getTestEntity(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_3, TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        functionsRepository.save(entity1);
        functionsRepository.save(entity2);
        functionsRepository.save(entity3);

        var function = functionsRepository.getByFunctionVersionId(TEST_VERSION_ID_1);
        assertThat(function).isNotNull();
        assertThat(function).isPresent();
        assertThat(function.get().getFunctionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(function.get().getFunctionVersionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(function.get().getFunctionName()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(function.get().getNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(function.get().getContainerArgs()).isEqualTo(TEST_CONTAINER_ARGS);
        assertThat(function.get().getContainerImage())
                .isEqualTo(TEST_NGC_CONTAINER_IMAGE.toString());
        assertThat(function.get().getApiBodyFormat()).isEqualTo(CUSTOM);
        assertThat(function.get().getModelSpecs()).hasSize(2);
        assertThat(function.get().getModelSpecs()).containsKeys("model-1", "model-2");
        assertThat(function.get().getDescription()).isEqualTo(TEST_DESCRIPTION);
        assertThat(function.get().getTags()).containsAll(TEST_TAGS);
        assertThat(function.get().getHealth()).isNotNull();
        assertThat(function.get().getFunctionLevelAuthorizedAccounts())
                .containsExactlyInAnyOrderElementsOf(functionLevelAuthzParties);
        assertThat(function.get().getVersionLevelAuthorizedAccounts())
                .containsExactlyInAnyOrderElementsOf(versionLevelAuthzParties);

        var func1 = functionsRepository.getByFunctionVersionId(TEST_VERSION_ID_1);
        assertThat(func1.get()).isNotNull();
        assertThat(func1.get().getFunctionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(func1.get().getFunctionVersionId()).isEqualTo(TEST_VERSION_ID_1);

        var func2 = functionsRepository.getByFunctionVersionId(TEST_VERSION_ID_2);
        assertThat(func2.get()).isNotNull();
        assertThat(func2.get().getFunctionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(func2.get().getFunctionVersionId()).isEqualTo(TEST_VERSION_ID_2);

        var func3 = functionsRepository.getByFunctionVersionId(TEST_VERSION_ID_3);
        assertThat(func3.get()).isNotNull();
        assertThat(func3.get().getFunctionId()).isEqualTo(TEST_FUNCTION_ID_2);
        assertThat(func3.get().getFunctionVersionId()).isEqualTo(TEST_VERSION_ID_3);

        functionsRepository.delete(entity1);

        func1 = functionsRepository.getByFunctionVersionId(TEST_VERSION_ID_1);
        assertThat(func1).isEmpty();
    }

    @Test
    void testFunctionsRepository() {
        var entity1 = getTestEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME);
        var entity2 = getTestEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_2, TEST_NCA_ID, TEST_FUNCTION_NAME_2);
        var entity3 = getTestEntity(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_3, TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        functionsRepository.save(entity1);
        functionsRepository.save(entity2);
        functionsRepository.save(entity3);

        var functions = functionsRepository.findAllByFunctionId(TEST_FUNCTION_ID).toList();
        assertThat(functions).isNotEmpty();
        assertThat(functions).hasSize(2);
        functions.forEach(function -> {
            assertThat(function.getFunctionId()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(function.getFunctionVersionId()).
                    isIn(Set.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2));
        });

        var versionIds = Set.of(
                functions.getFirst().getFunctionVersionId(),
                functions.get(1).getFunctionVersionId());
        assertThat(versionIds).containsExactlyInAnyOrder(TEST_VERSION_ID_1, TEST_VERSION_ID_2);

        var function = functionsRepository.getByFunctionVersionId(TEST_VERSION_ID_3);
        assertThat(function).isNotNull();
        assertThat(function).isPresent();
        assertThat(function.get().getFunctionId()).isEqualTo(TEST_FUNCTION_ID_2);
        assertThat(function.get().getFunctionVersionId()).isEqualTo(TEST_VERSION_ID_3);

        functionsRepository.delete(entity1);

        functions = functionsRepository.findAllByFunctionId(TEST_FUNCTION_ID).toList();
        assertThat(functions).isNotEmpty();
        assertThat(functions).hasSize(1);
        assertThat(functions.getFirst().getFunctionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(functions.getFirst().getFunctionVersionId()).isEqualTo(
                TEST_VERSION_ID_2);
    }

    @Test
    void testAuthorizedParties() {
        var entity1 = getTestEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME);
        var entity2 = getTestEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_2, TEST_NCA_ID, TEST_FUNCTION_NAME_2);
        var entity3 = getTestEntity(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_3, TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        functionsRepository.save(entity1);
        functionsRepository.save(entity2);
        functionsRepository.save(entity3);

        var allFunctions = functionsRepository.findAll();
        assertThat(allFunctions).hasSize(3);
        allFunctions.forEach(function -> {
            assertThat(function.getFunctionId()).isIn(Set.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2));
            assertThat(function.getFunctionVersionId()).isIn(Set.of(TEST_VERSION_ID_1,
                                                                    TEST_VERSION_ID_2,
                                                                    TEST_VERSION_ID_3));
            assertThat(function.getFunctionLevelAuthorizedAccounts())
                    .containsExactlyInAnyOrderElementsOf(functionLevelAuthzParties);
            assertThat(function.getVersionLevelAuthorizedAccounts())
                    .containsExactlyInAnyOrderElementsOf(versionLevelAuthzParties);

        });

        // Update function level authz parties.
        var updatedAzps = Sets.union(functionLevelAuthzParties, Set.of(TEST_AUTHORIZED_NCA_ID_6));
        var functions = functionsRepository.findAllByFunctionId(TEST_FUNCTION_ID).toList();
        assertThat(functions).hasSize(2);
        functions.forEach(function -> {
            // Use the insert() method to to update the entity.
            function.setFunctionLevelAuthorizedAccounts(updatedAzps);
            functionsRepository.insert(function);
        });

        // Fetch from DB again and check whether the field/entity got updated.
        functions = functionsRepository.findAllByFunctionId(TEST_FUNCTION_ID).toList();
        assertThat(functions).hasSize(2);
        functions.forEach(function -> {
            assertThat(function.getFunctionLevelAuthorizedAccounts())
                    .containsExactlyInAnyOrderElementsOf(updatedAzps);
        });


        // Delete function level authz parties.
        functions = functionsRepository.findAllByFunctionId(TEST_FUNCTION_ID).toList();
        assertThat(functions).hasSize(2);
        functions.forEach(function -> {
            // Cannot use insert() method when a null value is involved. So, use the save() method.
            function.setFunctionLevelAuthorizedAccounts(null);
            functionsRepository.save(function);
        });

        // Fetch from DB and check whether the field got cleared.
        functions = functionsRepository.findAllByFunctionId(TEST_FUNCTION_ID).toList();
        assertThat(functions).hasSize(2);
        functions.forEach(function -> {
            assertThat(function.getFunctionLevelAuthorizedAccounts()).isNull();
        });
    }


    @Test
    @SneakyThrows
    void testFunctionsDeploymentRepository() {
        var name1 = TEST_FUNCTION_NAME + "-1";
        var entity1 = getTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, name1);

        functionsRepository.save(entity1);

        UUID id1 = UUID.randomUUID();
        UUID id2 = UUID.randomUUID();
        var gpuSpecEntities = List.of(
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(id1)
                                     .build())
                        .gpu(T10).backend(GFN).maxInstances(4).minInstances(4)
                        .helmValidationPolicy(jsonMapper.writeValueAsString(
                                HelmValidationPolicyDto.builder()
                                        .name(DEFAULT)
                                        .extraKubernetesTypes(
                                                List.of(builder()
                                                                .group("group_1")
                                                                .version("version_1")
                                                                .kind("kind_1")
                                                                .build()))
                                        .build()))
                        .build(),
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(id2)
                                     .build())
                        .gpu(A10G).backend(GFN).maxInstances(5).minInstances(5)
                        .helmValidationPolicy(jsonMapper.writeValueAsString(
                                HelmValidationPolicyDto.builder()
                                        .name(UNRESTRICTED)
                                        .extraKubernetesTypes(
                                                List.of(builder()
                                                                .group("group_2")
                                                                .version("version_2")
                                                                .kind("kind_2")
                                                                .build()))
                                        .build()))
                        .build()
        );
        var deploymentEntity = FunctionDeploymentEntity.builder()
                .key(FunctionDeploymentKey.builder().functionVersionId(TEST_VERSION_ID_1).build())
                .deploymentId(TEST_DEPLOYMENT_ID)
                .functionId(TEST_FUNCTION_ID)
                .ncaId(TEST_NCA_ID)
                .createdAt(Instant.now())
                .lastUpdatedAt(Instant.now())
                .build();
        deploymentBatchWriter.createDeployment(
                new FunctionDeploymentContext(deploymentEntity, gpuSpecEntities));

        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1)
                        .orElseThrow();
        var deployment = deploymentContext.deployment();
        assertThat(deployment).isNotNull();
        assertThat(deployment.getKey().getFunctionVersionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(deployment.getFunctionId()).isEqualTo(TEST_FUNCTION_ID);

        var gpuSpecs = deploymentContext.gpuSpecs();
        assertThat(gpuSpecs).hasSize(2);

        gpuSpecs.forEach(spec -> {
            var gpu = spec.getGpu();
            var backend = spec.getBackend();
            assertThat(gpu).isIn(T10, A10G);
            assertThat(backend).isEqualTo(GFN);
            assertThat(spec.getAvailabilityZones()).isNullOrEmpty();
            assertThat(spec.getInstanceType()).isNull();

            // Update instance-type
            spec.setInstanceType(backend + ":" + gpu);
        });

        deploymentBatchWriter.updateDeployment(new FunctionDeploymentContext(deployment, gpuSpecs));

        deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1)
                .orElseThrow();
        gpuSpecs = deploymentContext.gpuSpecs();
        assertThat(gpuSpecs).isNotNull().isNotEmpty().hasSize(2);
        gpuSpecs.forEach(spec -> {
            var backend = spec.getBackend();
            var gpu = spec.getGpu();
            var instanceType = backend + ":" + gpu;
            assertThat(spec.getInstanceType()).isEqualTo(instanceType);
        });

        deploymentBatchWriter.deleteDeployment(TEST_NCA_ID, TEST_VERSION_ID_1,
                                               TEST_DEPLOYMENT_ID);
        var deploymentOpt = functionsDeploymentRepository
                    .getByKeyFunctionVersionId(TEST_VERSION_ID_1);
        assertThat(deploymentOpt).isEmpty();
    }

    @Test
    public void testFindAsyncByKeyFunctionId() {
        var entity1 = getTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                    TEST_NCA_ID, TEST_FUNCTION_NAME);
        functionsRepository.save(entity1);

        CompletableFuture<List<FunctionEntity>> asyncByKeyFunctionId =
                reactiveFunctionsRepository
                        .asyncFindByKeyFunctionId(TEST_FUNCTION_ID);

        assertThat(asyncByKeyFunctionId.state()).isEqualTo(RUNNING);
        var entity = asyncByKeyFunctionId.join();
        assertThat(asyncByKeyFunctionId.state()).isEqualTo(SUCCESS);
        assertThat(entity.stream().findFirst()).isPresent();
    }

    @Test
    public void testGetAsyncByKeyFunctionIdAndKeyFunctionVersionId() {
        var entity1 = getTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                    TEST_NCA_ID, TEST_FUNCTION_NAME);

        functionsRepository.save(entity1);

        CompletableFuture<Optional<FunctionEntity>> asyncByKeyFunctionId =
                reactiveFunctionsRepository
                        .asyncGetByFunctionIdAndFunctionVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);

        assertThat(asyncByKeyFunctionId.state()).isEqualTo(RUNNING);
        var entity = asyncByKeyFunctionId.join();
        assertThat(asyncByKeyFunctionId.state()).isEqualTo(SUCCESS);
        assertThat(entity).isPresent();
        assertThat(entity.get().getFunctionId()).isEqualTo(TEST_FUNCTION_ID);
    }

    @Test
    void testListIndexFetch() {
        var entity1 = getTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                    TEST_NCA_ID, TEST_FUNCTION_NAME);
        entity1.setFunctionLevelAuthorizedAccounts(
                Set.of(TEST_NCA_ID, TEST_NCA_ID_2, TEST_NCA_ID_3));
        functionsRepository.save(entity1);

        var entity2 = getTestEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                    TEST_NCA_ID, TEST_FUNCTION_NAME);
        entity2.setFunctionLevelAuthorizedAccounts(
                Set.of(TEST_NCA_ID_2, TEST_NCA_ID_3));
        functionsRepository.save(entity2);

        var entity3 = getTestEntity(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                    TEST_NCA_ID, TEST_FUNCTION_NAME);
        entity3.setFunctionLevelAuthorizedAccounts(
                Set.of(TEST_NCA_ID_2,  TEST_NCA_ID));
        functionsRepository.save(entity3);

        var authNcaId = functionsRepository
                .findAllByFunctionLevelAuthorizedAccount(TEST_NCA_ID);
        var authNcaId2 = functionsRepository
                .findAllByFunctionLevelAuthorizedAccount(TEST_NCA_ID_2);
        var authNcaId3 = functionsRepository
                .findAllByFunctionLevelAuthorizedAccount(TEST_NCA_ID_3);

        assertThat(authNcaId).isNotNull();
        assertThat(authNcaId
                           .map(FunctionEntity::getFunctionVersionId)
                           .collect(Collectors.toSet()))
                .containsExactlyInAnyOrder(TEST_VERSION_ID_1, TEST_VERSION_ID_3);

        assertThat(authNcaId2).isNotNull();
        assertThat(authNcaId2
                           .map(FunctionEntity::getFunctionVersionId)
                           .collect(Collectors.toSet()))
                .containsExactlyInAnyOrder(TEST_VERSION_ID_1, TEST_VERSION_ID_2, TEST_VERSION_ID_3);

        assertThat(authNcaId3).isNotNull();
        assertThat(authNcaId3
                           .map(FunctionEntity::getFunctionVersionId)
                           .collect(Collectors.toSet()))
                .containsExactlyInAnyOrder(TEST_VERSION_ID_1, TEST_VERSION_ID_2);
    }

    @Test
    void testListIndexFetchByFunctionId() {
        var entity1 = getTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                    TEST_NCA_ID, TEST_FUNCTION_NAME);
        entity1.setFunctionLevelAuthorizedAccounts(
                Set.of(TEST_NCA_ID, TEST_NCA_ID_2, TEST_NCA_ID_3));
        functionsRepository.save(entity1);

        var entity2 = getTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                    TEST_NCA_ID, TEST_FUNCTION_NAME);
        entity2.setFunctionLevelAuthorizedAccounts(
                Set.of(TEST_NCA_ID_2, TEST_NCA_ID_3));
        functionsRepository.save(entity2);

        var entity3 = getTestEntity(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                    TEST_NCA_ID, TEST_FUNCTION_NAME);
        entity3.setFunctionLevelAuthorizedAccounts(
                Set.of(TEST_NCA_ID_2,  TEST_NCA_ID));
        functionsRepository.save(entity3);

        var authNcaId = functionsRepository
                .findAllByFunctionId(TEST_FUNCTION_ID)
                .filter(function -> function.getFunctionLevelAuthorizedAccounts()
                        .contains(TEST_NCA_ID));
        var authNcaId2 = functionsRepository
                .findAllByFunctionId(TEST_FUNCTION_ID)
                .filter(function -> function.getFunctionLevelAuthorizedAccounts()
                        .contains(TEST_NCA_ID_2));
        var authNcaId3 = functionsRepository
                .findAllByFunctionId(TEST_FUNCTION_ID_3)
                .filter(function -> function.getFunctionLevelAuthorizedAccounts()
                        .contains(TEST_NCA_ID_2));

        assertThat(authNcaId).isNotNull();
        assertThat(authNcaId
                           .map(FunctionEntity::getFunctionVersionId)
                           .collect(Collectors.toSet()))
                .containsExactlyInAnyOrder(TEST_VERSION_ID_1);

        assertThat(authNcaId2).isNotNull();
        assertThat(authNcaId2
                           .map(FunctionEntity::getFunctionVersionId)
                           .collect(Collectors.toSet()))
                .containsExactlyInAnyOrder(TEST_VERSION_ID_1, TEST_VERSION_ID_2);

        assertThat(authNcaId3).isNotNull();
        assertThat(authNcaId3
                           .map(FunctionEntity::getFunctionVersionId)
                           .collect(Collectors.toSet()))
                .containsExactlyInAnyOrder(TEST_VERSION_ID_3);
    }

    private FunctionEntity getTestEntity(
            UUID id,
            UUID versionId,
            String ncaId,
            String name) {
        return FunctionEntity.builder()
                .functionId(id)
                .functionVersionId(versionId)
                .functionName(name)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(ncaId)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE.toString())
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .modelSpecs(Map.of(
                        "model-1", jsonMapper.createObjectNode()
                                .put("version", "1.0")
                                .put("url", TEST_MODEL_URL_1)
                                .toString(),
                        "model-2", jsonMapper.createObjectNode()
                                .put("version", "2.0")
                                .put("url", TEST_MODEL_URL_1)
                                .toString()))
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .functionLevelAuthorizedAccounts(functionLevelAuthzParties)
                .versionLevelAuthorizedAccounts(versionLevelAuthzParties)
                .build();
    }
}
