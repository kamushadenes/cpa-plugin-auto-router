package snippet

import (
	"strings"

	"github.com/tidwall/gjson"
)

type Signals struct {
	Tools             int    `json:"tools"`
	Images            int    `json:"images"`
	Messages          int    `json:"messages"`
	Format            string `json:"format"`
	HasNewUserMessage bool   `json:"-"`
}

// Extract returns the last user text, bounded by Unicode characters, and local
// request counters. It never includes system prompts, tool results, or images.
func Extract(format string, body []byte, max int) (string, Signals) {
	sig := Signals{Format: format}
	root := gjson.ParseBytes(body)
	sig.Tools = arrayLen(root.Get("tools"))

	var text string
	var lastIsUserText bool
	if messages := root.Get("messages"); messages.IsArray() {
		items := messages.Array()
		sig.Messages = len(items)
		for _, item := range items {
			textForItem := userText(item)
			if textForItem != "" {
				text = textForItem
			}
			sig.Images += imageCount(item.Get("content"))
		}
		if len(items) > 0 {
			lastIsUserText = userText(items[len(items)-1]) != ""
		}
	} else if input := root.Get("input"); input.Type == gjson.String {
		sig.Messages = 1
		text = strings.TrimSpace(input.String())
		lastIsUserText = text != ""
	} else if input.IsArray() {
		items := input.Array()
		sig.Messages = len(items)
		for _, item := range items {
			textForItem := userText(item)
			if textForItem != "" {
				text = textForItem
			}
			sig.Images += imageCount(item.Get("content"))
			if item.Get("type").String() == "input_image" || item.Get("type").String() == "image" {
				sig.Images++
			}
		}
		if len(items) > 0 {
			lastIsUserText = userText(items[len(items)-1]) != ""
		}
	}
	if max <= 0 {
		return "", sig
	}
	text = tailRunes(text, max)
	sig.HasNewUserMessage = lastIsUserText && text != ""
	return text, sig
}

func arrayLen(value gjson.Result) int {
	if !value.IsArray() {
		return 0
	}
	return len(value.Array())
}

func userText(item gjson.Result) string {
	if item.Get("role").String() != "user" {
		return ""
	}
	return strings.TrimSpace(contentText(item.Get("content")))
}

func contentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	parts := make([]string, 0)
	for _, part := range content.Array() {
		typeName := part.Get("type").String()
		if typeName != "" && typeName != "text" && typeName != "input_text" {
			continue
		}
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func imageCount(content gjson.Result) int {
	if !content.IsArray() {
		return 0
	}
	count := 0
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "image_url", "input_image", "image":
			count++
		}
	}
	return count
}

func tailRunes(text string, max int) string {
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[len(runes)-max:])
}
