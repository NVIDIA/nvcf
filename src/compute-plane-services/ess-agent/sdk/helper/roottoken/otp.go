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
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/hashicorp/go-secure-stdlib/base62"
)

// DefaultBase64EncodedOTPLength is the number of characters that will be randomly generated
// before the Base64 encoding process takes place.
const defaultBase64EncodedOTPLength = 16

// GenerateOTP generates a random token and encodes it as a Base64 or as a Base62 encoded string.
// Returns 0 if the generation completed without any error, 2 otherwise, along with the error.
func GenerateOTP(otpLength int) (string, error) {
	switch otpLength {
	case 0:
		// This is the fallback case
		buf := make([]byte, defaultBase64EncodedOTPLength)
		readLen, err := rand.Read(buf)
		if err != nil {
			return "", fmt.Errorf("error reading random bytes: %s", err)
		}

		if readLen != defaultBase64EncodedOTPLength {
			return "", fmt.Errorf("read %d bytes when we should have read 16", readLen)
		}

		return base64.StdEncoding.EncodeToString(buf), nil
	default:
		otp, err := base62.Random(otpLength)
		if err != nil {
			return "", fmt.Errorf("error reading random bytes: %w", err)
		}

		return otp, nil
	}
}
