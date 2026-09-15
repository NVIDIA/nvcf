/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package roottoken

import (
	"encoding/base64"
	"fmt"

	"github.com/hashicorp/vault/sdk/helper/xor"
)

// EncodeToken gets a token and an OTP and encodes the token.
// The OTP must have the same length as the token.
func EncodeToken(token, otp string) (string, error) {
	if len(token) == 0 {
		return "", fmt.Errorf("no token provided")
	} else if len(otp) == 0 {
		return "", fmt.Errorf("no otp provided")
	}

	// This function performs decoding checks so rather than decode the OTP,
	// just encode the value we're passing in.
	tokenBytes, err := xor.XORBytes([]byte(otp), []byte(token))
	if err != nil {
		return "", fmt.Errorf("xor of root token failed: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(tokenBytes), nil
}
