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

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"

	echo "github.com/labstack/echo/v4"
)

func requestRoutingKey(req *http.Request) string {
	if req == nil {
		return ""
	}

	return routingKeyFromOpenAIModelID(requestModelID(req))
}

func requestModelID(req *http.Request) string {
	if req == nil || req.Body == nil {
		return ""
	}

	body, err := captureRequestBody(req)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return ""
	}

	mediaType, params, err := mime.ParseMediaType(req.Header.Get(echo.HeaderContentType))
	if err != nil {
		return ""
	}

	switch mediaType {
	case echo.MIMEApplicationJSON:
		return requestModelIDFromJSON(body)
	case "multipart/form-data":
		return requestModelIDFromMultipart(body, params["boundary"])
	default:
		return ""
	}
}

func requestModelIDFromJSON(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	var model string
	if err := json.Unmarshal(payload["model"], &model); err != nil {
		return ""
	}

	return model
}

func requestModelIDFromMultipart(body []byte, boundary string) string {
	if boundary == "" {
		return ""
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return ""
		}
		if err != nil {
			return ""
		}

		switch part.FormName() {
		case "model":
			value, err := io.ReadAll(part)
			if err != nil {
				return ""
			}
			return string(value)
		case "llm":
			value, err := io.ReadAll(part)
			if err != nil {
				return ""
			}
			return requestModelIDFromJSON(value)
		}
	}
}

func rewriteJSONModel(body []byte, model string) ([]byte, bool, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return body, false, nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false, nil
	}
	if _, ok := payload["model"]; !ok {
		return body, false, nil
	}

	encodedModel, err := json.Marshal(model)
	if err != nil {
		return nil, false, fmt.Errorf("marshal model override: %w", err)
	}
	payload["model"] = encodedModel

	rewritten, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("marshal rewritten payload: %w", err)
	}
	return rewritten, true, nil
}

func rewriteMultipartRequestModel(
	body []byte,
	contentType string,
	model string,
) ([]byte, string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", fmt.Errorf("parse multipart content type: %w", err)
	}
	if mediaType != "multipart/form-data" {
		return body, contentType, nil
	}
	if params["boundary"] == "" {
		return nil, "", fmt.Errorf("multipart boundary is required")
	}

	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var rewritten bytes.Buffer
	writer := multipart.NewWriter(&rewritten)

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("read multipart body: %w", err)
		}

		partBody, err := io.ReadAll(part)
		if err != nil {
			return nil, "", fmt.Errorf("read multipart part %q: %w", part.FormName(), err)
		}

		switch part.FormName() {
		case "model":
			partBody = []byte(model)
		case "llm":
			nestedBody, changed, err := rewriteJSONModel(partBody, model)
			if err != nil {
				return nil, "", err
			}
			if changed {
				partBody = nestedBody
			}
		}

		dstPart, err := writer.CreatePart(clonePartHeader(part.Header))
		if err != nil {
			return nil, "", fmt.Errorf("create multipart part %q: %w", part.FormName(), err)
		}
		if _, err := dstPart.Write(partBody); err != nil {
			return nil, "", fmt.Errorf("write multipart part %q: %w", part.FormName(), err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart writer: %w", err)
	}

	return rewritten.Bytes(), writer.FormDataContentType(), nil
}

func clonePartHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	if src == nil {
		return nil
	}

	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}
