package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const zhipuSafetyMessage = "系统检测到输入或生成内容可能包含不安全或敏感内容，请您避免输入易产生敏感内容的提示语，感谢您的配合。"

func TestVendorRefusalScope(t *testing.T) {
	for _, c := range []struct {
		p    provider.Provider
		hold bool
	}{
		{provider.Provider{Anthropic: "https://token-plan-cn.xiaomimimo.com/anthropic"}, true},
		{provider.Provider{Chat: "https://OPEN.BIGMODEL.CN:443/api", Anthropic: "https://open.bigmodel.cn/anthropic"}, true},
		{provider.Provider{Chat: "https://token-plan-cn.xiaomimimo.com/v1", Anthropic: "https://relay.example.org/anthropic"}, false},
		{provider.Provider{Chat: "https://relay.example.org/v1", Anthropic: "https://open.bigmodel.cn/anthropic"}, false},
		{provider.Provider{Chat: "https://token-plan-cn.xiaomimimo.com/v1", Anthropic: "https://open.bigmodel.cn/anthropic"}, false},
		{provider.Provider{Anthropic: "https://open.bigmodel.cn.example.org/anthropic"}, false},
		{provider.Provider{}, false},
	} {
		if got := vendorRefusalHost(c.p) != ""; got != c.hold {
			t.Errorf("hold=%t want %t for Chat=%q Anthropic=%q", got, c.hold, c.p.Chat, c.p.Anthropic)
		}
	}
}

// The observed client record has 12,985 reasoning characters, the exact
// high-risk text and rawFinishReason=content_filter, not a captured SSE
// body. These frames use the Anthropic contract and synthetic reasoning.
func vendorThoughtReply(text, stop string) string {
	var thought strings.Builder
	for i := 0; thought.Len() < 12985; i++ {
		fmt.Fprintf(&thought, "reason%d ", i)
	}
	b, _ := json.Marshal(thought.String()[:12985])
	r := strings.Replace(anthropicThoughtThenAnswered, `"hm"`, string(b), 1)
	b, _ = json.Marshal(text)
	r = strings.Replace(r, `"from a"`, string(b), 1)
	r = strings.Replace(r, `"end_turn"`, `"`+stop+`"`, 1)
	return strings.Replace(r, `"output_tokens":4`, `"output_tokens":4443`, 1)
}

// vendorRefusalGateway retains the real host for the selection under
// test, but dials only local fixtures. No vendor or credential is used.
func vendorRefusalGateway(t *testing.T, host string, a *scripted, from provider.Protocol, fallback bool) (*Server, *scripted, string, string) {
	t.Helper()
	up := httptest.NewServer(a)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "a", Name: "A", Key: "fixture", Models: []string{"m"}, Anthropic: "http://" + host}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "b", provider.Anthropic, b)
	members := []string{"a/m"}
	if fallback {
		members = append(members, "b/m")
	}
	refusalGroup(t, members...)
	s := New()
	var dial net.Dialer
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		name, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if name == host {
			addr = up.Listener.Addr().String()
		} else if ip := net.ParseIP(name); ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("unexpected external dial: %s", addr)
		}
		return dial.DialContext(ctx, network, addr)
	}}
	t.Cleanup(tr.CloseIdleConnections)
	s.client = &http.Client{Transport: tr}
	path, ask := refusalErrorAsk(from)
	return s, b, path, ask
}

