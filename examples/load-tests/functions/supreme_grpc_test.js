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

import grpc from 'k6/net/grpc';
import { check } from 'k6';

export const options = {
    thresholds: {
        checks: [
            {
                threshold: 'rate>0.99', // string
                abortOnFail: true, // boolean
            },
        ],
        grpc_req_duration: [
            {
                threshold: 'avg<5000', // string
                abortOnFail: true, // boolean
            },
        ],
    },
};


const client = new grpc.Client();
client.load(['definitions'], 'echo.proto');


export default () => {
    client.connect(`${__ENV.NVCF_GRPC_URL}`, {
        plaintext: __ENV.GRPC_PLAINTEXT === 'true',
    });

    const metadata = {
        'function-id': `${__ENV.GRPC_SUPREME_FUNCTION_ID}`,
        'authorization': `Bearer ${__ENV.TOKEN}`,
    };
    if (__ENV.GRPC_SUPREME_FUNCTION_VERSION_ID) {
        metadata['function-version-id'] = `${__ENV.GRPC_SUPREME_FUNCTION_VERSION_ID}`;
    }

    const params = { metadata };
    const randomString = 'x'.repeat(__ENV.SENT_MESSAGE_SIZE)
    const req = { message: randomString, repeats: __ENV.RESPONSE_COUNT };
    const response = client.invoke('Echo/EchoMessage', req, params);

    const statusIsOk = check(response, {
        'status is OK': (r) => r && r.status === grpc.StatusOK,
    });

    if (!statusIsOk) {
        console.error('Check failed:', response.error);
    }

    // console.log(JSON.stringify(response.message));
    // console.log(JSON.stringify(response.error));

    client.close();
};