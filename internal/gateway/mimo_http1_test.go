package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
)

// These are protocol-contract fixtures, not captured MiMo payloads. The
// reported failure was Anthropic passthrough after text, with an HTTP/2
// INTERNAL_ERROR received from the peer. ErrAbortHandler after a flush
// makes Go's HTTP/2 server send that real RST_STREAM. Giving HTTP/1.1 a
// whole reply here proves the transport selection, not why MiMo reset it.
func TestMiMoCNStreamsOverHTTP1(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		for _, api := range []provider.Protocol{provider.Anthropic, provider.Chat, provider.Responses} {
			t.Run(fmt.Sprintf("%s/proxy=%v", api, proxy), func(t *testing.T) {
				fresh(t)
				before, after, path, ask := mimoStreamContract(api)
				s, seen := mimoResetGateway(t, "token-plan-cn.xiaomimimo.com", proxy, before, after, false)
				code, body := mimoStreamAsk(t, s, path, ask)
				if code != http.StatusOK || !strings.Contains(body, "hel") || !strings.Contains(body, after) || strings.Contains(body, "connection lost") {
					t.Fatalf("reply did not finish: %d %s", code, body)
				}
				upstreamPath := path
				if api == provider.Anthropic {
					upstreamPath = "/anthropic" + path
				}
				if got := seen(); !slices.Equal(got, []string{upstreamPath + " HTTP/1.1"}) {
					t.Fatalf("one HTTP/1.1 request wanted, got %v", got)
				}
			})
		}
	}
}

// Other hosts keep HTTP/2. A reset after text or a tool has begun still
// ends the stream with an error, without replaying it on another provider.
// The same boundary holds if the CN endpoint loses HTTP/1.1 mid-reply.
func TestMiMoStreamCutDoesNotReplay(t *testing.T) {
	for _, host := range []string{"token-plan-sgp.xiaomimimo.com", "token-plan-cn.xiaomimimo.com"} {
		for _, tool := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tool=%v", host, tool), func(t *testing.T) {
				fresh(t)
				before, after, path, ask := mimoStreamContract(provider.Anthropic)
				marker := "hel"
				if tool {
					ask = strings.TrimSuffix(ask, "}") + `,"tools":[{"name":"read_file","description":"Read one file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}`
					before = anthropicStart + sse(`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"read_file","input":{}}}`,
						`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`)
					marker = "call_1"
				}
				s, seen := mimoResetGateway(t, host, false, before, after, true)
				code, body := mimoStreamAsk(t, s, path, ask)
				cause, proto := "unexpected EOF", "HTTP/1.1"
				if host != "token-plan-cn.xiaomimimo.com" {
					cause, proto = "INTERNAL_ERROR; received from peer", "HTTP/2.0"
				}
				if code != http.StatusOK || !strings.Contains(body, marker) || !strings.Contains(body, "connection lost mid-reply") || !strings.Contains(body, cause) || strings.Contains(body, "message_stop") {
					t.Fatalf("cut reply must say it failed: %d %s", code, body)
				}
				if got := seen(); len(got) != 1 || !strings.HasSuffix(got[0], " "+proto) {
					t.Fatalf("cut reply was replayed or transport changed: %v", got)
				}
			})
		}
	}
}

func mimoStreamContract(api provider.Protocol) (before, after, path, ask string) {
	switch api {
	case provider.Anthropic:
		return anthropicStart + sse(`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) + anthropicText,
			sse(`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`) + anthropicStop,
			"/v1/messages", `{"model":"xiaomi/mimo-v2.6-pro","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	case provider.Responses:
		return quietCreated + quietDelta("hel"), quietDone,
			"/v1/responses", `{"model":"xiaomi/mimo-v2.6-pro","stream":true,"input":"hi"}`
	default:
		return sse(`data: {"id":"c1","object":"chat.completion.chunk","model":"mimo-v2.6-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}`),
			sse(`data: {"id":"c1","object":"chat.completion.chunk","model":"mimo-v2.6-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`),
			"/v1/chat/completions", `{"model":"xiaomi/mimo-v2.6-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	}
}

func mimoResetGateway(t *testing.T, host string, throughProxy bool, before, after string, cutHTTP1 bool) (*Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Proto)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if r.ProtoMajor == 1 && cutHTTP1 {
			w.Header().Set("Content-Length", fmt.Sprint(len(before)+len(after)))
		}
		io.WriteString(w, before)
		w.(http.Flusher).Flush()
		if r.ProtoMajor == 2 || cutHTTP1 {
			panic(http.ErrAbortHandler)
		}
		io.WriteString(w, after)
	}))
	up.EnableHTTP2 = true
	up.StartTLS()
	t.Cleanup(up.Close)
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	var d net.Dialer
	proxy := ""
	proxyAddr := ""
	if throughProxy {
		p := newConnectProxy(t, func(addr string) (net.Conn, error) {
			if addr != host+":443" {
				return nil, fmt.Errorf("unexpected CONNECT to %s", addr)
			}
			return d.Dial("tcp", up.Listener.Addr().String())
		})
		proxy, proxyAddr = p.URL(), p.ln.Addr().String()
		t.Cleanup(func() {
			if got := p.connects(); !slices.Equal(got, []string{host + ":443"}) {
				t.Errorf("provider proxy was not used once: %v", got)
			}
		})
	}
	var fallbackAddr string
	base := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: "example.com"},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			switch addr {
			case host + ":443":
				addr = up.Listener.Addr().String()
			case proxyAddr:
			case fallbackAddr:
			default:
				return nil, fmt.Errorf("unexpected dial to %s", addr)
			}
			return d.DialContext(ctx, network, addr)
		},
	}
	dispatch := netproxy.Dispatch(base)
	t.Cleanup(dispatch.(interface{ CloseIdleConnections() }).CloseIdleConnections)
	s := New()
	s.client = &http.Client{Transport: replayable{dispatch}}
	var fallbackCalls atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	fallbackAddr = fallback.Listener.Addr().String()
	t.Cleanup(fallback.Close)
	t.Cleanup(func() {
		if n := fallbackCalls.Load(); n != 0 {
			t.Errorf("already-started reply fell back %d times", n)
		}
	})
	for _, p := range []provider.Provider{
		{ID: "xiaomi", Name: "Xiaomi MiMo", Key: "k", Models: []string{"mimo-v2.6-pro"}, Chat: "https://" + host + "/v1", Anthropic: "https://" + host + "/anthropic", Responses: "https://" + host + "/v1", Proxy: proxy, Fallback: []string{"fallback/m"}},
		{ID: "fallback", Name: "Fallback", Key: "k", Models: []string{"m"}, Anthropic: fallback.URL},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

func mimoStreamAsk(t *testing.T, s *Server, path, body string) (int, string) {
	t.Helper()
	gw := httptest.NewServer(lanGuard(s.Handler()))
	defer gw.Close()
	client := gw.Client()
	client.Timeout = 10 * time.Second // a deadlock guard, not a speed assertion
	res, err := client.Post(gw.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}
