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

package models

import (
	"unicode/utf8"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
)

const (
	DefaultTTSSpeed          = 1.0
	DefaultTTSResponseFormat = AudioFormatMp3
	DefaultTTSSampleRate     = uint32(48000)
	DefaultTTSTemperature    = 1.0
)

var (
	SupportedTTSResponseFormats = []string{
		AudioFormatFlac,
		AudioFormatMp3,
		AudioFormatMuLaw,
		AudioFormatOgg,
		AudioFormatWav,
	}
	SupportedTTSSampleRates = []uint32{
		8000,
		16000,
		22050,
		24000,
		32000,
		44100,
		48000,
	}
	ContentTypeMap = map[string]string{
		AudioFormatFlac:  "audio/flac",
		AudioFormatMp3:   "audio/mpeg",
		AudioFormatMuLaw: "application/octet-stream",
		AudioFormatOgg:   "audio/ogg",
		AudioFormatWav:   "audio/wav",
	}
)

type TextToSpeechRequest struct {
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	Voice          string   `json:"voice"`
	Speed          *float32 `json:"speed"`
	ResponseFormat *string  `json:"response_format"`
	SampleRate     *uint32  `json:"sample_rate"`
	Temperature    *float32 `json:"-"`
}

func (r *TextToSpeechRequest) GetSpeed() float32 {
	return ptr.DerefOr(r.Speed, DefaultTTSSpeed)
}

func (r *TextToSpeechRequest) GetSampleRate() uint32 {
	return ptr.DerefOr(r.SampleRate, DefaultTTSSampleRate)
}

func (r *TextToSpeechRequest) GetResponseFormat() string {
	return ptr.DerefOr(r.ResponseFormat, DefaultTTSResponseFormat)
}

func (r *TextToSpeechRequest) GetTemperature() float32 {
	return ptr.DerefOr(r.Temperature, DefaultTTSTemperature)
}

func (r *TextToSpeechRequest) InputTokens() int {
	return utf8.RuneCountInString(r.Input)
}
