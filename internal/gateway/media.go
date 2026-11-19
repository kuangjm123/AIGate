package gateway

import (
	"encoding/json"
	"strings"
)

func hasMedia(body []byte) bool {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	return walkMedia(v)
}

func walkMedia(v any) bool {
	switch node := v.(type) {
	case map[string]any:
		if typ, _ := node["type"].(string); mediaType(typ) {
			return true
		}
		if _, ok := node["image_url"]; ok {
			return true
		}
		if mime, _ := node["mimeType"].(string); mediaMIME(mime) {
			return true
		}
		if mime, _ := node["mime_type"].(string); mediaMIME(mime) {
			return true
		}
		for _, child := range node {
			if walkMedia(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if walkMedia(child) {
				return true
			}
		}
	}
	return false
}

func mediaType(typ string) bool {
	switch typ {
	case "image", "image_url", "input_image", "video", "input_video":
		return true
	default:
		return false
	}
}

func mediaMIME(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	return strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/")
}
