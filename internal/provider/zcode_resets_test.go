package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// zhipuCards is a personal plan's reset list with the fields a spend is
// told: two five-hour cards, one running out sooner, and a weekly one.
const zhipuCards = `{
  "code": 200,
  "msg": "操作成功",
  "data": {
    "targetType": "PERSONAL",
    "fiveHourResets": [
      {"recordId": 7, "grantType": "DIRECT", "expireTime": "2026-10-28 12:36:02", "available": true},
      {"recordId": 8, "grantType": "GIFT", "expireTime": "2026-11-02 23:59:59", "available": true}
    ],
    "weekResets": [
      {"recordId": 11, "grantType": "DIRECT", "expireTime": "2026-10-30 23:59:59", "available": true}
    ]
  },
  "success": true
}`

// fakeZcode stands in for Z.ai or BigModel with a personal GLM Coding
// Plan: its windows, its reset cards and the spending of one. used is the
// percentage both windows say; back in a minute is when they start again.
type fakeZcode struct {
	srv   *httptest.Server
	used  float64
	used2 float64 // the weekly window's, when told apart
	cards string
	used_ int // cards spent
	asked chan map[string]any
}

func fakeZcodeOf(t *testing.T) *fakeZcode {
	t.Helper()
	f := &fakeZcode{cards: zhipuCards, asked: make(chan map[string]any, 4)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "glm-key" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/biz/customer-package-reset/list":
			w.Write([]byte(f.cards))
		case "/api/biz/customer-package-reset/use":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.asked <- body
			f.used_++
			w.Write([]byte(`{"code":200,"msg":"操作成功","data":{},"success":true}`))
		case "/api/monitor/usage/quota/limit":
			back := time.Now().Add(30 * time.Minute).UnixMilli()
			w2 := f.used2
			if w2 == 0 {
				w2 = f.used
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"level": "max",
				"limits": []any{
					map[string]any{"type": "CREDIT_LIMIT", "unit": 3, "number": 5, "percentage": f.used, "nextResetTime": back},
					map[string]any{"type": "CREDIT_LIMIT", "unit": 6, "number": 1, "percentage": w2, "nextResetTime": back},
				},
			}})
		default:
			t.Errorf("asked %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// fakeZcodeLogin keeps one ZCode account on the fake vendor's plan.
func fakeZcodeLogin(t *testing.T, f *fakeZcode) {
	t.Helper()
	signIn(t)
	auth, _ := json.Marshal(zcodeKey{Key: "glm-key", Base: f.srv.URL + "/api/anthropic"})
	if err := addSideLogin(savedLogin{Agent: "zcode", User: "me@example.com", Plan: "GLM Coding Max", Auth: auth}, "", func(savedLogin) {}); err != nil {
		t.Fatal(err)
	}
}

func resetAutoResetState(t *testing.T) {
	t.Helper()
	a := &autoReset
	a.Lock()
	a.path = ""
	a.spent, a.next = map[string]autoResetSpent{}, map[string]time.Time{}
	a.Unlock()
}

// The list's own fields — each card's record id and grant type — are kept
// as it says them, for spending it.
func TestZhipuResetCardsKept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(zhipuCards))
	}))
	defer srv.Close()
	rs := zhipuPersonalResets(context.Background(), srv.URL, "glm-key")
	if rs == nil || rs.Count != 3 || rs.FiveHour != 2 || rs.Weekly != 1 {
		t.Fatalf("resets: %+v", rs)
	}
	// sorted soonest first: the 10-28 card, the 10-30 weekly one, the 11-02 one
	if len(rs.Each) != 3 || rs.Each[0].RecordID != float64(7) || rs.Each[0].Grant != "DIRECT" || rs.Each[0].Window != "fiveHour" {
		t.Fatalf("each: %+v", rs.Each)
	}
	if rs.Each[1].RecordID != float64(11) || rs.Each[1].Window != "weekly" || rs.Each[2].RecordID != float64(8) || rs.Each[2].Grant != "GIFT" {
		t.Fatalf("each, after the first: %+v", rs.Each[1:])
	}
}

