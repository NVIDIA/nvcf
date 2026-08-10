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
        // Rate: content must be OK more than 95 times
        checks: [
            {
                threshold: 'rate>0.99', // string
                abortOnFail: true, // boolean
            },
        ],
        // Trend: Percentiles, averages, medians, and minimums
        // must be within specified milliseconds.
        http_req_duration: [
            {
                threshold: 'avg<500', // string
                abortOnFail: true, // boolean
            },
        ],
    },
};

export default function() {
    let response

    const headers = {
        headers: {
            'Authorization': `Bearer ${__ENV.TOKEN}`,
            'Content-Type': 'application/json',
            'Accept': 'application/json'
        },
    };

    const url = `${__ENV.BASE_URL}/v2/orgs/${__ENV.ORG_ID}/nvct/tasks/${__ENV.TASK_ID}/results`

    response = http.get(
        url, headers
    )
    const statusIsOk = check(response, {
        'status code MUST be 200': (response) => response.status === 200,
    })

    if (!statusIsOk) {
        console.error('Check failed:', response.status);
        console.error('Response:', response.body);
    }

    sleep(1)
}
