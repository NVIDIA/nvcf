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
package com.nvidia.nvcf.rest.account;

import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_REGISTRY;
import static com.nvidia.boot.registries.service.registry.client.acr.AzureRegistryClient.AZURE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PUBLIC_REGISTRY_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ACR_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ARTIFACTORY_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.DOCKER_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PRIVATE_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PUBLIC_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.HARBOR_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.VOLCENGINE_REGISTRY_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACCOUNT_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACCOUNT_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_4;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_4;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_5;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_6;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_DOCKER_REGISTRY;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_ARTIFACT_REGISTRY;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_REGISTRY;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.audit.AuditService;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.registries.service.registry.RegistryLookupService;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistryService;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistryService;
import com.nvidia.boot.registries.service.registry.model.ModelRegistryService;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistryService;
import com.nvidia.nvcf.persistence.account.AccountsRepository;
import com.nvidia.nvcf.persistence.account.entity.AccountEntity;
import com.nvidia.nvcf.persistence.client.ClientsRepository;
import com.nvidia.nvcf.persistence.registry.RegistryCredentialsByAccountRepository;
import com.nvidia.nvcf.rest.account.dto.CreateAccountRequest;
import com.nvidia.nvcf.rest.registry.TestRegistryCredentialService;
import com.nvidia.nvcf.rest.registry.TestRegistryService;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDto;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.client.ClientService;
import com.nvidia.nvcf.service.registry.RegistryCredentialService;
import java.net.URI;
import java.time.Instant;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

@Service
public class TestAccountService {

    private final AccountService accountService;
    private final ClientService clientService;
    private final AuditService auditService;
    private final RegistryCredentialService registryCredentialService;
    private final AccountsRepository accountsRepository;
    private final ClientsRepository clientsRepository;
    private final RegistryCredentialsByAccountRepository registryCredentialsByAccountRepository;
    private final TestRegistryCredentialService testRegistryCredentialService;
    private final RegistryLookupService registryLookupService;
    private final RegistryMapperService registryMapperService;
    private final ModelRegistryService modelRegistryService;
    private final HelmRegistryService helmRegistryService;
    private final ResourceRegistryService resourceRegistryService;
    private final ContainerRegistryService containerRegistryService;
    private final TestRegistryService testRegistryService;
    private final String ngcContainerRegistryHostname;
    private final String casBaseUrl;
    private final String dockerBaseUrl;
    private final String ecrPrivateHostname;
    private final String ecrPublicHostname;
    private final String volcengineHostname;
    private final String acrHostname;
    private final String harborHostname;
    private final String artifactoryHostname;

