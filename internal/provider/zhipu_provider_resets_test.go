package provider

// A GLM Coding Plan behind a provider key — the zhipu preset's shape —
// spends its reset cards as a ZCode account does (zcode_resets_test.go),
// told apart by its provider's id (and the key's name, when the provider
// holds more than one key), its requests rewritten to the fake vendor.

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// fakeZhipuProvider keeps one GLM plan key provider on the fake vendor,
// as the zhipu preset is set up: no account sign-in, the plan's key.
func fakeZhipuProvider(t *testing.T, f *fakeZcode) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", home)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{f.srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	if err := store(file{Providers: []Provider{{
		ID: "zhipu", Name: "Zhipu GLM Coding", Preset: "zhipu",
		Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Key: "glm-key",
	}}}); err != nil {
		t.Fatal(err)
	}
}

func resetExpiringState(t *testing.T) {
	t.Helper()
	expiring.Lock()
	expiring.next, expiring.until = map[string]time.Time{}, map[string]time.Time{}
	expiring.Unlock()
}

// A GLM plan key provider is told from one that isn't: its quota read
// from Zhipu's own plan endpoint, no account, a key on. The identity is
// the provider's id, with the key's name when it holds more than one.
func TestZhipuPlanKeyIdentity(t *testing.T) {
	glm := Provider{ID: "glm", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Key: "glm-a", KeyName: "work",
		Keys: []KeyAccount{{Key: "glm-a"}, {Key: "glm-b", Name: "home"}}}
	if !ZhipuPlanKey(glm) {
		t.Fatal("a GLM plan key provider not told")
	}
	if id, ok := ZhipuPlanIdentityOf(glm); !ok || id != "glm/work" {
		t.Fatalf("identity of the key in use: %q %v", id, ok)
	}
	two := glm
	two.Key, two.KeyName = "glm-b", "home"
	if id, _ := ZhipuPlanIdentityOf(two); id != "glm/home" {
		t.Fatalf("identity of the second key: %q", id)
	}
	one := glm
	one.Keys = nil
	if id, _ := ZhipuPlanIdentityOf(one); id != "glm" {
		t.Fatalf("identity with one key: %q", id)
	}
	// a pay-as-you-go GLM key is told apart by its cards, not here: its
	// host is the plan's own, but it lists no reset cards, so its card
	// shows no switch and a spend finds nothing to spend.
	for name, p := range map[string]Provider{
		"an api key of another vendor": {ID: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "k"},
		"no key":                       {ID: "glm", Chat: "https://open.bigmodel.cn/api/coding/paas/v4"},
	} {
		if ZhipuPlanKey(p) {
			t.Errorf("%s: told for a GLM plan key", name)
		}
	}
	if _, ok := ZhipuPlanIdentityOf(Provider{ID: "deepseek", Key: "k"}); ok {
		t.Error("identity of a provider that isn't one")
	}
}

// The says are kept by key, each its own switch, the peak one kept when
// the master switch turns both times off and on again.
func TestZhipuAutoResetCases(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := SetZhipuAutoReset(" Zhipu ", true); err != nil {
		t.Fatal(err)
	}
	if week, expiring, peak := ZhipuAutoResetWhen("ZHIPU"); !week || !expiring || peak {
		t.Fatalf("both on, peak off by default: %v %v %v", week, expiring, peak)
	}
	if !ZhipuAutoResetWeek("zhipu") {
		t.Fatal("the gateway's ask says no")
	}
	if s := AutoResetWords("zhipu", "zhipu"); s != "auto" {
		t.Fatalf("words: %q", s)
	}
	if err := SetZhipuAutoResetCase("zhipu", ResetCasePeak, true); err != nil {
		t.Fatal(err)
	}
	if _, _, peak := ZhipuAutoResetWhen("zhipu"); !peak || !zhipuPeakHeld("zhipu", time.Date(2026, 10, 20, 15, 0, 0, 0, zhipuZone)) || zhipuPeakHeld("zhipu", time.Date(2026, 10, 20, 19, 0, 0, 0, zhipuZone)) {
		t.Fatalf("peak held wrong")
	}
	if err := SetZhipuAutoResetCase("zhipu", ResetCaseWeek, false); err != nil {
		t.Fatal(err)
	}
	if week, expiring, peak := ZhipuAutoResetWhen("zhipu"); week || !expiring || !peak {
		t.Fatalf("week off keeps the others: %v %v %v", week, expiring, peak)
	}
	if s := AutoResetWords("zhipu", "zhipu"); s != "auto before one expires" {
		t.Fatalf("words: %q", s)
	}
	if err := SetZhipuAutoResetCase("zhipu", "sometimes", true); err == nil {
		t.Fatal("an unknown say was taken")
	}
	if err := SetZhipuAutoReset("zhipu", false); err != nil {
		t.Fatal(err)
	}
	if week, expiring, peak := ZhipuAutoResetWhen("zhipu"); week || expiring || peak {
		t.Fatalf("all off: %v %v %v", week, expiring, peak)
	}
}

