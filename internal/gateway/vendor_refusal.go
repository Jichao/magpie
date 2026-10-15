package gateway

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// vendorRefusalHost is deliberately an endpoint rule, not a model-family
// rule: GLM on other relays still streams reasoning at once. All APIs
// must name the same official host; Host() alone is only a display host.
func vendorRefusalHost(p provider.Provider) string {
	var host string
	for _, proto := range p.Speaks() {
		u, err := url.Parse(p.Base(proto))
		if err != nil {
			return ""
		}
		h := strings.ToLower(u.Hostname())
		switch h {
		case "token-plan-cn.xiaomimimo.com", "open.bigmodel.cn":
		default:
			return ""
		}
		if host != "" && host != h {
			return ""
		}
		host = h
	}
	return host
}

// Zhipu documents 1301 with this safety message. A persisted Anthropic
// error has [1301][message][request-id]; accept that precise message form
// even without a separate code. Neither a number alone nor words in
// assistant text establish a refusal.
func zhipuSafetyRefusal(code any, message string) bool {
	const fixed = "系统检测到输入或生成内容可能包含不安全或敏感内容，请您避免输入易产生敏感内容的提示语，感谢您的配合。"
	message = strings.TrimSpace(message)
	if (code == "1301" || code == float64(1301)) && strings.TrimSuffix(message, "。") == strings.TrimSuffix(fixed, "。") {
		return true
	}
	id, ok := strings.CutPrefix(message, "[1301]["+fixed+"][")
	if !ok || !strings.HasSuffix(id, "]") {
		return false
	}
	id = strings.TrimSuffix(id, "]")
	return id != "" && !strings.ContainsAny(id, "[]\r\n")
}

func (h *holdWriter) safetyHoldExpired() bool {
	return h.safetyHold && h.stream && (h.held.Len() > holdMost || time.Since(h.since) > holdThinking)
}

// holdMiMoText keeps only the observed fixed text's prefix, until the
// explicit filter stop is read by scan. A normal stop releases it as an
// ordinary answer. A different word, quotation, explanation, tool or
// unknown content lets the whole reply through immediately. Text alone
// never establishes a refusal, even when it matches the whole sentence.
func (h *holdWriter) holdMiMoText(ev []byte) bool {
	const fixed = "The request was rejected because it was considered high risk"
	var data []byte
	for _, ln := range bytes.Split(ev, []byte("\n")) {
		if bytes.HasPrefix(ln, []byte("data:")) {
			data = append(data, bytes.TrimSpace(ln[5:])...)
		}
	}
	if !json.Valid(data) {
		return false
	}
	v := gjson.ParseBytes(data)
	var text string
	repeated := false
	switch v.Get("type").String() {
	case "content_block_delta":
		if v.Get("delta.type").String() != "text_delta" {
			return false
		}
		text = v.Get("delta.text").String()
	case "content_block_start":
		if v.Get("content_block.type").String() != "text" {
			return false
		}
		text = v.Get("content_block.text").String()
	case "response.output_text.delta":
		text = v.Get("delta").String()
	case "response.output_text.done":
		text, repeated = v.Get("text").String(), true
	case "response.content_part.added", "response.content_part.done":
		if v.Get("part.type").String() != "output_text" {
			return false
		}
		text, repeated = v.Get("part.text").String(), true
	case "response.output_item.added", "response.output_item.done":
		parts := v.Get("item.content").Array()
		if v.Get("item.type").String() != "message" || len(parts) != 1 || parts[0].Get("type").String() != "output_text" {
			return false
		}
		text, repeated = parts[0].Get("text").String(), true
	case "": // Chat Completions: never hold a delta that also carries a tool.
		choices := v.Get("choices").Array()
		if len(choices) != 1 || choices[0].Get("finish_reason").String() != "" {
			return false
		}
		for k, value := range choices[0].Get("delta").Map() {
			if k != "content" && k != "role" && value.Exists() && value.String() != "" {
				return false
			}
		}
		text = choices[0].Get("delta.content").String()
	default:
		return false
	}
	if text == "" {
		return false
	}
	if repeated {
		return h.refusalPrefix != "" && text == h.refusalPrefix
	}
	if len(text) > len(fixed)-len(h.refusalPrefix) || !strings.HasPrefix(fixed, h.refusalPrefix+text) {
		return false
	}
	h.refusalPrefix += text
	return true
}
