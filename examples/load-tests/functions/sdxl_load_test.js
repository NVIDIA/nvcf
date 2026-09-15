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

import { sleep, check } from 'k6'
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

export default function() {
    let response

    const payload = `{
        "text_prompts": [
            {
                "text": "A steampunk dragon soaring over a Victorian cityscape, with gears and smoke billowing from its wings.",
                "weight": 1
            }
        ],
        "sampler": "K_EULER_ANCESTRAL",
        "steps": 2,
        "seed": 0
    }`

    const params = {
        timeout: 300 * 1000, //milliseconds
        headers: {
            'Authorization': `Bearer ${__ENV.TOKEN}`,
            'Content-Type': 'application/json',
            'NVCF-POLL-SECONDS': '300'
        },
    };

    const url = `${__ENV.SDXL_NVCF_URL}`

    response = http.post(
        url, payload, params
    )
    check(response, {
            'status code MUST be 200': (response) => response.status === 200,
        })

    sleep(0.001)
}