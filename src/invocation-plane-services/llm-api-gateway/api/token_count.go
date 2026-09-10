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
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strings"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
)

const (
	tokensPerMessage = 3
	tokensPerName    = 1
	tokensPerRole    = 2

	defaultTokensPerImage      = 256
	maxFallbackTokensPerImage  = 16384
	fallbackBaseTokensPerImage = 85
	fallbackPixelsPerToken     = 28 * 28
	maxImageHeaderBytes        = 512 << 10
)

type imageTokenEstimator func(
	width, height int,
	detail string,
	params imageTokenEstimatorParams,
) int

type imageTokenEstimatorParams struct {
	patchSize             int
	pixelsPerToken        int
	minTokens             int
	maxTokens             int
	maxDimension          int
	fixedTokens           int
	baseTokens            int
	tokensPerTile         int
	tileSize              int
	shortestSide          int
	maxTiles              int
	cropTokens            int
	multiplierNumerator   int
	multiplierDenominator int
}

type imageTokenEstimatorSpec struct {
	estimator imageTokenEstimator
	params    imageTokenEstimatorParams
}

var fallbackImageTokenEstimator = imageTokenEstimatorSpec{
	estimator: estimatedFallbackImageTokens,
	params: imageTokenEstimatorParams{
		baseTokens:     fallbackBaseTokensPerImage,
		maxTokens:      maxFallbackTokensPerImage,
		pixelsPerToken: fallbackPixelsPerToken,
	},
}

