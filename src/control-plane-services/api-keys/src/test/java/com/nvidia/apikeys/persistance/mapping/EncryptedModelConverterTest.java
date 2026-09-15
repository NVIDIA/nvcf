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

package com.nvidia.apikeys.persistance.mapping;

import static org.assertj.core.api.AssertionsForClassTypes.assertThat;

import com.fasterxml.jackson.annotation.JsonIgnore;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.apikeys.App;
import com.nvidia.apikeys.utils.JsonUtils;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.web.BaseIntegrationTest;
import com.nvidia.boot.jwt.services.JwtService;
import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverter;
import com.nvidia.boot.jwt.services.mapping.annotation.EncryptedFields;
import com.nvidia.boot.jwt.services.mapping.annotation.ValueObject;
import java.time.Instant;
import java.util.Map;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;
import org.springframework.test.context.ContextConfiguration;

@Slf4j
@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = App.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class EncryptedModelConverterTest extends BaseIntegrationTest {

    @Autowired
    private JwtService jwtService;

    @Autowired
    private EncryptedModelConverter<Object, Object> encryptedModelConverter;


    @Autowired
    private EncryptedModelConverter<KeyModel, KeyVo> keyEncryptedModelConverter;

    private static final JsonMapper JSON_MAPPER = JsonUtils.getRequestResponseJsonMapper();

    @Test
    void singleConverterWithGenerics() {
        // must be the exact same object
        assertThat(encryptedModelConverter).isSameAs(keyEncryptedModelConverter);
    }

    @Test
    void keyModel() {
        var vo = KeyVo.builder()
                .keyStatus(KeyStatus.ACTIVE)
                .ownerType(KeyOwnerType.USER)
                .ownerId("owner id")
                .issuerServiceId("issuer service id")
                .audienceServiceIds(Set.of("audience service id"))
                .keyId("key id")
                .keyHash("a hash")
                .expiresAt(Instant.now())
                .deletesAt(Instant.now().plusSeconds(1234))
                .apiKeySuffix("a suffix")
                .authorizations("some auth")
                .description("a description")
                .build();
        var model = keyEncryptedModelConverter.voToModel(vo);
        var voTranslated = keyEncryptedModelConverter.modelToVo(model);
        assertThat(vo).isEqualTo(voTranslated);
    }

    @Test
    void keyByOwnerAndService() {
        var vo = KeyByOwnerAndServiceVo.builder()
                .ownerType(KeyOwnerType.USER)
                .ownerId("owner id")
                .issuerServiceId("issuer service id")
                .keyId("key id")
                .ownerStatus(KeyOwnerStatus.ACTIVE)
                .ownerStatusUpdatedAt(Instant.now())
                .expiresAt(Instant.now().plusSeconds(12345))
                .deletesAt(Instant.now().plusSeconds(123456))
                .keyStatus(KeyStatus.ACTIVE)
                .keyHash("brown")
                .apiKeySuffix("a suffix")
                .audienceServiceIds(Set.of("audience service id"))
                .description("a description")
                .build();
        var model = (KeyByOwnerAndServiceModel) encryptedModelConverter.voToModel(vo);
        var voTranslated = (KeyByOwnerAndServiceVo) encryptedModelConverter.modelToVo(model);
        assertThat(vo).isEqualTo(voTranslated);
    }

    @Data
    @Builder(toBuilder = true)
    @NoArgsConstructor
    @AllArgsConstructor
    @Table("standard_models")
    public static class StandardModel {

        @PrimaryKeyColumn(name = "field1", ordinal = 0, type = PrimaryKeyType.PARTITIONED)
        String field1;

        @Column("field2")
        String field2;
        @EncryptedFields(encryptionKeyName = "payload_jwe_kid", valueObject = VoStandard.class)
        @Column("encryptedField")
        String encryptedField;
    }

    @Data
    @Builder(toBuilder = true)
    @NoArgsConstructor
    @AllArgsConstructor
    @ValueObject(model = StandardModel.class)
    public static class VoStandard {

        String field1;
        String field2;
        @JsonIgnore
        String ignoreMe;
        String encryptedField1;
        String encryptedField2;
    }

    @Test
    void ignoreVoField() {
        var vo = VoStandard.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField1("encrypted field value")
                .ignoreMe("shouldn't make it into the model")
                .build();
        var model = (StandardModel) encryptedModelConverter.voToModel(vo);
        var voTranslated = (VoStandard) encryptedModelConverter.modelToVo(model);
        var expectedVo = VoStandard.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField1("encrypted field value")
                .build();
        assertThat(expectedVo).isEqualTo(voTranslated);
    }

    @Test
    void addEncryptedFieldToVo()
            throws JacksonException {
        // model doesn't contain encryptedField2
        var oldVoEncryptedFields = Map.of("encryptedField1", "encrypted field value");
        var model = StandardModel.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField(encryptPayload(oldVoEncryptedFields))
                .build();
        var voTranslated = (VoStandard) encryptedModelConverter.modelToVo(model);
        var expectedVo = VoStandard.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField1("encrypted field value")
                .build();
        assertThat(expectedVo).isEqualTo(voTranslated);
    }

    @Test
    void noFieldsToStart()
            throws JacksonException {
        // model will have no values in the encrypted field, so Vo encrypted fields will be empty
        Map<String, Object> oldVoEncryptedFields = Map.of();
        var model = StandardModel.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField(encryptPayload(oldVoEncryptedFields))
                .build();
        var voTranslated = (VoStandard) encryptedModelConverter.modelToVo(model);
        var expectedVo = VoStandard.builder()
                .field1("field1 value")
                .field2("field2 value")
                .build();
        assertThat(expectedVo).isEqualTo(voTranslated);
    }

    @Test
    void removeEncryptedFieldFromVo()
            throws JacksonException {
        // model has an extra field not in the Vo anymore, encryptedField3
        var oldVoEncryptedFields = Map.of("encryptedField1", "encrypted field value",
                                          "encryptedField3", "encrypted field value 3");
        var model = StandardModel.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField(encryptPayload(oldVoEncryptedFields))
                .build();
        var voTranslated = (VoStandard) encryptedModelConverter.modelToVo(model);
        var expectedVo = VoStandard.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField1("encrypted field value")
                .build();
        assertThat(expectedVo).isEqualTo(voTranslated);
    }

    private String encryptPayload(Map<String, ?> voFields)
            throws JacksonException {
        return jwtService.encryptWithKeysetName("payload_jwe_kid",
                                                JSON_MAPPER.writeValueAsString(voFields));
    }
}
