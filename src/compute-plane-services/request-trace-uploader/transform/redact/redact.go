// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package redact replaces multimodal media in request trace records with
// markers, so images, audio, video and files never leave the pod.
//
// It is an exact port of the earlier Python uploader's redaction (version 2):
// the same media detection, marker object, inline token, MIME sniffing, and
// part count, so redacted rows keep their shape. Media is found three ways:
//
//  1. Media content parts keep their type and get a marker in place of their
//     media field; every other field of the part is dropped.
//  2. Fields known to hold raw media (images, b64_json, multi_modal_data,
//     inline audio data and similar) are replaced by a marker.
//  3. In any other string, each media data URL and each long base64 run is
//     replaced. A string that is only that media becomes the marker;
//     otherwise only the media text becomes an inline token.
//
// Lengths, thresholds and the marker's "bytes" count Unicode code points, as
// Python's len() does, and "sha256" hashes the media text's UTF-8 bytes.
package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform/jsonvalue"
)

// Version identifies this redaction algorithm. A transport prepared with a
// different version must be rebuilt from its source.
const Version = "v2"

func set(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

var (
	multimodalPartTypes = set(
		"image_url", "input_audio", "file", "video_url", "audio_url",
		"image_embeds", "audio_embeds", "image_pil",
		"input_image", "input_file", "input_video",
		"image_file",
		"image", "audio", "video", "document", "container_upload",
		"pdf", "input_pdf", "image_base64",
	)
	// textCapablePartTypes may carry text rather than media.
	textCapablePartTypes = set("document")
	// textSourceTypes mark a source that holds text, not media.
	textSourceTypes = set("text", "content")
	// untypedMediaKeys hold media in a list item with no "type".
	untypedMediaKeys = []string{"image_url", "audio_url", "video_url", "input_audio", "image_embeds", "audio_embeds"}
	// mediaFieldOrder is where media lives inside a typed part, after the
	// part's own type.
	mediaFieldOrder = []string{
		"image_url", "video_url", "audio_url", "input_audio", "image", "audio", "video", "file", "pdf",
		"image_base64", "image_file", "file_data", "file_url", "file_id", "source", "data", "url",
		"b64_json", "base64",
	}
	// mediaStringKeys hold the content, or a reference to it, inside a
	// media value.
	mediaStringKeys    = []string{"url", "data", "file_data", "file_url", "file_id", "file_uri", "fileUri", "image_url", "b64_json", "base64"}
	mimeHintKeys       = []string{"media_type", "mime_type", "mimeType", "mime"}
	rawMediaStringKeys = set("b64_json", "partial_image_b64", "image_base64")
	rawMediaListKeys   = set("images")
	rawMediaMapKeys    = set("multi_modal_data", "mm_data")
	rawMediaObjectKeys = set("audio", "inline_data", "inlineData", "file_data", "fileData")

	mediaMIMEMajors            = set("image", "audio", "video", "font", "model")
	textualApplicationSubtypes = set("json", "xml", "javascript", "ecmascript", "x-javascript", "x-www-form-urlencoded", "sql", "yaml", "x-yaml", "toml")
)

const (
	// base64RunMinChars is the shortest base64 run inside other text that
	// counts as media, about 3 KiB decoded.
	base64RunMinChars = 4096
	// base64StringMinChars is the shortest whole string of base64 that
	// counts as media.
	base64StringMinChars = 1024
	// rawMediaMinChars is the shortest base64 treated as media in a field
	// that holds media.
	rawMediaMinChars = 64
)

var magicMIME = []struct {
	offset int
	magic  string
	mime   string
}{
	{0, "\x89PNG", "image/png"},
	{0, "\xff\xd8\xff", "image/jpeg"},
	{0, "GIF8", "image/gif"},
	{0, "%PDF", "application/pdf"},
	{8, "WEBP", "image/webp"},
	{8, "WAVE", "audio/wav"},
	{8, "AVI ", "video/x-msvideo"},
	{0, "ID3", "audio/mpeg"},
	{0, "fLaC", "audio/flac"},
	{0, "OggS", "audio/ogg"},
	{0, "\x1aE\xdf\xa3", "video/webm"},
	{4, "ftyp", "video/mp4"},
	{0, "PK\x03\x04", "application/zip"},
}

// mimeType is a MIME type that may be absent; absent is written as null.
type mimeType struct {
	value string
	set   bool
}

func someMIME(value string) mimeType { return mimeType{value: value, set: true} }

// marker describes one redacted media value.
type marker struct {
	kind   string
	mime   mimeType
	bytes  int
	sha256 string
}

func (m marker) value() *jsonvalue.Value {
	out := jsonvalue.NewObject()
	out.Set("redacted", jsonvalue.NewString("multimodal"))
	out.Set("kind", jsonvalue.NewString(m.kind))
	if m.mime.set {
		out.Set("mime", jsonvalue.NewString(m.mime.value))
	} else {
		out.Set("mime", jsonvalue.NewNull())
	}
	out.Set("bytes", jsonvalue.NewInt(int64(m.bytes)))
	out.Set("sha256", jsonvalue.NewString(m.sha256))
	return out
}

func (m marker) token() string {
	mime := "null"
	if m.mime.set {
		mime = m.mime.value
	}
	return "[redacted:multimodal kind=" + m.kind + " mime=" + mime + " bytes=" + strconv.Itoa(m.bytes) + " sha256=" + m.sha256 + "]"
}

func newMarker(text, kind string, mime mimeType) marker {
	switch kind {
	case "data_url":
		if own, ok := dataURLMIME(text); ok {
			mime = someMIME(own)
		}
	case "inline":
		if !mime.set {
			if sniffed, ok := sniffMIME(text); ok {
				mime = someMIME(sniffed)
			}
		}
	}
	sum := sha256.Sum256([]byte(text))
	return marker{kind: kind, mime: mime, bytes: jsonvalue.CodePoints(text), sha256: hex.EncodeToString(sum[:])}
}

func stringMarker(text, field string, mime mimeType) marker {
	return newMarker(text, mediaKind(text, field), mime)
}

// valueMarker marks any value. A value that is not a string, such as an
// embedding list, is hashed as its canonical JSON.
func valueMarker(v *jsonvalue.Value, field string, mime mimeType) marker {
	if text, ok := v.String(); ok {
		return stringMarker(text, field, mime)
	}
	return newMarker(string(jsonvalue.CanonicalSorted(v)), "inline", mime)
}

// Record redacts media anywhere in a decoded record and returns the record,
// possibly replaced when it is a bare string, with the number of media parts
// replaced. Containers are changed in place. When anything was replaced and
// the record is an object, the count is stored as multimodal_redacted_parts
// on the payload, or on the event when there is no payload object.
func Record(record *jsonvalue.Value) (*jsonvalue.Value, int) {
	wasObject := record.Kind() == jsonvalue.Object
	root, count := redactTree(record)
	if count > 0 && wasObject {
		event := root
		if root.Has("event") {
			event = root.Get("event")
		}
		if event.Kind() != jsonvalue.Object {
			event = root
		}
		container := event
		if payload := event.Get("payload"); payload != nil && payload.Kind() == jsonvalue.Object {
			container = payload
		}
		container.Set("multimodal_redacted_parts", jsonvalue.NewInt(int64(count)))
	}
	return root, count
}

func redactTree(root *jsonvalue.Value) (*jsonvalue.Value, int) {
	if text, ok := root.String(); ok {
		if replaced, count := redactString(text); count > 0 {
			return replaced, count
		}
		return root, 0
	}
	type item struct {
		node   *jsonvalue.Value
		inList bool
	}
	count := 0
	stack := []item{{node: root}}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node := top.node
		switch node.Kind() {
		case jsonvalue.Object:
			if redactMediaPart(node, top.inList) {
				count++
				continue
			}
			members := node.Members()
			for i := range members {
				key, value := members[i].Key, members[i].Value
				switch {
				case rawMediaStringKeys[key] || rawMediaListKeys[key] || rawMediaMapKeys[key] || rawMediaObjectKeys[key]:
					count += redactRawMediaField(node, key, value)
					value = node.Get(key)
				case key == "result" && isString(node.Get("type"), "image_generation_call"):
					// The Responses API image_generation_call.result is raw base64.
					if m, ok := rawMediaMarker(value, key, mimeType{}); ok {
						node.Set(key, m.value())
						count++
						continue
					}
				}
				if text, ok := value.String(); ok {
					if replaced, found := redactString(text); found > 0 {
						node.Set(key, replaced)
						count += found
					}
				} else if kind := value.Kind(); kind == jsonvalue.Object || kind == jsonvalue.Array {
					stack = append(stack, item{node: value})
				}
			}
		case jsonvalue.Array:
			for i, value := range node.Items() {
				if text, ok := value.String(); ok {
					if replaced, found := redactString(text); found > 0 {
						node.SetItem(i, replaced)
						count += found
					}
				} else if kind := value.Kind(); kind == jsonvalue.Object || kind == jsonvalue.Array {
					// A dict in a list may be a content part.
					stack = append(stack, item{node: value, inList: kind == jsonvalue.Object})
				}
			}
		}
	}
	return root, count
}