// Each entry is a model-name fragment paired with an estimator and its
// processor parameters. Image-capable models from the popularity-ranked NIM
// catalog are registered when their processors have a distinct equation.
// Text-only, non-chat, and unknown models naturally use the bounded fallback.
//
// The remaining entries preserve common hosted-model families. Several names
// share an equation when their published processors share the same patching or
// tiling architecture; only the parameter set changes.
var imageTokenEstimators = map[string]imageTokenEstimatorSpec{
	// MiniMax M3 merges 14px patches in 2x2 groups. Its 672x672 pixel
	// budget therefore becomes 576 effective 28px tokens.
	"minimax-m3": {
		estimator: estimatedPatchGridImageTokens,
		params: imageTokenEstimatorParams{
			patchSize: 28,
			minTokens: 4,
			maxTokens: 576,
		},
	},
	// Nemotron Omni applies a 2x pixel shuffle to 16px patches. Dividing
	// its 1,024 to 13,312 pre-shuffle patch budget by four gives 256 to
	// 3,328 effective 32px tokens.
	"nemotron-3-nano-omni-30b-a3b-reasoning": {
		estimator: estimatedPatchGridImageTokens,
		params: imageTokenEstimatorParams{
			patchSize: 32,
			minTokens: 256,
			maxTokens: 3328,
		},
	},
	"step-3-7-flash": {
		estimator: estimatedStepImageTokens,
		params: imageTokenEstimatorParams{
			maxDimension: 3024,
			fixedTokens:  169,
			cropTokens:   81,
			tileSize:     504,
		},
	},
	"gemma-4-31b-it": {
		estimator: estimatedFixedImageTokens,
		params:    imageTokenEstimatorParams{fixedTokens: 280},
	},
	// Nemotron Nano VL emits 256 tokens per 512px InternVL tile and adds
	// one thumbnail when the selected aspect-ratio grid has multiple tiles.
	"nemotron-nano-12b-v2-vl": {
		estimator: estimatedAspectRatioTileImageTokens,
		params: imageTokenEstimatorParams{
			tileSize:      512,
			maxTiles:      12,
			tokensPerTile: 256,
		},
	},
	"llama-3-2-90b-vision-instruct": {
		estimator: estimatedMllamaImageTokens,
		params: imageTokenEstimatorParams{
			tileSize:      560,
			maxTiles:      4,
			tokensPerTile: 1601,
		},
	},
	"llama-3-2-11b-vision-instruct": {
		estimator: estimatedMllamaImageTokens,
		params: imageTokenEstimatorParams{
			tileSize:      560,
			maxTiles:      4,
			tokensPerTile: 1601,
		},
	},
	"qwen2-vl":    qwen2ImageTokenEstimator,
	"qwen-2-vl":   qwen2ImageTokenEstimator,
	"qwen2-5-vl":  qwen2ImageTokenEstimator,
	"qwen-2-5-vl": qwen2ImageTokenEstimator,
	"qwen3-vl":    qwen3ImageTokenEstimator,
	"qwen-3-vl":   qwen3ImageTokenEstimator,
	"qwen3-5":     qwen3ImageTokenEstimator,
	"qwen-3-5":    qwen3ImageTokenEstimator,
	"pixtral": {
		estimator: estimatedPatchRowsImageTokens,
		params: imageTokenEstimatorParams{
			patchSize:    16,
			maxDimension: 1024,
		},
	},
	"gemma-3": {
		estimator: estimatedFixedImageTokens,
		params:    imageTokenEstimatorParams{fixedTokens: 256},
	},
	"gemma3": {
		estimator: estimatedFixedImageTokens,
		params:    imageTokenEstimatorParams{fixedTokens: 256},
	},
	"claude-opus-4-7": highResolutionClaudeImageTokenEstimator,
	"claude-opus-4-8": highResolutionClaudeImageTokenEstimator,
	"claude-sonnet-5": highResolutionClaudeImageTokenEstimator,
	"claude-fable-5":  highResolutionClaudeImageTokenEstimator,
	"claude-mythos-5": highResolutionClaudeImageTokenEstimator,
	"claude":          standardClaudeImageTokenEstimator,
	"gpt-4o-mini": {
		estimator: estimatedTiledImageTokens,
		params: imageTokenEstimatorParams{
			baseTokens:    2833,
			tokensPerTile: 5667,
			tileSize:      512,
			maxDimension:  2048,
			shortestSide:  768,
		},
	},
	"gpt-5-1": {
		estimator: estimatedTiledImageTokens,
		params: imageTokenEstimatorParams{
			baseTokens:    70,
			tokensPerTile: 140,
			tileSize:      512,
			maxDimension:  2048,
			shortestSide:  768,
		},
	},
	"gpt-4-1-mini": {
		estimator: estimatedMultipliedPatchImageTokens,
		params: imageTokenEstimatorParams{
			patchSize:             32,
			maxDimension:          2048,
			maxTokens:             6144,
			multiplierNumerator:   162,
			multiplierDenominator: 100,
		},
	},
	"gpt-4-1-nano": {
		estimator: estimatedMultipliedPatchImageTokens,
		params: imageTokenEstimatorParams{
			patchSize:             32,
			maxDimension:          2048,
			maxTokens:             6144,
			multiplierNumerator:   246,
			multiplierDenominator: 100,
		},
	},
	"gpt-4-1": standardOpenAITileImageTokenEstimator,
	"gpt-4o":  standardOpenAITileImageTokenEstimator,
}

var qwen2ImageTokenEstimator = imageTokenEstimatorSpec{
	// Qwen2 and Qwen2.5 merge 14px patches in 2x2 groups, producing one
	// token per effective 28px patch within the processor's bounded range.
	estimator: estimatedPatchGridImageTokens,
	params: imageTokenEstimatorParams{
		patchSize: 28,
		minTokens: 4,
		maxTokens: 16384,
	},
}

var qwen3ImageTokenEstimator = imageTokenEstimatorSpec{
	// Qwen3 uses 16px patches with the same 2x2 spatial merge, so its
	// effective patch is 32px while retaining a bounded token budget.
	estimator: estimatedPatchGridImageTokens,
	params: imageTokenEstimatorParams{
		patchSize: 32,
		minTokens: 4,
		maxTokens: 16384,
	},
}

