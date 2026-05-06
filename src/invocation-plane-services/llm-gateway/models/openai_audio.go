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
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
)

type TranscriptionResponseFormat string

const (
	TranscriptionResponseFormatJSON        TranscriptionResponseFormat = "json"
	TranscriptionResponseFormatText        TranscriptionResponseFormat = "text"
	TranscriptionResponseFormatSRT         TranscriptionResponseFormat = "srt"
	TranscriptionResponseFormatVerboseJSON TranscriptionResponseFormat = "verbose_json"
	TranscriptionResponseFormatVTT         TranscriptionResponseFormat = "vtt"
)

const (
	DefaultAudioTemperature = float32(0)
	DefaultResponseFormat   = TranscriptionResponseFormatJSON
)

var TranscriptionResponseFormats = [...]TranscriptionResponseFormat{
	TranscriptionResponseFormatJSON,
	TranscriptionResponseFormatText,
	TranscriptionResponseFormatVerboseJSON,
}

var (
	SupportedTranscriptionAudioFormats = [...]string{
		AudioFormatFlac,
		AudioFormatMp3,
		AudioFormatMp4,
		AudioFormatMpeg,
		AudioFormatMpga,
		AudioFormatM4a,
		AudioFormatOgg,
		AudioFormatOpus,
		AudioFormatWav,
		AudioFormatWebm,
	}
	SupportedLanguages = map[string]struct{}{
		"af": {}, "am": {}, "ar": {}, "as": {}, "az": {}, "ba": {}, "be": {}, "bg": {}, "bn": {}, "bo": {},
		"br": {}, "bs": {}, "ca": {}, "cs": {}, "cy": {}, "da": {}, "de": {}, "el": {}, "en": {}, "es": {},
		"et": {}, "eu": {}, "fa": {}, "fi": {}, "fo": {}, "fr": {}, "gl": {}, "gu": {}, "ha": {}, "haw": {},
		"he": {}, "hi": {}, "hr": {}, "ht": {}, "hu": {}, "hy": {}, "id": {}, "is": {}, "it": {}, "ja": {},
		"jv": {}, "ka": {}, "kk": {}, "km": {}, "kn": {}, "ko": {}, "la": {}, "lb": {}, "ln": {}, "lo": {},
		"lt": {}, "lv": {}, "mg": {}, "mi": {}, "mk": {}, "ml": {}, "mn": {}, "mr": {}, "ms": {}, "mt": {},
		"my": {}, "ne": {}, "nl": {}, "nn": {}, "no": {}, "oc": {}, "pa": {}, "pl": {}, "ps": {}, "pt": {},
		"ro": {}, "ru": {}, "sa": {}, "sd": {}, "si": {}, "sk": {}, "sl": {}, "sn": {}, "so": {}, "sq": {},
		"sr": {}, "su": {}, "sv": {}, "sw": {}, "ta": {}, "te": {}, "tg": {}, "th": {}, "tk": {}, "tl": {},
		"tr": {}, "tt": {}, "uk": {}, "ur": {}, "uz": {}, "vi": {}, "yue": {}, "yi": {}, "yo": {}, "zh": {},
	}
)

const defaultTranslationLanguage = "en"

const (
	TranscriptionTimestampGranularitiesWord    = "word"
	TranscriptionTimestampGranularitiesSegment = "segment"
)

const (
	TranscriptionRequestFile                   = "file"
	TranscriptionRequestAudioURL               = "url"
	TranscriptionRequestModel                  = "model"
	TranscriptionRequestLanguage               = "language"
	TranscriptionRequestPrompt                 = "prompt"
	TranscriptionRequestResponseFormat         = "response_format"
	TranscriptionRequestTemperature            = "temperature"
	TranscriptionRequestTimestampGranularities = "timestamp_granularities[]"
	TranscriptionRequestDiarize                = "diarize"
)

type AudioToTextBaseParams struct {
	URL            *string
	Model          *string
	Prompt         *string
	Language       *string
	ResponseFormat *TranscriptionResponseFormat
	Temperature    *float32

	file *multipart.FileHeader
}

func (params *AudioToTextBaseParams) GetTemperatureOrDefault() float32 {
	return ptr.DerefOr(params.Temperature, DefaultAudioTemperature)
}

func (params *AudioToTextBaseParams) GetResponseFormatOrDefault() TranscriptionResponseFormat {
	return ptr.DerefOr(params.ResponseFormat, DefaultResponseFormat)
}

func (params *AudioToTextBaseParams) HasFile() bool {
	return params.file != nil
}

func (params *AudioToTextBaseParams) SetFile(f *multipart.FileHeader) {
	params.file = f
}

func (params *AudioToTextBaseParams) FileOpen() (io.ReadSeekCloser, error) {
	if params.file == nil {
		return nil, fmt.Errorf("file is required")
	}
	return params.file.Open()
}

func (params *AudioToTextBaseParams) FileSize() int64 {
	if params.file == nil {
		return 0
	}
	return params.file.Size
}

type TranscriptionParams struct {
	AudioToTextBaseParams

	SegmentTimestamps bool
	WordTimestamps    bool
	Diarize           bool
}

type TranslationParams struct {
	AudioToTextBaseParams
}