func isString(v *jsonvalue.Value, want string) bool {
	text, ok := v.String()
	return ok && text == want
}

// mediaResult is what mediaValueMarker found.
type mediaResult int

const (
	foundMedia mediaResult = iota
	textSource
	noMediaString
)

// redactMediaPart redacts one content part in place and reports whether it
// was media.
func redactMediaPart(part *jsonvalue.Value, inList bool) bool {
	partType, typed := part.Get("type").String()
	var candidates []string
	switch {
	case typed:
		if !multimodalPartTypes[partType] {
			return false
		}
		candidates = append([]string{partType}, mediaFieldOrder...)
	case !part.Has("type") && inList && hasAny(part, untypedMediaKeys):
		candidates = untypedMediaKeys
	default:
		return false
	}
	field := ""
	for _, key := range candidates {
		if v := part.Get(key); v != nil && v.Kind() != jsonvalue.Null {
			field = key
			break
		}
	}
	if field == "" {
		// No media field, for example {"type": "document", "text": "..."}.
		// Its strings still go through the string checks.
		return false
	}
	value := part.Get(field)
	if text, ok := value.String(); ok && !typed && !looksLikeMedia(text) {
		// The untyped shorthand counts only when the value looks like media,
		// so {"image_url": "label"} in a schema example is kept.
		return false
	}
	textCapable := typed && textCapablePartTypes[partType]
	if text, ok := value.String(); ok && textCapable && !looksLikeMedia(text) {
		return false
	}
	partMIME := mimeHint(part, typed && partType == "input_audio")
	m, result := mediaValueMarker(value, field, partMIME)
	switch result {
	case textSource:
		return false
	case noMediaString:
		if textCapable {
			return false
		}
		m = valueMarker(value, field, partMIME)
	}
	part.Clear()
	if typed {
		part.Set("type", jsonvalue.NewString(partType))
	}
	part.Set(field, m.value())
	return true
}