var standardClaudeImageTokenEstimator = imageTokenEstimatorSpec{
	// Standard-resolution Claude models use one token per 28px patch after
	// fitting within a 1,568px edge and a 1,568-token budget.
	estimator: estimatedClaudeImageTokens,
	params: imageTokenEstimatorParams{
		patchSize:    28,
		maxDimension: 1568,
		maxTokens:    1568,
	},
}

var highResolutionClaudeImageTokenEstimator = imageTokenEstimatorSpec{
	// High-resolution Claude models raise the edge and token limits while
	// retaining the same 28px patch equation.
	estimator: estimatedClaudeImageTokens,
	params: imageTokenEstimatorParams{
		patchSize:    28,
		maxDimension: 2576,
		maxTokens:    4784,
	},
}

var standardOpenAITileImageTokenEstimator = imageTokenEstimatorSpec{
	estimator: estimatedTiledImageTokens,
	params: imageTokenEstimatorParams{
		baseTokens:    85,
		tokensPerTile: 170,
		tileSize:      512,
		maxDimension:  2048,
		shortestSide:  768,
	},
}

var tokenExtraForModel = map[string]int{
	"llama2-70b-4096":    15,
	"mixtral-8x7b-32768": 1,
	"gemma-7b-it":        2,
}

func estimatedTokenCountForRequest(model string, request *models.ChatCompletionRequest) int {
	if request == nil {
		return 0
	}

	totalTokens := tokenExtraForModel[model]
	if request.Messages != nil {
		totalTokens += estimatedTokenCountForMessages(model, *request.Messages)
	}

	totalTokens += estimatedTokenCountForValue(request.Tools)
	totalTokens += estimatedTokenCountForValue(request.Functions)
	totalTokens += estimatedTokenCountForToolChoice(request.ToolChoice)
	totalTokens += estimatedTokenCountForFunctionChoice(request.FunctionChoice)
	totalTokens += estimatedTokenCountForValue(request.ResponseFormat)
	totalTokens += estimatedTokenCountForStop(request.Stop)
	totalTokens += estimatedTokenCountForStringPtr(request.User)
	totalTokens += estimatedTokenCountForStringPtr(request.ReasoningFormat)
	totalTokens += estimatedTokenCountForStringPtr(request.ReasoningEffort)
	totalTokens += estimatedTokenCountForValue(request.Metadata)

	return totalTokens
}

func estimatedTokenCountForMessages(model string, messages []models.ChatMessage) int {
	totalTokens := 0
	for _, message := range messages {
		totalTokens += tokensPerMessage
		totalTokens += tokensPerRole

		totalTokens += estimatedTokenCountForMessageContent(model, message.Content)
		if message.Name != nil {
			totalTokens += estimatedTokenCountForStringPtr(message.Name)
			totalTokens += tokensPerName
		}
		totalTokens += estimatedTokenCountForStringPtr(message.ToolCallID)
		totalTokens += estimatedTokenCountForStringPtr(message.Reasoning)
		totalTokens += estimatedTokenCountForText(message.Channel)
		totalTokens += estimatedTokenCountForValue(message.ToolCalls)
		totalTokens += estimatedTokenCountForValue(message.FunctionCall)
	}

	return totalTokens
}

func estimatedTokenCountForMessageContent(model string, content models.ChatMessageContent) int {
	totalTokens := 0
	for _, part := range content {
		switch typed := part.(type) {
		case models.ContentPartText:
			totalTokens += len(typed.String()) / 4
		case *models.ContentPartImageURL:
			totalTokens += estimatedTokenCountForImage(model, typed)
		case models.ContentPartImageURL:
			totalTokens += estimatedTokenCountForImage(model, &typed)
		default:
			totalTokens += estimatedTokenCountForValue(part)
		}
	}

	return totalTokens
}