// UseZcodeReset spends the card that runs out first, telling the vendor
// what its own page does: the card's record id and grant type, a fresh
// request id, and which plan and window the card is of. The use is kept
// as one made by hand.
func TestUseZcodeReset(t *testing.T) {
	f := fakeZcodeOf(t)
	fakeZcodeLogin(t, f)
	out, err := UseZcodeReset(context.Background(), "", "")
	if err != nil || out.Code != "reset" || out.Windows != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	body := <-f.asked
	if body["targetType"] != "PERSONAL" || body["resetType"] != "FIVE_HOUR" || body["recordId"] != float64(7) || body["grantType"] != "DIRECT" {
		t.Fatalf("asked %v", body)
	}
	if id, _ := body["requestId"].(string); id == "" {
		t.Fatalf("no request id: %v", body)
	}
	if used := ResetsUsed("zcode", "me@example.com"); len(used) != 1 || used[0].Why != ResetUsedByHand {
		t.Fatalf("uses: %+v", used)
	}
}

// A card of the window asked for is spent: the weekly one on the week.
func TestUseZcodeResetOfWindow(t *testing.T) {
	f := fakeZcodeOf(t)
	fakeZcodeLogin(t, f)
	out, err := UseZcodeReset(context.Background(), "me@example.com", "weekly")
	if err != nil || out.Code != "reset" {
		t.Fatalf("%+v %v", out, err)
	}
	if body := <-f.asked; body["resetType"] != "WEEK" || body["recordId"] != float64(11) {
		t.Fatalf("asked %v", body)
	}
	// and none of another window is spent when the plan holds none of it
	if out, err := UseZcodeReset(context.Background(), "me@example.com", "monthly"); err != nil || out.Code != "no_credit" {
		t.Fatalf("%+v %v", out, err)
	}
}

// The window that stops the plan and is used up is the one its card is
// spent on, the longest first; the on-demand ones never stop it.
func TestZcodeWindowUsedUp(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Hour)
	five := QuotaWindow{Span: 5 * time.Hour, Used: 100, ResetsAt: &later}
	week := QuotaWindow{Span: 7 * 24 * time.Hour, Used: 100, ResetsAt: &later}
	for name, c := range map[string]struct {
		ws       []QuotaWindow
		kind     string
		noWindow bool
	}{
		"both used up, the week first": {[]QuotaWindow{five, week}, "weekly", false},
		"the five hours only":          {[]QuotaWindow{five, QuotaWindow{Span: 7 * 24 * time.Hour, Used: 40, ResetsAt: &later}}, "fiveHour", false},
		"nothing used up":              {[]QuotaWindow{QuotaWindow{Span: 5 * time.Hour, Used: 90, ResetsAt: &later}}, "", true},
		"on-demand aside":              {[]QuotaWindow{{Span: 30 * 24 * time.Hour, Used: 100, ResetsAt: &later, Aside: true}}, "", true},
		"window end gone by":           {[]QuotaWindow{{Span: 5 * time.Hour, Used: 100, ResetsAt: &now}}, "", true},
	} {
		kind, until := zcodeWindowUsedUp(c.ws, now)
		if (kind == "") != c.noWindow || kind != c.kind || (kind != "" && until == nil) {
			t.Errorf("%s: %q %v", name, kind, until)
		}
	}
}