func hasAny(object *jsonvalue.Value, keys []string) bool {
	for _, key := range keys {
		if object.Has(key) {
			return true
		}
	}
	return false
}

// mediaValueMarker finds the media in one media field, looking inside an
// object value when needed. A MIME type stated closer to the media wins.
func mediaValueMarker(value *jsonvalue.Value, field string, mime mimeType) (marker, mediaResult) {
	if value.Kind() != jsonvalue.Object {
		return valueMarker(value, field, mime), foundMedia
	}
	if sourceType, ok := value.Get("type").String(); ok && textSourceTypes[sourceType] {
		return marker{}, textSource
	}
	if hint := mimeHint(value, field == "input_audio"); hint.set {
		mime = hint
	}
	for _, key := range mediaStringKeys {
		if text, ok := value.Get(key).String(); ok {
			return stringMarker(text, key, mime), foundMedia
		}
	}
	for _, key := range mediaStringKeys {
		if nested := value.Get(key); nested != nil && nested.Kind() == jsonvalue.Object {
			if m, result := mediaValueMarker(nested, key, mime); result == foundMedia {
				return m, foundMedia
			}
		}
	}
	return marker{}, noMediaString
}

// mimeHint returns a MIME type stated next to the media, such as media_type.
// For audio, a "format" such as "wav" gives audio/wav.
func mimeHint(container *jsonvalue.Value, audio bool) mimeType {
	for _, key := range mimeHintKeys {
		if text, ok := container.Get(key).String(); ok && text != "" {
			return someMIME(text)
		}
	}
	if audio {
		if format, ok := container.Get("format").String(); ok && format != "" {
			return someMIME("audio/" + pyLower(format))
		}
	}
	return mimeType{}
}

// rawMediaMarker marks a string in a field that holds media, when it looks
// like media.
func rawMediaMarker(value *jsonvalue.Value, field string, mime mimeType) (marker, bool) {
	text, ok := value.String()
	if !ok || !looksLikeMedia(text) {
		return marker{}, false
	}
	return stringMarker(text, field, mime), true
}

