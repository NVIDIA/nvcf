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
package com.nvidia.nvcf.service.registry;

import static com.nvidia.nvcf.rest.function.management.TestManagementService.Trait.CONTAINER_BASED;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.AssertionsForClassTypes.assertThatThrownBy;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.TestManagementService;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.util.MockEssServer;
import java.util.EnumSet;
import java.util.HashSet;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class RegistryCredentialServiceTest {

    @Autowired
    private RegistryCredentialService registryCredentialService;

    @Autowired
    private RegistryCredentialLookupService registryCredentialLookupService;

    @Autowired
    private RegistryCredentialEssService registryCredentialEssService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestManagementService testManagementService;

    @Autowired
    private FunctionLookupService functionLookupService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private AccountService accountService;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    private AuditEventPayload.Builder auditEventPayloadBuilder;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        auditEventPayloadBuilder = testCommonService.getAuditEventPayloadBuilder();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
    }

    @Test
    void shouldNotDeleteRegistryCredentialWithDependentFunctions() {
        // Create default accounts and corresponding registry credentials.
        testAccountService.createDefaultAccountsClientsAndRegistries();

        // Verify accounts were created successfully.
        var accounts = accountService.getAccounts();
        assertThat(accounts).isNotEmpty().hasSize(testAccountService.getDefaultAccounts().size());

        // Verify registry credentials were created successfully.
        var registryCredentialIds = new HashSet<UUID>();
        accounts.forEach(account -> {
            var ncaId = account.getNcaId();
            var dtos = registryCredentialLookupService.getRegistryCredentialDtos(ncaId);
            dtos.forEach(dto -> {
                var registryCredentialId = dto.registryCredentialId();
                var artifactTypes = dto.artifactTypes();
                var secret = registryCredentialEssService.getRegistryCredentialSecret(ncaId,
                                                                                      registryCredentialId);
                assertThat(secret).isPresent();
                assertThat(secret.get().value().asString()).isNotBlank();

                if (artifactTypes.contains(ArtifactTypeEnum.CONTAINER)
                        && ncaId.equals(TEST_NCA_ID)) {
                    // Save the container registry cred id for account TEST_NCA_ID so that we can
                    // try to delete it explicitly.
                    registryCredentialIds.add(registryCredentialId);
                }
            });
            assertThat(registryCredentialLookupService.getRegistryCredentialDtos(ncaId)).hasSize(3);
        });

        // Create a container-based function in account TEST_NCA_ID.
        testManagementService
                .createFunctionEntityWithTraits(TEST_FUNCTION_ID,
                                                TEST_VERSION_ID_1,
                                                TEST_NCA_ID,
                                                TEST_FUNCTION_NAME,
                                                EnumSet.of(CONTAINER_BASED));

        // Verify container-based function was created successfully.
        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionIdOrThrow(TEST_FUNCTION_ID,
                                                          TEST_VERSION_ID_1);
        assertThat(function).isNotNull();
        assertThat(function.getContainerImage()).isNotBlank();

        // Try deleting the container registry credential. It will fail as there is a function
        // in the account that depends on the container registry.
        assertThat(registryCredentialIds.stream().findAny()).isPresent(); // Just one in the set.
        var registryId = registryCredentialIds.stream().findAny().get();
        assertThatThrownBy(
                () -> registryCredentialService.deleteRegistryCredential(
                        TEST_NCA_ID, registryId, auditEventPayloadBuilder, true))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining(TEST_FUNCTION_ID.toString())
                .hasMessageContaining(TEST_VERSION_ID_1.toString())
                .hasMessageContaining(registryId.toString());

        // Delete the function. Then, try deleting the system provisioned container registry.
        // This time, it will be successful as there is no other dependency.
        functionsRepository.deleteAll();
        registryCredentialService
                .deleteRegistryCredential(TEST_NCA_ID, registryId, auditEventPayloadBuilder, true);

        // Delete default accounts and registryCredentials created at the beginning of the test.
        testAccountService.cleanupAccountsClientsAndRegistries();
    }

}