func estimatedTokenCountForImage(model string, part *models.ContentPartImageURL) int {
	spec := imageTokenEstimatorForModel(model)
	detail := ""
	if part != nil {
		detail = part.Detail
	}

	width, height, ok := imageDimensions(part)
	if !ok {
		return estimatedTokenCountWithoutImageDimensions(detail, spec)
	}
	return spec.estimator(width, height, detail, spec.params)
}

func estimatedTokenCountForImageDimensions(model string, width, height int, detail string) int {
	if width <= 0 || height <= 0 {
		return defaultTokensPerImage
	}

	spec := imageTokenEstimatorForModel(model)
	return spec.estimator(width, height, detail, spec.params)
}

func canonicalImageModelName(model string) string {
	if separator := strings.LastIndexByte(model, '/'); separator >= 0 {
		model = model[separator+1:]
	}
	model = strings.ToLower(model)
	model = strings.ReplaceAll(model, "_", "-")
	return strings.ReplaceAll(model, ".", "-")
}

func imageTokenEstimatorForModel(model string) imageTokenEstimatorSpec {
	model = canonicalImageModelName(model)
	bestMatch := ""
	bestSpec := fallbackImageTokenEstimator
	for nameFragment, spec := range imageTokenEstimators {
		matchesName := model == nameFragment || strings.HasPrefix(model, nameFragment+"-")
		if !matchesName || len(nameFragment) <= len(bestMatch) {
			continue
		}
		bestMatch = nameFragment
		bestSpec = spec
	}
	return bestSpec
}

func estimatedTokenCountWithoutImageDimensions(
	detail string,
	spec imageTokenEstimatorSpec,
) int {
	// Square and extreme-aspect inputs exercise the current estimators' maximum
	// patch or tile budgets. Fixed and low-detail estimators ignore dimensions.
	const largeDimension = 1 << 24
	params := spec.params
	return max(
		spec.estimator(largeDimension, largeDimension, detail, params),
		spec.estimator(largeDimension, 1, detail, params),
		spec.estimator(1, largeDimension, detail, params),
	)
}

// Patch-grid processors emit approximately one language-model token for each
// effective patch. The registry supplies the effective patch size after any
// spatial merge and the processor's minimum and maximum token budgets. This
// covers Qwen, MiniMax M3, Claude, and Nemotron Omni without decoding pixels.
func estimatedPatchGridImageTokens(
	width, height int,
	_ string,
	params imageTokenEstimatorParams,
) int {
	widthInPatches := ceilDiv(width, params.patchSize)
	heightInPatches := ceilDiv(height, params.patchSize)
	tokens := cappedProduct(widthInPatches, heightInPatches, params.maxTokens)
	return max(params.minTokens, tokens)
}

func estimatedClaudeImageTokens(
	width, height int,
	detail string,
	params imageTokenEstimatorParams,
) int {
	width, height = fitWithinMaxDimension(width, height, params.maxDimension)
	return estimatedPatchGridImageTokens(width, height, detail, params)
}

// Pixtral emits one token for each 16x16 patch plus one separator for each
// patch row after resizing the longest edge.
func estimatedPatchRowsImageTokens(
	width, height int,
	_ string,
	params imageTokenEstimatorParams,
) int {
	width, height = fitWithinMaxDimension(width, height, params.maxDimension)
	widthInPatches := ceilDiv(width, params.patchSize)
	heightInPatches := ceilDiv(height, params.patchSize)
	return (widthInPatches + 1) * heightInPatches
}

// Fixed-budget processors normalize every image to a configured number of
// soft tokens. Gemma 3 uses 256. The NIM catalog's Gemma 4 configuration uses
// an aspect-preserving budget of approximately 280 tokens.
func estimatedFixedImageTokens(
	_, _ int,
	_ string,
	params imageTokenEstimatorParams,
) int {
	return params.fixedTokens
}

