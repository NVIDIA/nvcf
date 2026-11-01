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
import sse from "k6/x/sse"
import { Trend } from 'k6/metrics';

export const TargetResponsesPerSecondTrend = new Trend('TargetResponsesPerSecond');
export const ResponsesPerSecondTrend = new Trend('ResponsesPerSecond');
export const options = {
    thresholds: {
        checks: [
            {
                threshold: 'rate>0.99', // string
                abortOnFail: true, // boolean
            },
        ],
        ResponsesPerSecond: [
            {
                threshold: `avg>${1/Number(__ENV.RESPONSE_DELAY) * 0.95}`, // target RPS within 5%
                abortOnFail: false, // boolean
            },
        ],
    }
};

function memoizeRepeat() {
    const cache = {};

    return function(stringSize) {
        if (!(stringSize in cache)) {
            cache[stringSize] = 'x'.repeat(stringSize);
        }
        return cache[stringSize];
    };
}

const memoizedRepeat = memoizeRepeat();

export default function() {

    const randomString = memoizedRepeat(__ENV.SENT_MESSAGE_SIZE)

    const payload = `{
        "message": "${randomString}",
        "repeats": ${Number(__ENV.RESPONSE_COUNT)},
        "delay": ${Number(__ENV.RESPONSE_DELAY)},
        "stream": true
    }`

    const params = {
        timeout: 300 * 1000, //milliseconds
        method: 'POST',
        body: payload,
        headers: {
            'Authorization': `Bearer ${__ENV.TOKEN}`,
            'Content-Type': 'application/json',
            'NVCF-POLL-SECONDS': '300',
            'Accept': 'text/event-stream'
        },
    };

    const url = `${__ENV.HTTP_SUPREME_NVCF_URL}`

    let response_count = 0
    let start
    let end
    const response = sse.open(url, params, function (client) {
        client.on('event', function () {
            response_count++
            if (response_count === 1) {
                start = Date.now();
            } else if (response_count === Number(__ENV.RESPONSE_COUNT)){
                end = Date.now();
                client.close()
            }
        })
        client.on('error', function (e) {
            console.log('An unexpected error occurred: ', e.error())
            client.close()
        })
    })
    const statusIsOk = check(response, {
        'status code MUST be 200': (response) => response.status === 200,
    })

    if (!statusIsOk) {
        console.error('Check failed:', response);
        TargetResponsesPerSecondTrend.add(0);
        ResponsesPerSecondTrend.add(0);
    } else {
        let responseTimeElapsed = (end - start)/1000
        TargetResponsesPerSecondTrend.add(1/Number(__ENV.RESPONSE_DELAY));
        ResponsesPerSecondTrend.add((Number(__ENV.RESPONSE_COUNT)-1)/responseTimeElapsed);
    }
}