    public TestAccountService(
            AccountService accountService,
            ClientService clientService,
            AuditService auditService,
            RegistryCredentialService registryCredentialService,
            AccountsRepository accountsRepository,
            ClientsRepository clientsRepository,
            RegistryCredentialsByAccountRepository registryCredentialsByAccountRepository,
            TestRegistryCredentialService testRegistryCredentialService,
            RegistryLookupService registryLookupService,
            ModelRegistryService modelRegistryService,
            HelmRegistryService helmRegistryService,
            ResourceRegistryService resourceRegistryService,
            ContainerRegistryService containerRegistryService,
            RegistryMapperService registryMapperService,
            TestRegistryService testRegistryService,
            @Value("${nvcf.registries.recognized.container.ngc.hostname}")
            String ngcContainerRegistryHostname,
            @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
            String casBaseUrl,
            @Value("${nvcf.registries.recognized.container.docker.hostname}")
            String dockerBaseUrl,
            @Value("${nvcf.registries.recognized.container.ecr.hostname}")
            String ecrPrivateHostname,
            @Value("${nvcf.registries.recognized.container.ecr-public.hostname}")
            String ecrPublicHostname,
            @Value("${nvcf.registries.recognized.container.volcengine.hostname}")
            String volcengineHostname,
            @Value("${nvcf.registries.recognized.container.acr.hostname}")
            String acrHostname,
            @Value("${nvcf.registries.recognized.container.harbor.hostname}")
            String harborHostname,
            @Value("${nvcf.registries.recognized.container.artifactory.hostname}")
            String artifactoryHostname) {
        this.accountService = accountService;
        this.clientService = clientService;
        this.auditService = auditService;
        this.registryCredentialService = registryCredentialService;
        this.accountsRepository = accountsRepository;
        this.clientsRepository = clientsRepository;
        this.registryCredentialsByAccountRepository = registryCredentialsByAccountRepository;
        this.testRegistryCredentialService = testRegistryCredentialService;
        this.registryLookupService = registryLookupService;
        this.modelRegistryService = modelRegistryService;
        this.helmRegistryService = helmRegistryService;
        this.resourceRegistryService = resourceRegistryService;
        this.containerRegistryService = containerRegistryService;
        this.registryMapperService = registryMapperService;
        this.testRegistryService = testRegistryService;
        this.ngcContainerRegistryHostname = ngcContainerRegistryHostname;
        this.casBaseUrl = casBaseUrl;
        this.dockerBaseUrl = dockerBaseUrl;
        this.ecrPrivateHostname = ecrPrivateHostname;
        this.ecrPublicHostname = ecrPublicHostname;
        this.volcengineHostname = volcengineHostname;
        this.acrHostname = acrHostname;
        this.harborHostname = harborHostname;
        this.artifactoryHostname = artifactoryHostname;

        updateNgcRegistryHostnameWithStageEndpoint();
        updateDockerRegistryHostnameWithEndpoint();
        updateEcrPrivateRegistryHostname();
        updateEcrPublicRegistryHostname();
        updateVolcengineRegistryHostname();
        updateAcrRegistryHostname();
        updateHarborRegistryHostname();
        updateArtifactoryRegistryHostname();
    }