// OpenAI high/auto detail processors first constrain image dimensions and
// then charge a model-specific base plus a cost for each 512px tile. Low detail
// only uses the base cost.
func estimatedTiledImageTokens(
	width, height int,
	detail string,
	params imageTokenEstimatorParams,
) int {
	if strings.EqualFold(detail, "low") {
		return params.baseTokens
	}

	width, height = fitWithinMaxDimension(width, height, params.maxDimension)
	width, height = fitShortestSideWithin(width, height, params.shortestSide)
	tiles := ceilDiv(width, params.tileSize) * ceilDiv(height, params.tileSize)
	return params.baseTokens + tiles*params.tokensPerTile
}

// GPT-4.1 Mini and Nano cover the image with 32x32 patches after fitting it
// within 2048px and apply a model-specific multiplier. Capping the patch count
// directly is a fast conservative approximation of the published proportional
// resize step when an image would exceed the patch budget.
func estimatedMultipliedPatchImageTokens(
	width, height int,
	_ string,
	params imageTokenEstimatorParams,
) int {
	width, height = fitWithinMaxDimension(width, height, params.maxDimension)
	patches := cappedProduct(
		ceilDiv(width, params.patchSize),
		ceilDiv(height, params.patchSize),
		params.maxTokens,
	)
	return ceilDiv(
		patches*params.multiplierNumerator,
		params.multiplierDenominator,
	)
}

// Step 3.7 Flash always emits a 169-token global view. Images larger than the
// global view, or narrow panoramas, add 81-token non-overlapping crops plus two
// boundary tokens per crop and a separator between crop rows. These constants
// and the 3,024px input cap come directly from its processor configuration.
func estimatedStepImageTokens(
	width, height int,
	_ string,
	params imageTokenEstimatorParams,
) int {
	width, height = fitWithinMaxDimension(width, height, params.maxDimension)
	longSide, shortSide := max(width, height), min(width, height)
	if shortSide < 32 && float64(longSide)/float64(shortSide) > 4 {
		width, height = longSide, longSide
		shortSide = longSide
	}
	windowSize := 0
	if longSide <= 728 {
		if float64(longSide)/float64(shortSide) > 1.5 {
			windowSize = shortSide
		}
	} else if float64(longSide)/float64(shortSide) > 4 {
		windowSize = min(shortSide, params.tileSize)
	} else {
		windowSize = params.tileSize
	}

	if windowSize == 0 {
		return params.fixedTokens + 2
	}

	width = stepCropDimension(width, windowSize)
	height = stepCropDimension(height, windowSize)
	columns := ceilDiv(width, windowSize)
	rows := ceilDiv(height, windowSize)
	crops := columns * rows
	return params.fixedTokens + 2 + crops*(params.cropTokens+2) + max(0, rows-1)
}

func stepCropDimension(dimension, windowSize int) int {
	ratio := float64(dimension) / float64(windowSize)
	if ratio < 1 {
		return dimension
	}
	wholeWindows := dimension / windowSize
	if ratio-float64(wholeWindows) > 0.2 {
		wholeWindows++
	}
	return windowSize * wholeWindows
}

// InternVL-style processors choose a grid of fixed-size tiles that best fits
// the source aspect ratio. Larger images can win ties in favor of more tiles,
// and Nemotron Nano VL adds a thumbnail whenever it uses multiple detail tiles.
func estimatedAspectRatioTileImageTokens(
	width, height int,
	_ string,
	params imageTokenEstimatorParams,
) int {
	tiles := aspectRatioTileCount(width, height, params.tileSize, params.maxTiles)
	if tiles > 1 {
		tiles++
	}
	return tiles * params.tokensPerTile
}

func aspectRatioTileCount(width, height, tileSize, maxTiles int) int {
	imageRatio := float64(width) / float64(height)
	imageArea := float64(width) * float64(height)
	bestDifference := math.Inf(1)
	bestTiles := 1
	for tiles := 1; tiles <= maxTiles; tiles++ {
		for columns := 1; columns <= tiles; columns++ {
			if tiles%columns != 0 {
				continue
			}
			rows := tiles / columns
			difference := math.Abs(imageRatio - float64(columns)/float64(rows))
			if difference < bestDifference {
				bestDifference = difference
				bestTiles = tiles
				continue
			}
			if difference == bestDifference &&
				imageArea > float64(tileSize*tileSize*tiles)/2 {
				bestTiles = tiles
			}
		}
	}
	return bestTiles
}