// UseZhipuReset spends the card that runs out first, telling the vendor
// what its own page does, as a ZCode account's does; the use is kept as
// one made by hand under the provider key's identity.
func TestUseZhipuReset(t *testing.T) {
	f := fakeZcodeOf(t)
	fakeZhipuProvider(t, f)
	out, err := UseZhipuReset(context.Background(), "zhipu", "")
	if err != nil || out.Code != "reset" || out.Windows != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	body := <-f.asked
	if body["targetType"] != "PERSONAL" || body["resetType"] != "FIVE_HOUR" || body["recordId"] != float64(7) || body["grantType"] != "DIRECT" {
		t.Fatalf("asked %v", body)
	}
	if used := ResetsUsed("zhipu", "zhipu"); len(used) != 1 || used[0].Why != ResetUsedByHand {
		t.Fatalf("uses: %+v", used)
	}
	// of the window asked for: the weekly one on the week
	if out, err := UseZhipuReset(context.Background(), "zhipu", "weekly"); err != nil || out.Code != "reset" {
		t.Fatalf("%+v %v", out, err)
	}
	if body := <-f.asked; body["resetType"] != "WEEK" || body["recordId"] != float64(11) {
		t.Fatalf("asked %v", body)
	}
	// a key that isn't one is refused, not guessed at
	if _, err := UseZhipuReset(context.Background(), "deepseek", ""); err == nil || !strings.Contains(err.Error(), "no GLM plan key") {
		t.Fatalf("err: %v", err)
	}
}

// A window used up spends the card of that window, once for that window;
// a key the user said nothing about spends none.
func TestAutoUseZhipuReset(t *testing.T) {
	f := fakeZcodeOf(t)
	f.used, f.used2 = 10, 100 // the week used up, the five hours not
	fakeZhipuProvider(t, f)
	resetAutoResetState(t)
	out, err := AutoUseZhipuReset(context.Background(), "zhipu")
	if err != nil || out.Code != "" || out.Code == "reset" {
		t.Fatalf("off by default: %+v %v", out, err)
	}
	select {
	case body := <-f.asked:
		t.Fatalf("spent with the switch off: %v", body)
	default:
	}
	if err := SetZhipuAutoReset("zhipu", true); err != nil {
		t.Fatal(err)
	}
	if out, err := AutoUseZhipuReset(context.Background(), "zhipu"); err != nil || out.Code != "reset" {
		t.Fatalf("%+v %v", out, err)
	}
	if body := <-f.asked; body["resetType"] != "WEEK" || body["recordId"] != float64(11) {
		t.Fatalf("asked %v", body)
	}
	if used := ResetsUsed("zhipu", "zhipu"); len(used) != 1 || used[0].Why != ResetUsedWeek {
		t.Fatalf("uses: %+v", used)
	}
	// once for that window: the card of another is not spent on it
	if out, err := AutoUseZhipuReset(context.Background(), "zhipu"); err != nil || out.Code == "reset" {
		t.Fatalf("spent again for the same window: %+v %v", out, err)
	}
	select {
	case body := <-f.asked:
		t.Fatalf("asked %v", body)
	default:
	}
}

// A card about to run out unused is spent before it does, when the key's
// windows have been used; one that would start nothing again is left.
func TestSpendExpiringZhipuResets(t *testing.T) {
	f := fakeZcodeOf(t)
	f.used = 30 // used, not used up
	f.cards = `{"code":200,"msg":"操作成功","data":{"targetType":"PERSONAL",
		"fiveHourResets":[{"recordId":7,"grantType":"DIRECT","expireTime":"` + time.Now().Add(20*time.Minute).Format("2006-01-02 15:04:05") + `","available":true}]},"success":true}`
	fakeZhipuProvider(t, f)
	resetAutoResetState(t)
	resetExpiringState(t)
	if err := SetZhipuAutoResetCase("zhipu", ResetCaseExpiring, true); err != nil {
		t.Fatal(err)
	}
	SpendExpiringZhipuResets(context.Background())
	select {
	case body := <-f.asked:
		if body["recordId"] != float64(7) {
			t.Fatalf("asked %v", body)
		}
		if used := ResetsUsed("zhipu", "zhipu"); len(used) != 1 || used[0].Why != ResetUsedExpiring {
			t.Fatalf("uses: %+v", used)
		}
	default:
		t.Fatal("the card about to run out was not spent")
	}

	// nothing used: a card that would start nothing again is left
	f.used_ = 0
	resetAutoResetState(t)
	resetExpiringState(t)
	f.used = 0
	SpendExpiringZhipuResets(context.Background())
	select {
	case body := <-f.asked:
		t.Fatalf("spent on windows never used: %v", body)
	default:
	}
}
