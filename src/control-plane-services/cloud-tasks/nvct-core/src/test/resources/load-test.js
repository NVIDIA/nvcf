// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
import http from 'k6/http';

export default function () {
    const url = 'https://stg.api.nvcf.nvidia.com/v1/nvcf';
    const payload = JSON.stringify({
        "requestHeader": {
            "functionName": "simple_int8_sre",
            "functionId": "7d199eeb-8af6-4588-ba60-3009773cd29d",
            "metadata": [{"key": "metadata-key1", "value": "value1"},
                {"key": "metadata-key2", "value": "value2"}]
        },
        "requestBody": {
            "id": "42",
            "inputs": [{
                "name": "INPUT0",
                "shape": [1, 16],
                "datatype": "INT8",
                "data": [5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5]
            }, {
                "name": "INPUT1",
                "shape": [1, 16],
                "datatype": "INT8",
                "data": [1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1]
            }],
            "outputs": [{"name": "OUTPUT0"}, {"name": "OUTPUT1"}]
        }
    });
    const params = {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': 'Bearer <>',
        },
    };
    const response = http.post(url, payload, params);
    if (response.status == 202) {
        const requestId = response.json().reqId;
        let response_status = 202
        while (response_status == 202) {
            const res = http.get(url + '/' + requestId, params);
            response_status = res.status; // console.log(response_status)
        }
    }
}