// Llama 3.2 Vision selects the smallest upscaling canvas, or the largest
// downscaling canvas when no supported canvas is large enough. Each selected
// 560px tile yields 1,600 patch features plus one class feature.
func estimatedMllamaImageTokens(
	width, height int,
	_ string,
	params imageTokenEstimatorParams,
) int {
	return mllamaTileCount(width, height, params.tileSize, params.maxTiles) *
		params.tokensPerTile
}

func mllamaTileCount(width, height, tileSize, maxTiles int) int {
	bestTiles := 1
	bestScale := -1.0
	bestArea := 0
	hasUpscalingCanvas := false
	for columns := 1; columns <= maxTiles; columns++ {
		for rows := 1; rows <= maxTiles/columns; rows++ {
			tiles := columns * rows
			scale := min(
				float64(columns*tileSize)/float64(width),
				float64(rows*tileSize)/float64(height),
			)
			area := tiles * tileSize * tileSize
			isUpscalingCanvas := scale >= 1
			switch {
			case isUpscalingCanvas && (!hasUpscalingCanvas || scale < bestScale ||
				(scale == bestScale && area < bestArea)):
				hasUpscalingCanvas = true
				bestScale = scale
				bestArea = area
				bestTiles = tiles
			case !isUpscalingCanvas && !hasUpscalingCanvas &&
				(scale > bestScale || scale == bestScale && area < bestArea):
				bestScale = scale
				bestArea = area
				bestTiles = tiles
			}
		}
	}
	return bestTiles
}

// Unknown models use a stable middle-ground estimate: 85 framing tokens plus
// one token per 28x28 pixels. The cap handles malformed dimensions and models
// that resize very large images before encoding them.
func estimatedFallbackImageTokens(
	width, height int,
	_ string,
	params imageTokenEstimatorParams,
) int {
	maxImagePixels := (params.maxTokens - params.baseTokens) * params.pixelsPerToken
	if height > maxImagePixels || width > maxImagePixels/height {
		return params.maxTokens
	}

	pixels := width * height
	variableTokens := ceilDiv(pixels, params.pixelsPerToken)
	return params.baseTokens + variableTokens
}

func ceilDiv(value, divisor int) int {
	return 1 + (value-1)/divisor
}

func cappedProduct(left, right, cap int) int {
	if left > cap/right {
		return cap
	}
	return min(left*right, cap)
}

func fitWithinMaxDimension(width, height, maxDimension int) (int, int) {
	if width <= maxDimension && height <= maxDimension {
		return width, height
	}
	if width >= height {
		height = max(1, int(int64(height)*int64(maxDimension)/int64(width)))
		return maxDimension, height
	}
	width = max(1, int(int64(width)*int64(maxDimension)/int64(height)))
	return width, maxDimension
}

func fitShortestSideWithin(width, height, maxShortestSide int) (int, int) {
	if min(width, height) <= maxShortestSide {
		return width, height
	}
	if width <= height {
		height = max(1, int(int64(height)*int64(maxShortestSide)/int64(width)))
		return maxShortestSide, height
	}
	width = max(1, int(int64(width)*int64(maxShortestSide)/int64(height)))
	return width, maxShortestSide
}

func imageDimensions(part *models.ContentPartImageURL) (int, int, bool) {
	reader, ok := imageDataReader(part)
	if !ok {
		return 0, 0, false
	}

	buffered := bufio.NewReader(io.LimitReader(reader, maxImageHeaderBytes))
	header, _ := buffered.Peek(30)
	if width, height, ok := webPDimensions(header); ok {
		return width, height, true
	}

	config, _, err := image.DecodeConfig(buffered)
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, false
	}

	return config.Width, config.Height, true
}

