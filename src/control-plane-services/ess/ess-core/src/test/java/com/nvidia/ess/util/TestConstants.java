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

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.ess.exceptions.ProblemSummary;
import com.nvidia.ess.utils.EntityUtils;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import lombok.SneakyThrows;
import tools.jackson.core.type.TypeReference;
import tools.jackson.databind.ObjectMapper;

public class TestConstants {

    public static final String TEST_ESS_REQUEST_ID = Uuids.timeBased().toString();
    public static final String TEST_ESS_AGENT_ID = UUID.randomUUID().toString();

    public static final String TEST_NAMESPACE = "test-namespace";
    public static final int TEST_ENTITY_HASH_SIZE = 1;
    public static final String TEST_ENTITY_TYPE = "test-entity-type";
    public static final String TEST_ENTITY_ID = "test-entity-id";
    public static final String TEST_ENTITY =
            EntityUtils.getEntity(TEST_ENTITY_TYPE, TEST_ENTITY_ID);
    public static final int TEST_HASH_BUCKET = 0;

    public static final String TEST_SECRET_PATH = "dir1/dir2/secret1";
    public static final String TEST_SECRET_PATH_PARENT = "dir1/dir2";
    public static final String TEST_SECRET_PATH_ROOT = "dir1";

    public static final String DELETION_TEST_SECRET_PATH = "a/b/c/d/e";
    public static final String DELETION_TEST_SECRET_PATH_PARENT = "a/b/c/d";
    public static final String DELETION_TEST_SECRET_PATH_GP = "a/b/c";
    public static final String DELETION_TEST_SECRET_PATH_GGP = "a/b";
    public static final String DELETION_TEST_SECRET_PATH_ROOT = "a";

    public static final List<String> DELETION_TEST_SECRET_PATH_SIBLINGS_LEFT = List.of(
        "a/b/c/d/a",
        "a/b/c/d/b",
        "a/b/c/d/c",
        "a/b/c/d/d"
    );

    public static final List<String> DELETION_TEST_SECRET_PATH_SIBLINGS_RIGHT = List.of(
        "a/b/c/d/f",
        "a/b/c/d/g",
        "a/b/c/d/h",
        "a/b/c/d/i"
    );

    public static final List<String> DELETION_TEST_SECRET_PATH_FIRST_COUSINS_LEFT = List.of(
        "a/b/c/a/z",
        "a/b/c/b/a",
        "a/b/c/ba",
        "a/b/c/bb/a",
        "a/b/c/bbb",
        "a/b/c/bcd",
        "a/b/c/c/a",
        "a/b/c/c/b",
        "a/b/c/c/z"
    );

    public static final List<String> DELETION_TEST_SECRET_PATH_FIRST_COUSINS_RIGHT = List.of(
        "a/b/c/dcab",
        "a/b/c/e/a",
        "a/b/c/e/b",
        "a/b/c/ee/a",
        "a/b/c/eee",
        "a/b/c/f/a",
        "a/b/c/f/z",
        "a/b/c/ff/a",
        "a/b/c/fff"
    );

    public static final List<String> DELETION_TEST_SECRET_PATH_SECOND_COUSINS_LEFT = List.of(
        "a/b/b/a",
        "a/b/b/aa/c/e/g",
        "a/b/b/c/d",
        "a/b/bcdef",
        "a/b/bcgh/i/j/k/l",
        "a/b/bchj"
    );

    public static final List<String> DELETION_TEST_SECRET_PATH_SECOND_COUSINS_RIGHT = List.of(
        "a/b/cba/d/e/f/g/h",
        "a/b/d/e/f",
        "a/b/d/e/g",
        "a/b/d/f/g",
        "a/b/defg",
        "a/b/e/fgh/i/j"
    );

    public static final List<String> DELETION_TEST_SECRET_PATHS_WITH_DIFF_ROOT = List.of(
        "b/c",
        "b/cc/d/e/f",
        "c",
        "d/e/f/g/h/i/j",
        "d/e/f/g/h/j/k",
        "d/f/g/h/j/k",
        "d/g/h"
    );

    public static final UUID TEST_SECRET_VERSION = Uuids.timeBased();

    public static final String TEST_ENTITY_TYPE_2 = "test-entity-type-2";

    public static final String TEST_NEK_ID = "test_ns_key1";

    public static final String TEST_DB_NO_NODE_AVAILABLE_EXCEPTION =
            "No node was available to execute the query";

    public static final UUID TEST_CREATE_SECRET_SUCCESS_NEW_VERSION = Uuids.timeBased();
    public static final UUID TEST_CREATE_SECRET_REQUEST_CAS_VERSION = Uuids.timeBased();

    public static final String TEST_CREATE_SECRET_REQUEST_PAYLOAD_JSON = """
      {
        "sampleNull": null,
        "sampleInteger": -2,
        "sampleFloat": -2.5,
        "sampleString": "sampleValue",
        "sampleEmptyString": "",
        "sampleList": ["sampleValue1", "sampleValue2"],
        "sampleEmptyList": [],
        "sampleMap": {"key1": "value1", "key2": ["hello", "world"]},
        "sampleEmptyMap": {}
      }
      """.trim();

    public static final Map<String, Object> TEST_CREATE_SECRET_REQUEST_PAYLOAD = constructTestCreateSecretRequestPayload();
    public static final String TEST_SECRET_DATA_CIPHERTEXT = "<encrypted data>";

    public static final ProblemSummary TEST_PROBLEM_SUMMARY = ProblemSummary.builder()
            .problemBrief("test")
            .affectedResource("test")
            .build();

    @SneakyThrows
    private static Map<String, Object> constructTestCreateSecretRequestPayload() {
        var typeRef = new TypeReference<HashMap<String,Object>>() {};
        return (new ObjectMapper()).readValue(TEST_CREATE_SECRET_REQUEST_PAYLOAD_JSON, typeRef);      
    }
}
