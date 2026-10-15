package provider

import (
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/netproxy"
)

func init() { netproxy.HTTP1 = http1Backend }

// http1Backend keeps the observed HTTP/2 stream-reset compatibility
// workaround scoped to the backend that needs it. MiMo's China Token Plan
// has reset Anthropic streams after text with INTERNAL_ERROR; speaking
// HTTP/1.1 avoids that failure mode without replaying a partial reply.
// This does not establish why the peer reset the stream. Revisit the CN
// exception when upstream's HTTP/2 behavior is fixed or verified stable;
// the other MiMo regions, pay-as-you-go API and app are unchanged.
func http1Backend(u *url.URL) bool {
	return codexBackend(u) || strings.EqualFold(u.Hostname(), "token-plan-cn.xiaomimimo.com")
}

// codexBackend says u is on the ChatGPT backend (CodexBase's host), which
// every transport netproxy.Dispatch makes speaks HTTP/1.1 to: the
// gateway's turns, the models list, the warm-up, the allowance and its
// resets. Codex itself does: codex-cli offers no ALPN at all there, and
// Pi's Codex provider speaks HTTP/1.1 or a WebSocket. On one shared HTTP/2
// connection a Codex stream was reset now and then mid-reply (jasonz3g on
// X, "stream ID 21; INTERNAL_ERROR; received from peer"), and through a
// local proxy a request failed with PROTOCOL_ERROR until HTTP/2 was turned
// off.
func codexBackend(u *url.URL) bool {
	b, err := url.Parse(CodexBase)
	return err == nil && u.Host == b.Host
}
