package provider

import (
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
)

func TestMiMoHTTP1Scope(t *testing.T) {
	for _, c := range []struct {
		url  string
		want bool
	}{
		{"https://token-plan-cn.xiaomimimo.com/anthropic/v1/messages", true},
		{"https://token-plan-cn.xiaomimimo.com/v1/chat/completions", true},
		{"https://token-plan-cn.xiaomimimo.com:443/v1/responses", true},
		{"https://TOKEN-PLAN-CN.XIAOMIMIMO.COM/v1/models", true},
		{"https://token-plan-sgp.xiaomimimo.com/v1/responses", false},
		{"https://token-plan-ams.xiaomimimo.com/v1/responses", false},
		{"https://api.xiaomimimo.com/v1/responses", false},
		{"https://mimo-server-sgp.xiaomimimo.com/api/route", false},
		{"https://token-plan-cn.xiaomimimo.com.example.org/v1/responses", false},
		{"https://relay.example.org/token-plan-cn.xiaomimimo.com/v1/responses", false},
	} {
		t.Run(c.url, func(t *testing.T) {
			u, err := url.Parse(c.url)
			if err != nil {
				t.Fatal(err)
			}
			if got := netproxy.HTTP1(u); got != c.want {
				t.Errorf("HTTP/1.1 only = %v, want %v", got, c.want)
			}
		})
	}
}
