// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { check } from 'k6'
import http from 'k6/http'

export const options = {
    thresholds: {
        checks: [
            {
                threshold: 'rate>0.99', // string
                abortOnFail: true, // boolean
            },
        ],
        http_req_duration: [
            {
                threshold: 'avg<15000', // string
                abortOnFail: true, // boolean
            },
        ],
    },
};

function memoizePayload() {
    let cache = null;

    return function() {
        if (cache === null) {
            const payloadObj = {
                "message": 'x'.repeat(__ENV.SENT_MESSAGE_SIZE),
                "repeats": Number(__ENV.RESPONSE_COUNT),
            };
            if (__ENV.RESPONSE_DELAY_TIME) {
                payloadObj.delay = Number(__ENV.RESPONSE_DELAY_TIME);
            }
            cache = JSON.stringify(payloadObj);
        }
        return cache;
    };
}

const getPayload = memoizePayload();

const params = {
    timeout: 300 * 1000, //milliseconds
    headers: {
        'Authorization': `Bearer ${__ENV.TOKEN}`,
        'Content-Type': 'application/json',
        'NVCF-POLL-SECONDS': '300'
    },
};

if (__ENV.INVOKE_HOST) {
    params.headers['Host'] = __ENV.INVOKE_HOST;
}

export default function() {
    let response

    const payload = getPayload();

    const url = `${__ENV.HTTP_SUPREME_NVCF_URL}`

    response = http.post(
        url, payload, params
    )

    const statusIsOk = check(response, {
        'status code MUST be 200': (response) => response.status === 200,
    })

    if (!statusIsOk) {
        console.error('Check failed:', response);
    }
}