// redactRawMediaField redacts a field known to hold raw media and returns the
// part count.
func redactRawMediaField(node *jsonvalue.Value, key string, value *jsonvalue.Value) int {
	count := 0
	switch {
	case rawMediaStringKeys[key]:
		if m, ok := rawMediaMarker(value, key, mimeHint(node, false)); ok {
			node.Set(key, m.value())
			count = 1
		}
	case rawMediaListKeys[key]:
		if value.Kind() == jsonvalue.Array {
			for i, item := range value.Items() {
				if m, ok := rawMediaMarker(item, key, mimeType{}); ok {
					value.SetItem(i, m.value())
					count++
				}
			}
		} else if m, ok := rawMediaMarker(value, key, mimeType{}); ok {
			node.Set(key, m.value())
			count = 1
		}
	case rawMediaMapKeys[key]:
		if value.Kind() != jsonvalue.Object {
			break
		}
		for _, member := range value.Members() {
			modality, item := member.Key, member.Value
			if item.Kind() == jsonvalue.Array {
				for i, entry := range item.Items() {
					if m, ok := rawMediaMarker(entry, modality, mimeType{}); ok {
						item.SetItem(i, m.value())
						count++
					}
				}
			} else if m, ok := rawMediaMarker(item, modality, mimeType{}); ok {
				value.Set(modality, m.value())
				count++
			}
		}
	case rawMediaObjectKeys[key]:
		if value.Kind() != jsonvalue.Object {
			break
		}
		mime := mimeHint(value, key == "audio")
		for _, field := range []string{"data", "file_uri", "fileUri"} {
			if m, ok := rawMediaMarker(value.Get(field), field, mime); ok {
				value.Set(field, m.value())
				count++
			}
		}
	}
	return count
}

// redactString returns the replacement for a string and the number of media
// parts in it. A count of zero means the string is kept as is.
func redactString(text string) (*jsonvalue.Value, int) {
	spans := mediaSpans(text)
	if len(spans) == 0 {
		if isBase64String(text) && len(text) >= base64StringMinChars && hasBase64Classes(text, 0, len(text)) {
			return stringMarker(pyStrip(text), "", mimeType{}).value(), 1
		}
		return nil, 0
	}
	if len(spans) == 1 && pyStrip(text[:spans[0].start]) == "" && pyStrip(text[spans[0].end:]) == "" {
		// The whole string is the media.
		s := spans[0]
		return stringMarker(text[s.start:s.end], "", s.mediaType()).value(), 1
	}
	var b strings.Builder
	position := 0
	for _, s := range spans {
		b.WriteString(text[position:s.start])
		b.WriteString(stringMarker(text[s.start:s.end], "", s.mediaType()).token())
		position = s.end
	}
	b.WriteString(text[position:])
	return jsonvalue.NewString(b.String()), len(spans)
}

// looksLikeMedia reports a data URL, a URL, or base64 long enough to be
// media.
func looksLikeMedia(text string) bool {
	if isDataURL(text) || mediaKind(text, "") == "url" {
		return true
	}
	// A base64 string is ASCII, so its byte length is its length.
	return isBase64String(text) && len(text) >= rawMediaMinChars
}

// mediaKind classifies a media string as data_url, file_id, url or inline.
func mediaKind(text, field string) string {
	if isDataURL(text) {
		return "data_url"
	}
	if field == "file_id" || field == "image_file" {
		return "file_id"
	}
	head := prefixCodePoints(text, 64)
	if scheme, _, found := strings.Cut(head, "://"); found && scheme != "" && isAlnum(strings.NewReplacer("+", "", "-", "", ".", "").Replace(scheme)) {
		return "url"
	}
	return "inline"
}

// sniffMIME matches the start of decoded base64 against known media magic
// bytes.
func sniffMIME(text string) (string, bool) {
	head := prefixCodePoints(pyStrip(prefixCodePoints(text, 4096)), 24)
	head = strings.NewReplacer("-", "+", "_", "/").Replace(head)
	n := jsonvalue.CodePoints(head)
	head = prefixCodePoints(head, n-n%4)
	raw, ok := decodeBase64Strict(head)
	if !ok {
		return "", false
	}
	for _, m := range magicMIME {
		if len(raw) >= m.offset+len(m.magic) && raw[m.offset:m.offset+len(m.magic)] == m.magic {
			return m.mime, true
		}
	}
	return "", false
}

func prefixCodePoints(text string, n int) string {
	i := 0
	for count := 0; count < n && i < len(text); count++ {
		_, size := jsonvalue.DecodeRune(text[i:])
		i += size
	}
	return text[:i]
}