func (params *AudioToTextBaseParams) Parse(form *multipart.Form) ([]string, error) {
	var extraValues []string
	for name, values := range form.Value {
		switch name {
		case TranscriptionRequestModel:
			if len(values) > 1 {
				return nil, fmt.Errorf("only one %s is allowed", name)
			}
			model := strings.ToLower(values[0])
			params.Model = &model
		case TranscriptionRequestPrompt:
			if len(values) > 1 {
				return nil, fmt.Errorf("only one %s is allowed", name)
			}
			if len(values[0]) > 896 {
				return nil, fmt.Errorf(
					"prompt length must be 896 characters or fewer, but provided prompt contains %d characters",
					len(values[0]),
				)
			}
			params.Prompt = &values[0]
		case TranscriptionRequestResponseFormat:
			if len(values) > 1 {
				return nil, fmt.Errorf("only one %s is allowed", name)
			}
			responseFormat := TranscriptionResponseFormat(values[0])
			if !slices.Contains(TranscriptionResponseFormats[:], responseFormat) {
				return nil, fmt.Errorf("`response_format` must be one of %v", TranscriptionResponseFormats)
			}
			params.ResponseFormat = &responseFormat
		case TranscriptionRequestTemperature:
			if len(values) > 1 {
				return nil, fmt.Errorf("only one %s is allowed", name)
			}
			temperature, err := strconv.ParseFloat(values[0], 32)
			if err != nil {
				return nil, fmt.Errorf("`temperature` must be a number")
			}
			if temperature < 0 {
				return nil, fmt.Errorf("`temperature` < 0 is not supported")
			}
			if temperature > 1 {
				return nil, fmt.Errorf("`temperature` > 1 is not supported")
			}
			temp := float32(temperature)
			params.Temperature = &temp
		case TranscriptionRequestAudioURL:
			if len(values) > 1 {
				return nil, fmt.Errorf("only one %s is allowed", name)
			}
			params.URL = &values[0]
		default:
			extraValues = append(extraValues, name)
		}
	}

	for name, files := range form.File {
		if name != TranscriptionRequestFile {
			return nil, fmt.Errorf("unknown file `%s`", name)
		}
		if len(files) > 1 {
			return nil, fmt.Errorf("only one file is allowed")
		}
		extension := normalizedFileExtension(files[0].Filename)
		if !slices.Contains(SupportedTranscriptionAudioFormats[:], extension) {
			return nil, fmt.Errorf("file must be one of the following types: %v", SupportedTranscriptionAudioFormats)
		}
		if files[0].Size == 0 {
			return nil, fmt.Errorf("file is empty")
		}
		params.file = files[0]
	}

	if params.Model == nil {
		return nil, fmt.Errorf("`model` is a required property")
	}
	if params.file != nil && params.URL != nil {
		return nil, fmt.Errorf("request cannot contain both a `file` and `url`")
	}
	if params.file == nil && params.URL == nil {
		return nil, fmt.Errorf("`file` or `url` must be provided")
	}
	return extraValues, nil
}

func (params *TranslationParams) Parse(form *multipart.Form) error {
	extraValues, err := params.AudioToTextBaseParams.Parse(form)
	if err != nil {
		return err
	}
	for _, name := range extraValues {
		values := form.Value[name]
		switch name {
		case TranscriptionRequestLanguage:
			if len(values) > 1 {
				return fmt.Errorf("only one %s is allowed", name)
			}
			if values[0] != defaultTranslationLanguage {
				return fmt.Errorf("unsupported language: %s\nLanguage must be %s", values[0], defaultTranslationLanguage)
			}
		case TranscriptionRequestDiarize:
			return fmt.Errorf("`diarize` parameter is not supported for translations")
		default:
			return fmt.Errorf("unknown param `%s`", name)
		}
	}
	params.Language = ptr.To(defaultTranslationLanguage)
	return nil
}

func (params *TranscriptionParams) Parse(form *multipart.Form) error {
	extraValues, err := params.AudioToTextBaseParams.Parse(form)
	if err != nil {
		return err
	}
	for _, name := range extraValues {
		values := form.Value[name]
		switch name {
		case TranscriptionRequestTimestampGranularities:
			if params.GetResponseFormatOrDefault() != TranscriptionResponseFormatVerboseJSON {
				return fmt.Errorf(
					"`%s` must be `%s` if `%s` is provided",
					TranscriptionRequestResponseFormat,
					TranscriptionResponseFormatVerboseJSON,
					TranscriptionRequestTimestampGranularities,
				)
			}
			for _, value := range values {
				switch value {
				case TranscriptionTimestampGranularitiesWord:
					params.WordTimestamps = true
				case TranscriptionTimestampGranularitiesSegment:
					params.SegmentTimestamps = true
				default:
					return fmt.Errorf(
						"`%s` is not a valid value for `timestampGranularities`. Must be either `%s` or `%s`",
						value,
						TranscriptionTimestampGranularitiesWord,
						TranscriptionTimestampGranularitiesSegment,
					)
				}
			}
		case TranscriptionRequestLanguage:
			if len(values) > 1 {
				return fmt.Errorf("only one %s is allowed", name)
			}
			if _, ok := SupportedLanguages[values[0]]; !ok {
				return fmt.Errorf("unsupported language: %s", values[0])
			}
			params.Language = &values[0]
		case TranscriptionRequestDiarize:
			if len(values) > 1 {
				return fmt.Errorf("only one %s is allowed", name)
			}
			switch values[0] {
			case "true":
				params.Diarize = true
			case "false":
				params.Diarize = false
			default:
				return fmt.Errorf("`diarize` must be true or false")
			}
		default:
			return fmt.Errorf("unknown param `%s`", name)
		}
	}
	if params.GetResponseFormatOrDefault() == TranscriptionResponseFormatVerboseJSON &&
		!params.WordTimestamps &&
		!params.SegmentTimestamps {
		params.SegmentTimestamps = true
	}
	return nil
}

func normalizedFileExtension(name string) string {
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if extension == "" {
		return strings.ToLower(name)
	}
	return extension
}
