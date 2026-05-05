// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package special

import "github.com/volcengine/volcengine-go-sdk/volcengine/response"

type ResponseSpecial func(response.VolcengineResponse, interface{}) interface{}

var responseSpecialMapping map[string]ResponseSpecial

func init() {
	responseSpecialMapping = map[string]ResponseSpecial{
		"iot": iotResponse,
	}
}

func ResponseSpecialMapping() map[string]ResponseSpecial {
	return responseSpecialMapping
}