func imageDataReader(part *models.ContentPartImageURL) (io.Reader, bool) {
	if part == nil {
		return nil, false
	}
	metadata, payload, isDataURL := strings.Cut(part.URL, ",")
	if isDataURL {
		metadata = strings.ToLower(metadata)
		if !strings.HasPrefix(metadata, "data:image/") ||
			!strings.Contains(metadata, ";base64") {
			return nil, false
		}
		return base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload)), true
	}

	if part.URL == "" || strings.Contains(part.URL, "://") ||
		strings.HasPrefix(strings.ToLower(part.URL), "data:") {
		return nil, false
	}
	return base64.NewDecoder(base64.StdEncoding, strings.NewReader(part.URL)), true
}

func webPDimensions(header []byte) (int, int, bool) {
	if len(header) < 25 || string(header[:4]) != "RIFF" || string(header[8:12]) != "WEBP" {
		return 0, 0, false
	}

	switch string(header[12:16]) {
	case "VP8X":
		if len(header) < 30 {
			return 0, 0, false
		}
		width := 1 + int(header[24]) + int(header[25])<<8 + int(header[26])<<16
		height := 1 + int(header[27]) + int(header[28])<<8 + int(header[29])<<16
		return width, height, true
	case "VP8L":
		if header[20] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(header[21:25])
		width := 1 + int(bits&0x3fff)
		height := 1 + int(bits>>14&0x3fff)
		return width, height, true
	case "VP8 ":
		if len(header) < 30 || !bytes.Equal(header[23:26], []byte{0x9d, 0x01, 0x2a}) {
			return 0, 0, false
		}
		width := int(binary.LittleEndian.Uint16(header[26:28]) & 0x3fff)
		height := int(binary.LittleEndian.Uint16(header[28:30]) & 0x3fff)
		return width, height, width > 0 && height > 0
	default:
		return 0, 0, false
	}
}

func estimatedTokenCountForToolChoice(choice models.ChatCompletionToolChoiceField) int {
	totalTokens := estimatedTokenCountForStringPtr(choice.String)
	totalTokens += estimatedTokenCountForValue(choice.ToolChoice)
	return totalTokens
}

func estimatedTokenCountForFunctionChoice(choice models.ChatCompletionFunctionChoiceField) int {
	totalTokens := estimatedTokenCountForStringPtr(choice.String)
	totalTokens += estimatedTokenCountForValue(choice.FunctionCall)
	return totalTokens
}

func estimatedTokenCountForStop(stop models.ChatCompletionStopField) int {
	totalTokens := 0
	for _, value := range stop {
		totalTokens += estimatedTokenCountForText(value)
	}
	return totalTokens
}

func estimatedTokenCountForStringPtr(value *string) int {
	if value == nil {
		return 0
	}
	return estimatedTokenCountForText(*value)
}

func estimatedTokenCountForValue(value any) int {
	if value == nil {
		return 0
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return estimatedTokenCountForText(fmt.Sprint(value))
	}
	if string(raw) == "null" {
		return 0
	}
	return estimatedTokenCountForText(string(raw))
}

func estimatedInputTokensForNormalizedRequest(
	model string,
	request *models.ChatCompletionRequest,
) int {
	return max(0, estimatedTokenCountForRequest(model, request))
}

func estimatedTokenCountForText(text string) int {
	if text == "" {
		return 0
	}

	return 5 + int(math.Ceil(float64(len(text))/4.0))
}

func maxOutputTokensForRequest(request *models.ChatCompletionRequest) int {
	if request == nil {
		return 0
	}
	if request.MaxCompletionTokens != nil {
		return int(*request.MaxCompletionTokens)
	}
	if request.MaxTokens != nil {
		return int(*request.MaxTokens)
	}
	return 1
}