// A spend kept out of the peak: one due inside it is made at its start,
// one looked at already inside it waits for its end, and one looked at
// after is spent at once.
func TestZhipuSpentAtOutOfPeak(t *testing.T) {
	cst := zhipuZone
	at := func(h, m int) time.Time { return time.Date(2026, 10, 20, h, m, 0, 0, cst) }
	for name, c := range map[string]struct {
		until, now, want time.Time
		stopped          bool
		back             time.Time
	}{
		"due before the peak":   {at(13, 30), at(12, 0), at(13, 0), false, time.Time{}},
		"due inside, before it": {at(16, 30), at(12, 0), at(14, 0), false, time.Time{}},
		"due inside, in it":     {at(16, 30), at(15, 0), time.Time{}, false, time.Time{}},
		"due inside, past it":   {at(18, 20), at(18, 5), at(18, 5), false, time.Time{}},
		"held up in it":         {at(20, 0), at(15, 0), time.Time{}, true, at(21, 0)},
		"held up past it":       {at(20, 0), at(18, 5), at(18, 5), true, at(21, 0)},
	} {
		if got := zhipuSpentAt(c.until, c.now, c.stopped, c.back); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	if got := zhipuSpentAt(at(13, 30), at(12, 0), false, time.Time{}); !got.Equal(at(13, 0)) {
		t.Fatalf("a spend due before the peak, pulled: %v", got)
	}
}

// The says are kept by account, each its own switch, the peak one kept
// when the master switch turns both times off and on again.
func TestZcodeAutoResetCases(t *testing.T) {
	signIn(t)
	if err := SetZcodeAutoReset(" Me@Example.com ", true); err != nil {
		t.Fatal(err)
	}
	if week, expiring, peak := ZcodeAutoResetWhen("ME@example.com"); !week || !expiring || peak {
		t.Fatalf("both on, peak off by default: %v %v %v", week, expiring, peak)
	}
	if !AutoResets("zcode", "me@example.com") || !AutoResetsWeek("zcode", "me@example.com") {
		t.Fatal("the gateway's asks say no")
	}
	if err := SetZcodeAutoResetCase("me@example.com", ResetCasePeak, true); err != nil {
		t.Fatal(err)
	}
	if _, _, peak := ZcodeAutoResetWhen("me@example.com"); !peak || !zcodePeakHeld("me@example.com", time.Date(2026, 10, 20, 15, 0, 0, 0, zhipuZone)) || zcodePeakHeld("me@example.com", time.Date(2026, 10, 20, 19, 0, 0, 0, zhipuZone)) {
		t.Fatalf("peak held wrong")
	}
	if err := SetZcodeAutoResetCase("me@example.com", ResetCaseExpiring, false); err != nil {
		t.Fatal(err)
	}
	if week, expiring, peak := ZcodeAutoResetWhen("me@example.com"); !week || expiring || !peak {
		t.Fatalf("expiring off keeps the others: %v %v %v", week, expiring, peak)
	}
	if s := settings.Load(); s.ZcodeAutoResetOnly["me@example.com"] != ResetCaseWeek || len(s.ZcodeAutoResetOffPeak) != 1 {
		t.Fatalf("kept %v %v", s.ZcodeAutoResetOnly, s.ZcodeAutoResetOffPeak)
	}
	if err := SetZcodeAutoResetCase("me@example.com", "sometimes", true); err == nil {
		t.Fatal("an unknown say was taken")
	}
	if err := SetZcodeAutoReset("me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); len(s.ZcodeAutoReset) != 0 || len(s.ZcodeAutoResetOnly) != 0 || len(s.ZcodeAutoResetOffPeak) != 0 {
		t.Fatalf("all off: %v %v %v", s.ZcodeAutoReset, s.ZcodeAutoResetOnly, s.ZcodeAutoResetOffPeak)
	}
}

// A card the list gives no record id is never spent: the vendor's use
// call takes a request without one to mean the card that runs out first
// and spends it at once (2026-10-17, a probe with recordId null spent
// one), and one spent by mistake can't be taken back. One with an id
// behind the id-less is spent instead.
func TestZhipuUseNoRecordID(t *testing.T) {
	f := fakeZcodeOf(t)
	f.cards = `{"code":200,"msg":"操作成功","data":{"targetType":"PERSONAL",
		"fiveHourResets":[{"recordId":0,"grantType":"DIRECT","expireTime":"2026-11-01 23:59:59","available":true},
		{"grantType":"DIRECT","expireTime":"2026-11-02 23:59:59","available":true},
		{"recordId":9,"grantType":"DIRECT","expireTime":"2026-11-03 23:59:59","available":true}]},"success":true}`
	fakeZcodeLogin(t, f)
	out, err := UseZcodeReset(context.Background(), "", "")
	if err != nil || out.Code != "reset" {
		t.Fatalf("%+v %v", out, err)
	}
	if body := <-f.asked; body["recordId"] != float64(9) {
		t.Fatalf("asked %v", body)
	}

	// with none that has an id, nothing is asked at all
	f.cards = `{"code":200,"msg":"操作成功","data":{"targetType":"PERSONAL",
		"fiveHourResets":[{"recordId":0,"grantType":"DIRECT","expireTime":"2026-11-02 23:59:59","available":true},
		{"grantType":"DIRECT","expireTime":"2026-11-03 23:59:59","available":true}]},"success":true}`
	if out, err := UseZcodeReset(context.Background(), "", ""); err != nil || out.Code != "no_credit" {
		t.Fatalf("%+v %v", out, err)
	}
	select {
	case body := <-f.asked:
		t.Fatalf("spent with no record id: %v", body)
	default:
	}
}

// A window used up spends the card of that window, once for that window;
// the card of another window is never spent on one used up.
func TestAutoUseZcodeReset(t *testing.T) {
	f := fakeZcodeOf(t)
	f.used, f.used2 = 100, 10 // the five hours used up, the week not
	fakeZcodeLogin(t, f)
	resetAutoResetState(t)
	if err := SetZcodeAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	out, err := AutoUseZcodeReset(context.Background(), "")
	if err != nil || out.Code != "reset" {
		t.Fatalf("%+v %v", out, err)
	}
	if body := <-f.asked; body["resetType"] != "FIVE_HOUR" || body["recordId"] != float64(7) {
		t.Fatalf("asked %v", body)
	}
	if used := ResetsUsed("zcode", "me@example.com"); len(used) != 1 || used[0].Why != ResetUsedWeek {
		t.Fatalf("uses: %+v", used)
	}

	// the same window again: none spent
	if out, err := AutoUseZcodeReset(context.Background(), ""); err != nil || out.Code != "" {
		t.Fatalf("a second one for the same window: %+v %v", out, err)
	}

	// the week used up: its card, not a five-hour one
	f.used2 = 100
	a := &autoReset
	a.Lock()
	a.spent = map[string]autoResetSpent{} // its five hours spent for is gone by
	a.Unlock()
	if out, err := AutoUseZcodeReset(context.Background(), ""); err != nil || out.Code != "reset" {
		t.Fatalf("%+v %v", out, err)
	}
	if body := <-f.asked; body["resetType"] != "WEEK" || body["recordId"] != float64(11) {
		t.Fatalf("asked %v", body)
	}

	// no window used up: none spent
	f.used, f.used2 = 10, 10
	a.Lock()
	a.spent = map[string]autoResetSpent{}
	a.Unlock()
	if out, err := AutoUseZcodeReset(context.Background(), ""); err != nil || out.Code != "" {
		t.Fatalf("%+v %v", out, err)
	}
}

// A card about to run out unused is spent half an hour before it does,
// only when the account's windows have been used at all, and never
// during the peak the account keeps clear.
func TestSpendExpiringZcodeResets(t *testing.T) {
	f := fakeZcodeOf(t)
	f.used = 30 // used, not used up
	f.cards = `{"code":200,"msg":"操作成功","data":{"targetType":"PERSONAL",
		"fiveHourResets":[{"recordId":7,"grantType":"DIRECT","expireTime":"` + time.Now().Add(20*time.Minute).Format("2006-01-02 15:04:05") + `","available":true}]},"success":true}`
	fakeZcodeLogin(t, f)
	resetAutoResetState(t)
	if err := SetZcodeAutoResetCase("me@example.com", ResetCaseExpiring, true); err != nil {
		t.Fatal(err)
	}
	SpendExpiringZcodeResets(context.Background())
	select {
	case body := <-f.asked:
		if body["recordId"] != float64(7) {
			t.Fatalf("asked %v", body)
		}
		if used := ResetsUsed("zcode", "me@example.com"); len(used) != 1 || used[0].Why != ResetUsedExpiring {
			t.Fatalf("uses: %+v", used)
		}
	default:
		t.Fatal("the card about to run out was not spent")
	}

	// nothing used: a card that would start nothing again is left
	f.used_ = 0
	resetAutoResetState(t)
	f.used = 0
	SpendExpiringZcodeResets(context.Background())
	select {
	case body := <-f.asked:
		t.Fatalf("spent on windows never used: %v", body)
	default:
	}
}

// A card expiring inside the peak, the account kept off it, is spent at
// the peak's start rather than in it — or left, looked at inside it.
func TestSpendExpiringZcodeResetsOutOfPeak(t *testing.T) {
	f := fakeZcodeOf(t)
	f.used = 30
	f.cards = `{"code":200,"msg":"操作成功","data":{"targetType":"PERSONAL",
		"fiveHourResets":[{"recordId":7,"grantType":"DIRECT","expireTime":"` + time.Now().Add(3*time.Hour).Format("2006-01-02 15:04:05") + `","available":true}]},"success":true}`
	fakeZcodeLogin(t, f)
	resetAutoResetState(t)
	if err := SetZcodeAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := SetZcodeAutoResetCase("me@example.com", ResetCasePeak, true); err != nil {
		t.Fatal(err)
	}
	// the card runs out inside the peak: spent at its start, so not now
	// unless now is before it — and never inside it
	now := time.Now().In(zhipuZone)
	SpendExpiringZcodeResets(context.Background())
	select {
	case body := <-f.asked:
		if h := now.Hour(); !(h < 14) {
			t.Fatalf("spent during or after the peak's start: %v (now %dh)", body, h)
		}
	default:
		if h := now.Hour(); h < 14 && now.Add(3*time.Hour).In(zhipuZone).Hour() >= 14 && now.Add(3*time.Hour).In(zhipuZone).Hour() < 18 {
			t.Fatalf("a card running out inside the peak, now before it, was not spent at its start")
		}
	}
}

// A Codex account and a ZCode account can share a user, and neither
// spends on the other's say: a card the ZCode account spent by itself
// holds off neither the Codex account's auto-use nor the other way
// round, and each agent's uses sit in its own history — though its own
// say of what it spent still holds off its own second one.
func TestZcodeAndCodexShareAnAccount(t *testing.T) {
	f := fakeZcodeOf(t)
	f.used, f.used2 = 100, 10 // the five hours used up, the week not
	fakeZcodeLogin(t, f)
	resetAutoResetState(t)
	if err := SetZcodeAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	consumed := fakeWeekUsedUp(t) // ChatGPT with the same user's week used up
	if out, err := AutoUseZcodeReset(context.Background(), ""); err != nil || out.Code != "reset" {
		t.Fatalf("the ZCode card: %+v %v", out, err)
	}
	if out, err := AutoUseCodexReset(context.Background(), "me@example.com"); err != nil || out.Code != "reset" {
		t.Fatalf("the Codex reset, the same user: %+v %v", out, err)
	}
	if consumed.Load() != 1 {
		t.Fatalf("consumed %d Codex resets, want 1", consumed.Load())
	}
	if f.used_ != 1 {
		t.Fatalf("spent %d ZCode cards, want 1", f.used_)
	}
	z, c := ResetsUsed("zcode", "me@example.com"), ResetsUsed("codex", "me@example.com")
	if len(z) != 1 || z[0].Why != ResetUsedWeek || len(c) != 1 || c[0].Why != ResetUsedWeek {
		t.Fatalf("uses: zcode %+v, codex %+v", z, c)
	}
	// its own say of the window still holds off its own second one
	if out, err := AutoUseZcodeReset(context.Background(), ""); err != nil || out.Code != "" {
		t.Fatalf("a second ZCode card for the same window: %+v %v", out, err)
	}
}

// A codex-autoreset.json kept before the agent was in its keys — the
// ledger was Codex's alone then — still holds off the Codex account it
// was spent for, and not a ZCode account sharing its user.
func TestAutoResetLegacyKeysAreCodex(t *testing.T) {
	f := fakeZcodeOf(t)
	f.used, f.used2 = 100, 10
	fakeZcodeLogin(t, f)
	resetAutoResetState(t)
	if err := SetZcodeAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	p := autoReset.where()
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.Marshal(map[string]autoResetSpent{"me@example.com": {Until: time.Now().Add(72 * time.Hour), At: time.Now().Add(-time.Hour), Out: ResetOutcome{Code: "reset", Windows: 2}}})
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	consumed := fakeWeekUsedUp(t)
	if out, err := AutoUseCodexReset(context.Background(), "me@example.com"); err != nil || out.Code != "" {
		t.Fatalf("the Codex account, held off by what its file kept: %+v %v", out, err)
	}
	if consumed.Load() != 0 {
		t.Fatalf("consumed %d Codex resets, want none", consumed.Load())
	}
	if out, err := AutoUseZcodeReset(context.Background(), ""); err != nil || out.Code != "reset" {
		t.Fatalf("the ZCode account, sharing the user: %+v %v", out, err)
	}
	if f.used_ != 1 {
		t.Fatalf("spent %d ZCode cards, want 1", f.used_)
	}
}