func TestVendorRefusalAfterReasoning(t *testing.T) {
	for _, from := range []provider.Protocol{provider.Anthropic, provider.Chat, provider.Responses} {
		// The GLM fixtures combine its documented code and persisted
		// error message with the protocol contract, not a wire capture.
		for _, c := range []struct{ name, host, failure string }{
			{"mimo filter", "token-plan-cn.xiaomimimo.com", ""},
			{"zhipu code", "open.bigmodel.cn", `"code":"1301","message":"` + zhipuSafetyMessage + `"`},
			{"zhipu bracketed message", "open.bigmodel.cn", `"message":"[1301][` + zhipuSafetyMessage + `][fixture-request-id]"`},
		} {
			t.Run(c.name+"/"+string(from), func(t *testing.T) {
				fresh(t)
				body := vendorThoughtReply(highRiskMessage, "content_filter")
				if c.failure != "" {
					body = body[:strings.Index(body, `event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":1`)] + sse(`event: error`+"\n"+`data: {"type":"error","error":{"type":"invalid_request_error",`+c.failure+`}}`)
				}
				a := &scripted{replies: []reply{{200, "text/event-stream", body}}}
				s, b, path, ask := vendorRefusalGateway(t, c.host, a, from, true)
				code, got := sendRefusalError(t, s, path, ask)
				if code != 200 || !strings.Contains(got, "from b") || strings.Contains(got, "reason0") || strings.Contains(got, highRiskMessage) || strings.Contains(got, "1301") || a.n != 1 || b.n != 1 {
					t.Fatalf("refusal did not move to the fallback: %d a=%d b=%d %.600s", code, a.n, b.n, got)
				}
				r := lastRoute(s)
				if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil {
					t.Fatalf("refusal rested or retried the account: %+v", r.Tries)
				}
				if c.failure == "" && (len(r.Usage) != 2 || r.Usage[0].Output != 4443) {
					t.Fatalf("terminal usage was lost: %+v", r.Usage)
				}
			})
		}
	}
}

func TestMiMoRefusalTextIsNotAHeuristic(t *testing.T) {
	for _, c := range []struct {
		name, host, text, stop string
		fallback               bool
	}{
		{"normal exact sentence", "token-plan-cn.xiaomimimo.com", highRiskMessage, "end_turn", true},
		{"quotation", "token-plan-cn.xiaomimimo.com", `"` + highRiskMessage + `"`, "content_filter", true},
		{"explanation", "token-plan-cn.xiaomimimo.com", highRiskMessage + ". This is the server's error.", "content_filter", true},
		{"ordinary text", "token-plan-cn.xiaomimimo.com", "Here is the answer", "content_filter", true},
		{"last candidate", "token-plan-cn.xiaomimimo.com", highRiskMessage, "content_filter", false},
		{"another host", "relay.example.org", highRiskMessage, "content_filter", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			body := vendorThoughtReply(c.text, c.stop)
			a := &scripted{replies: []reply{{200, "text/event-stream", body}}}
			s, b, path, ask := vendorRefusalGateway(t, c.host, a, provider.Anthropic, c.fallback)
			code, got := sendRefusalError(t, s, path, ask)
			if code != 200 || got != body || a.n != 1 || b.n != 0 {
				t.Fatalf("ordinary or already-sent answer was replaced: %d a=%d b=%d %.400s", code, a.n, b.n, got)
			}
		})
	}
}

