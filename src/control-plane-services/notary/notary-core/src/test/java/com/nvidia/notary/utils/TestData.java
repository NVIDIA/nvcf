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
package com.nvidia.notary.utils;

import java.time.Instant;
import java.util.Date;

public class TestData {

    public static final String TEST_TIME_STRING = "2023-10-03T08:25:24.00Z";
    public static final Instant TEST_TIME_INSTANT = Instant.parse(TEST_TIME_STRING);
    public static final Date TEST_TIME_DATE = Date.from(TEST_TIME_INSTANT);

    public static final String TEST_JTI = "13273c57-75d6-4d9d-9d26-7e337aa9d6e6";

    public static final String SERVICE_ID_1 = "xqlnhnfz6tjcz-ashudoahdkz6zrpjttwhaxjhaketm";
    public static final String SERVICE_ID_2 = "jkpojec1fc1cfinnsllwmd2qnfaxsu042e-ilpy1koi";

    public static final String SIGNING_KEY_ID_1 = "signing-key-kid-1";

    public static final String TEST_FIXED_JTI = "2a280586-1809-4ac6-a75d-58666fe12eda";

    public static final String REQUEST_DATA_SERIALIZED = """
            {
                "audience_service_ids": ["xqlnhnfz6tjcz-ashudoahdkz6zrpjttwhaxjhaketm"],
                "data": {
                    "string": "Example String",
                    "number": 123,
                    "boolean": true,
                    "null": null,
                    "object": {
                        "nestedString": "Nested Example",
                        "nestedNumber": 456,
                        "nestedObject": {
                            "deepString": "Deeply Nested Example",
                            "deepArray": [
                                7,
                                8,
                                9
                            ],
                            "deepObject": {
                                "deeperString": "Deeper Level",
                                "deeperNumber": 101112,
                                "deeperObject": {
                                    "deepestString": "Deepest Level",
                                    "deepestList": [
                                        10,
                                        "eleven",
                                        true
                                    ]
                                }
                            }
                        }
                    },
                    "array": [
                        1,
                        "two",
                        false,
                        null,
                        {
                            "objectInArray": 789
                        }
                    ],
                    "arrayOfObjects": [
                        {
                            "id": 1,
                            "name": "Item One"
                        },
                        {
                            "id": 2,
                            "name": "Item Two"
                        }
                    ],
                    "specialCharacters": "\\u003c\\u003e\\u0026\\u0022\\u0027\\/",
                    "escapedCharacters": "\\\\t\\\\n\\\\r\\\\b\\\\f\\\\\\"\\\\\\\\"
                }
            }
            """;

    public static final String EXPECTED_ASSERTION_CLAIMS = """
            {\
            "iss":"http://assertion.issuer.test",\
            "sub":"oauth2-client-id",\
            "aud":"xqlnhnfz6tjcz-ashudoahdkz6zrpjttwhaxjhaketm",\
            "assertion":{\
            "string":"Example String",\
            "number":123,\
            "boolean":true,\
            "null":null,\
            "object":{\
            "nestedString":"Nested Example",\
            "nestedNumber":456,\
            "nestedObject":{"deepString":"Deeply Nested Example",\
            "deepArray":[7,8,9],"deepObject":{\
            "deeperString":"Deeper Level","deeperNumber":101112,\
            "deeperObject":{"deepestString":"Deepest Level",\
            "deepestList":[10,"eleven",true]}}}},\
            "array":[1,"two",false,null,{"objectInArray":789}],\
            "arrayOfObjects":[{"id":1,"name":"Item One"},{"id":2,"name":"Item Two"}],\
            "specialCharacters":"<>&\\"'/",\
            "escapedCharacters":"\\\\t\\\\n\\\\r\\\\b\\\\f\\\\\\"\\\\\\\\"},\
            "iat":1696321524,"jti":"2a280586-1809-4ac6-a75d-58666fe12eda"}""";
}