    private void updateNgcRegistryHostnameWithStageEndpoint() {
        modelRegistryService.overwriteRegistryHostnameMap(
                URI.create(casBaseUrl).getHost(), TEST_NGC_ARTIFACT_REGISTRY);
        resourceRegistryService.overwriteRegistryHostnameMap(
                URI.create(casBaseUrl).getHost(), TEST_NGC_ARTIFACT_REGISTRY);
        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(casBaseUrl).getHost(), TEST_NGC_HELM_REGISTRY);
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(ngcContainerRegistryHostname).getHost(),
                TEST_NGC_CONTAINER_REGISTRY);
        registryLookupService.updateContainerRegistryConfigMap(
                ngcContainerRegistryHostname, TEST_NGC_CONTAINER_REGISTRY);
        registryLookupService.updateContainerRegistryMap(
                NGC_PRIVATE_REGISTRY_NAME, TEST_NGC_CONTAINER_REGISTRY);
        registryLookupService.updateHelmRegistryConfigMap(
                casBaseUrl, TEST_NGC_HELM_REGISTRY);
        registryLookupService.updateHelmRegistryMap(NGC_PRIVATE_REGISTRY_NAME,
                                                    TEST_NGC_HELM_REGISTRY);
        registryLookupService.updateModelRegistryConfigMap(
                casBaseUrl, TEST_NGC_ARTIFACT_REGISTRY);
        registryLookupService.updateModelRegistryMap(
                NGC_PRIVATE_REGISTRY_NAME, TEST_NGC_ARTIFACT_REGISTRY);
        registryLookupService.updateResourceRegistryConfigMap(
                casBaseUrl, TEST_NGC_ARTIFACT_REGISTRY);
        registryLookupService.updateResourceRegistryMap(
                NGC_PRIVATE_REGISTRY_NAME, TEST_NGC_ARTIFACT_REGISTRY);
        registryMapperService.updateNgcArtifactRegistryHostname(TEST_NGC_ARTIFACT_REGISTRY);
        registryMapperService.updateNgcHelmRegistryHostname(TEST_NGC_HELM_REGISTRY);
        registryMapperService.updateNgcContainerRegistryHostname(TEST_NGC_CONTAINER_REGISTRY);
    }

    private void updateDockerRegistryHostnameWithEndpoint() {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(dockerBaseUrl).getHost(), TEST_DOCKER_REGISTRY);
        registryLookupService.updateContainerRegistryConfigMap(
                dockerBaseUrl, TEST_DOCKER_REGISTRY);
        registryLookupService.updateContainerRegistryMap(
                DOCKER_REGISTRY_NAME, TEST_DOCKER_REGISTRY);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(dockerBaseUrl).getHost(), TEST_DOCKER_REGISTRY);
        registryLookupService.updateHelmRegistryConfigMap(
                dockerBaseUrl, TEST_DOCKER_REGISTRY);
        registryLookupService.updateHelmRegistryMap(DOCKER_REGISTRY_NAME, TEST_DOCKER_REGISTRY);
    }

    private void updateEcrPrivateRegistryHostname() {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(ecrPrivateHostname).getHost(), ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryConfigMap(
                ecrPrivateHostname, ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryMap(
                ECR_PRIVATE_REGISTRY_NAME, ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(ecrPrivateHostname).getHost(), ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryConfigMap(
                ecrPrivateHostname, ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryMap(
                ECR_PRIVATE_REGISTRY_NAME, ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
    }

    private void updateEcrPublicRegistryHostname() {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(ecrPublicHostname).getHost(), ECR_PUBLIC_REGISTRY_HOSTNAME);
        registryLookupService.updateContainerRegistryConfigMap(
                ecrPublicHostname, ECR_PUBLIC_REGISTRY_HOSTNAME);
        registryLookupService.updateContainerRegistryMap(
                ECR_PUBLIC_REGISTRY_NAME, ECR_PUBLIC_REGISTRY_HOSTNAME);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(ecrPublicHostname).getHost(), ECR_PUBLIC_REGISTRY_HOSTNAME);
        registryLookupService.updateHelmRegistryConfigMap(
                ecrPublicHostname, ECR_PUBLIC_REGISTRY_HOSTNAME);
        registryLookupService.updateHelmRegistryMap(
                ECR_PUBLIC_REGISTRY_NAME, ECR_PUBLIC_REGISTRY_HOSTNAME);
    }

    private void updateVolcengineRegistryHostname() {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(volcengineHostname).getHost(), VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryConfigMap(
                volcengineHostname, VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryMap(
                VOLCENGINE_REGISTRY_NAME, VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(volcengineHostname).getHost(), VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryConfigMap(
                volcengineHostname, VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryMap(
                VOLCENGINE_REGISTRY_NAME, VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
    }

    private void updateAcrRegistryHostname() {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(acrHostname).getHost(), AZURE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryConfigMap(
                acrHostname, AZURE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryMap(
                ACR_REGISTRY_NAME, AZURE_REGISTRY_GLOBAL_HOSTNAME);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(acrHostname).getHost(), AZURE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryConfigMap(
                acrHostname, AZURE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryMap(
                ACR_REGISTRY_NAME, AZURE_REGISTRY_GLOBAL_HOSTNAME);
    }

    private void updateHarborRegistryHostname() {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(harborHostname).getHost(), TEST_HARBOR_REGISTRY);
        registryLookupService.updateContainerRegistryConfigMap(
                harborHostname, TEST_HARBOR_REGISTRY);
        registryLookupService.updateContainerRegistryMap(
                HARBOR_REGISTRY_NAME, TEST_HARBOR_REGISTRY);
        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(harborHostname).getHost(), TEST_HARBOR_REGISTRY);
        registryLookupService.updateHelmRegistryConfigMap(
                harborHostname, TEST_HARBOR_REGISTRY);
        registryLookupService.updateHelmRegistryMap(
                HARBOR_REGISTRY_NAME, TEST_HARBOR_REGISTRY);
    }

    private void updateArtifactoryRegistryHostname() {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(artifactoryHostname).getHost(), TEST_ARTIFACTORY_REGISTRY);
        registryLookupService.updateContainerRegistryConfigMap(
                artifactoryHostname, TEST_ARTIFACTORY_REGISTRY);
        registryLookupService.updateContainerRegistryMap(
                ARTIFACTORY_REGISTRY_NAME, TEST_ARTIFACTORY_REGISTRY);
        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(artifactoryHostname).getHost(), TEST_ARTIFACTORY_REGISTRY);
        registryLookupService.updateHelmRegistryConfigMap(
                artifactoryHostname, TEST_ARTIFACTORY_REGISTRY);
        registryLookupService.updateHelmRegistryMap(
                ARTIFACTORY_REGISTRY_NAME, TEST_ARTIFACTORY_REGISTRY);
    }

    private CreateAccountRequest buildCreateAccountRequest(String name, String adminClientId,
                                                           List<RegistryCredentialDto> registryCredentials) {
        var builder = CreateAccountRequest.builder()
                .name(name)
                .registryCredentials(registryCredentials);
        if (adminClientId != null) {
            builder.adminClientId(adminClientId);
        }
        return builder.build();
    }

    public CreateAccountRequest buildCreateAccountRequest(String name, String adminClientId) {
        return buildCreateAccountRequest(name, adminClientId, List.of(
                TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                TEST_NGC_HELM_REGISTRY_CREDENTIAL));
    }

    public record AccountData(String ncaId, String name, String adminClientId) {
    }

    public List<AccountData> getDefaultAccounts() {
        return List.of(
                new AccountData(TEST_NCA_ID, TEST_ACCOUNT_NAME, TEST_CLIENT_ID),
                new AccountData(TEST_NCA_ID_2, TEST_ACCOUNT_NAME_2, TEST_CLIENT_ID_2),
                new AccountData(TEST_AUTHORIZED_NCA_ID_1, "test-authorized-account-1",
                                TEST_AUTHORIZED_CLIENT_ID_1),
                new AccountData(TEST_AUTHORIZED_NCA_ID_2, "test-authorized-account-2",
                                TEST_AUTHORIZED_CLIENT_ID_2),
                new AccountData(TEST_AUTHORIZED_NCA_ID_3, "test-authorized-account-3",
                                TEST_AUTHORIZED_CLIENT_ID_3),
                new AccountData(TEST_AUTHORIZED_NCA_ID_4, "test-authorized-account-4",
                                TEST_AUTHORIZED_CLIENT_ID_4),
                new AccountData(TEST_AUTHORIZED_NCA_ID_5, "test-authorized-account-5", null),
                new AccountData(TEST_AUTHORIZED_NCA_ID_6, "test-authorized-account-6", null));
    }

    public List<AccountData> getProdDefaultAccounts() {
        return List.of(
                new AccountData(TEST_NCA_ID, TEST_ACCOUNT_NAME, TEST_CLIENT_ID));
    }

    public void createDefaultAccountsClientsAndRegistries() {
        createDefaultAccountsClientsAndRegistries(false);
    }

    public void createDefaultAccountsClientsAndRegistries(boolean validateRegistryCredentials) {
        for (AccountData acc : getDefaultAccounts()) {
            var auditEventPayloadBuilder = getAuditEventPayloadBuilder();
            var request = buildCreateAccountRequest(acc.name, acc.adminClientId);
            accountService.createCloudAccount(acc.ncaId, request, auditEventPayloadBuilder);
            if (validateRegistryCredentials) {
                testRegistryCredentialService.validateRegistryCredentials(acc.ncaId);
            }
        }
    }


    public void cleanupAccountsClientsAndRegistries() {
        var accounts = accountService.getAccounts();
        accounts.forEach(account -> {
            var ncaId = account.getNcaId();
            registryCredentialService.deleteAllRegistryCredentials(ncaId,
                                                                   getAuditEventPayloadBuilder());
        });

        clientService.clearClientCache();
        accountsRepository.deleteAll();
        clientsRepository.deleteAll();
        registryCredentialsByAccountRepository.deleteAll();
        testRegistryCredentialService.invalidateCache();
        testRegistryService.clearAll();
    }

    public void createAccountWithNoOAuth2Clients(String ncaId, String name) {
        // account with no OAuth2 client id
        var request = buildCreateAccountRequest(name, null);
        accountService.createCloudAccount(ncaId, request, getAuditEventPayloadBuilder());
    }

    public void createAccountAndAssociateClients(String ncaId, Set<String> clientIds) {
        var name = "cloud-account";
        var request = buildCreateAccountRequest(name, null);
        accountService.createCloudAccount(ncaId, request, getAuditEventPayloadBuilder());

        Optional.ofNullable(clientIds)
                .filter(cIds -> !cIds.isEmpty())
                .ifPresent(cIds -> cIds
                        .forEach(cid -> accountService.associateClient(ncaId, cid,
                                                                       getAuditEventPayloadBuilder())));

        // Check if the DB contains the rows for account/clients.
        var accountEntity = accountService.getAccount(ncaId);
        assertThat(accountEntity).isNotNull();
        assertThat(accountEntity.getNcaId()).isEqualTo(ncaId);
        if (clientIds != null && !clientIds.isEmpty()) {
            assertThat(accountEntity.getClientIds())
                    .containsExactlyInAnyOrderElementsOf(clientIds);
            clientIds.forEach(clientId -> {
                var client = clientService.lookupClient(clientId);
                assertThat(client).isNotNull();
                assertThat(client).isPresent();
                assertThat(client.get().getClientId()).isNotBlank();
                assertThat(client.get().getClientId()).isEqualTo(clientId);
                assertThat(client.get().getNcaId()).isEqualTo(ncaId);
            });
        } else {
            assertThat(accountEntity.getClientIds()).isNull();
        }
    }

    public void deleteAccount(String ncaId) {
        registryCredentialService.deleteAllRegistryCredentials(ncaId, getAuditEventPayloadBuilder());

        // Delete clients associated with the account.
        var accountEntity = accountService.getAccount(ncaId);
        var clientIds = Objects.requireNonNullElse(accountEntity.getClientIds(), Set.<String>of());
        clientIds.forEach(clientService::deleteClient);
        accountService.deleteAccount(ncaId, getAuditEventPayloadBuilder());
    }

    public void updateAccountMaxTelemetries(String ncaId, Integer maxTelemetriesAllowed) {
        var accountEntity = accountService.getAccount(ncaId);
        var updatedAccountEntity = accountEntity.toBuilder()
                .maxTelemetriesAllowed(maxTelemetriesAllowed)
                .lastUpdatedAt(Instant.now())
                .build();
        accountService.saveAccount(updatedAccountEntity);
    }

    public void updateAccountMaxRegistryCredentials(String ncaId,
                                                    Integer maxRegistryCredentialsAllowed) {
        var accountEntity = accountService.getAccount(ncaId);
        var updatedAccountEntity = accountEntity.toBuilder()
                .maxRegistryCredentialsAllowed(maxRegistryCredentialsAllowed)
                .lastUpdatedAt(Instant.now())
                .build();
        accountService.saveAccount(updatedAccountEntity);
    }

    public AccountEntity getAccountByNcaId(String ncaId) {
        return accountService.getAccount(ncaId);
    }

    private AuditEventPayload.Builder getAuditEventPayloadBuilder() {
        return auditService.auditEventPayloadBuilder()
                .groupType("NVCF-TESTS")
                .actorId("unknown")
                .actorLocation("nowhere")
                .subjectId("unknown")
                .subjectLocation("nowhere")
                .objectLocation("NVCF-TESTS")
                .custom("test-context", "TestAccountService"); // Avoid exception from
    }
}