func TestZhipuSafetyErrorContract(t *testing.T) {
	for _, code := range []any{"1301", 1301} {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": zhipuSafetyMessage}})
		if _, ok := policyRefusal(b); !ok {
			t.Errorf("official safety error was not a refusal: %s", b)
		}
	}
	for _, code := range []any{nil, "1301", 1301} {
		bracketed, _ := json.Marshal(map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": code, "message": "[1301][" + zhipuSafetyMessage + "][fixture-request-id]"}})
		if _, ok := policyRefusal(bracketed); !ok {
			t.Errorf("persisted Anthropic error message was not a refusal: %s", bracketed)
		}
	}
	for _, c := range []struct {
		code    any
		message string
	}{
		{"1302", zhipuSafetyMessage}, {1301, "invalid API key"}, {1301.5, zhipuSafetyMessage},
		{nil, zhipuSafetyMessage},
		{nil, "[1302][" + zhipuSafetyMessage + "][fixture-request-id]"},
		{nil, "[1301][" + zhipuSafetyMessage + "][]"},
		{nil, "[1301][" + zhipuSafetyMessage + "][fixture-request-id] explanation"},
		{nil, "[1301][" + zhipuSafetyMessage + "][fixture-request-id][extra]"},
	} {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": c.code, "message": c.message}})
		if _, ok := policyRefusal(b); ok {
			t.Errorf("unrelated error was a refusal: %s", b)
		}
	}
}

func TestMiMoWholeReplyScope(t *testing.T) {
	for _, c := range []struct {
		content string
		refused bool
	}{
		{`[]`, true},
		{`[{"type":"text","text":"` + highRiskMessage + `"}]`, false},
		{`[{"type":"thinking","thinking":"reasoning"}]`, false},
		{`[{"type":"tool_use","id":"c1","name":"read_file","input":{}}]`, false},
	} {
		body := []byte(`{"type":"message","stop_reason":"content_filter","content":` + c.content + `}`)
		if _, ok := refusedReply(body); ok != c.refused {
			t.Errorf("whole reply refusal=%t, want %t: %s", ok, c.refused, body)
		}
	}
}

func TestVendorRefusalHoldingBounds(t *testing.T) {
	full := vendorThoughtReply(highRiskMessage, "content_filter")
	textAt := strings.Index(full, `event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":1`)
	lead := full[:textAt]
	filter := sse(`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"content_filter"},"usage":{"output_tokens":4443}}`, `event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	start := func() (*holdWriter, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		h := newHoldWriter(rec, true)
		h.safetyHold, h.mimoText = true, true
		h.alive, h.streams = &keptAlive{proto: provider.Anthropic, streams: true}, true
		h.Header().Set("Content-Type", "text/event-stream")
		h.WriteHeader(200)
		h.first.start = time.Now().Add(-time.Second)
		h.Write([]byte(lead))
		if h.passing || !h.started() || h.first.first == 0 {
			t.Fatal("held reasoning must still count as the first token")
		}
		return h, rec
	}
	textEvent := func(text string) string {
		b, _ := json.Marshal(text)
		return sse(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":` + string(b) + `}}`)
	}
	for split := 0; split <= len(highRiskMessage); split++ {
		h, rec := start()
		for _, part := range []string{highRiskMessage[:split], highRiskMessage[split:]} {
			ev := textEvent(part)
			// Even the event and JSON tokens can straddle writes.
			for i := range len(ev) {
				h.Write([]byte(ev[i : i+1]))
			}
		}
		if h.passing || rec.Body.Len() != 0 {
			t.Fatalf("prefix split %d was released before its stop", split)
		}
		h.Write([]byte(filter))
		h.settle()
		if !h.failed() || !h.refused || h.passing {
			t.Fatalf("split %d did not classify the explicit filter", split)
		}
	}
	t.Run("keepalive", func(t *testing.T) {
		h, rec := start()
		h.keepQuiet()
		if h.passing || !strings.Contains(rec.Body.String(), ": keepalive") || strings.Contains(rec.Body.String(), "reason0") {
			t.Fatal("held reasoning must send only a keepalive")
		}
	})
	for _, why := range []string{"time", "size", "tool", "mismatch"} {
		t.Run(why, func(t *testing.T) {
			h, rec := start()
			switch why {
			case "time":
				h.since = time.Now().Add(-holdThinking - time.Second)
				h.keepQuiet()
			case "size":
				// An unfinished event also counts toward the memory cap.
				for h.held.Len() <= holdMost && !h.passing {
					h.Write([]byte(strings.Repeat(" ", 32<<10)))
				}
			case "tool":
				h.Write([]byte(textEvent("The request")))
				h.Write([]byte(sse(`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"c1","name":"read_file","input":{}}}`)))
			case "mismatch":
				h.Write([]byte(textEvent(highRiskMessage + ". Explanation follows.")))
			}
			if !h.passing || !strings.Contains(rec.Body.String(), "reason0") {
				t.Fatal("bound, tool or ordinary text must release the whole reply")
			}
			h.Write([]byte(filter))
			h.settle()
			if h.failed() || h.refused || !h.refusedAfter {
				t.Fatal("a filter after output must not replay or rest the account")
			}
		})
	}
}
