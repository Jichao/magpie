package provider

// A GLM Coding Plan's reset cards, spent from magpie for the ZCode
// accounts the user said may, as Codex's resets are (codex_resets.go):
// Zhipu lists them by window (zhipuResets) — one starts the five hours
// again, one the week — and spends one at /api/biz/customer-package-reset/use,
// told the card's own record id and grant type and a fresh request id,
// as bigmodel.cn's usage page asks it. The one that runs out first is
// spent, so none that lasts longer goes before it. The ledgers of a spend
// keep the agent with the account (resetsLedgerKey), for a Codex account
// can share the user and neither may spend on the other's say.
//
// A window used up spends the card of that window, the week's before the
// five hours' where both are: the five hours do pass on their own, as
// Codex's do, but a GLM plan's cards are five-hour ones mostly, and
// waiting the five hours out is what the card is for. The card of
// another window is never spent on one used up: nothing says it starts
// that window again. One a window at most (autoReset), so a busy day
// doesn't eat every card at once.
//
// Accounts the user keeps off the peak (settings.ZcodeAutoResetOffPeak)
// spend no card in UTC+8 2pm–6pm, when usage counts whole against the
// plan's windows instead of half (the off-peak half price): a spend due
// then is pulled to the peak's start, one looked at inside it waits for
// its end, and a card may run out inside it rather than be spent there.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// ResetCasePeak is the third of a ZCode account's says about spending
// its reset cards, kept with the two times a card is spent at: its
// switch (the Usage card, the Settings page) says none is spent during
// the peak.
const ResetCasePeak = "peak"

// zhipuZone is the vendor's clock: the peak is told in Beijing time,
// whatever the computer's is.
var zhipuZone = time.FixedZone("UTC+8", 8*3600)

// inZhipuPeak says whether t falls inside the peak, workdays UTC+8 2pm
// to 6pm (the vendor's page: outside it credits count half, weekends all
// day), so a spend's fresh quota goes twice as far there.
func inZhipuPeak(t time.Time) bool {
	t = t.In(zhipuZone)
	return t.Weekday() >= time.Monday && t.Weekday() <= time.Friday && t.Hour() >= 14 && t.Hour() < 18
}

// zhipuPeakStart is when the peak t falls in began.
func zhipuPeakStart(t time.Time) time.Time {
	t = t.In(zhipuZone)
	return time.Date(t.Year(), t.Month(), t.Day(), 14, 0, 0, 0, zhipuZone)
}

// zcodePeakHeld says the account spends no card at now: the user kept
// the peak clear for it (settings.ZcodeAutoResetOffPeak) and now falls
// inside.
func zcodePeakHeld(user string, now time.Time) bool {
	_, _, peak := ZcodeAutoResetWhen(user)
	return peak && inZhipuPeak(now)
}

// zhipuSpentAt is expiringResetSpent kept out of the peak: a spend due
// inside it is made at its start instead — as late as is safe before it
// — one looked at already inside it waits for its end, and one due
// before it but looked at after is spent at once, past it. Zero (not
// due) as expiringResetSpent says it.
func zhipuSpentAt(until, now time.Time, stopped bool, back time.Time) time.Time {
	at := expiringResetSpent(until, now, stopped, back)
	if at.IsZero() || !inZhipuPeak(at) {
		return at
	}
	if start := zhipuPeakStart(at); start.After(now) {
		return start
	}
	if inZhipuPeak(now) {
		return time.Time{} // in the peak now: nothing until it ends
	}
	return now
}

// ZcodeAutoResetWhen says at which of the two times the ZCode account
// user spends a reset card by itself, and whether it spends none during
// the peak: both times unless kept to one (settings.ZcodeAutoResetOnly).
func ZcodeAutoResetWhen(user string) (week, expiring, peak bool) {
	user = strings.ToLower(user)
	s := settings.Load()
	if user == "" || !containsFold(s.ZcodeAutoReset, user) {
		return false, false, false
	}
	switch s.ZcodeAutoResetOnly[user] {
	case ResetCaseWeek:
		week = true
	case ResetCaseExpiring:
		expiring = true
	default:
		week, expiring = true, true
	}
	peak = containsFold(s.ZcodeAutoResetOffPeak, user)
	return week, expiring, peak
}

func containsFold(ss []string, v string) bool {
	for _, s := range ss {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// ZcodeAutoReset says whether the ZCode account user spends a reset card
// by itself at either time.
func ZcodeAutoReset(user string) bool {
	week, expiring, _ := ZcodeAutoResetWhen(user)
	return week || expiring
}

// ZcodeSignedIn is the ZCode account first in use: ZCode's own account
// when magpie found none other.
func ZcodeSignedIn() (string, bool) {
	ls := zcodeLogins()
	if len(ls) == 0 {
		return "", false
	}
	return ls[0].User, true
}

// SetZcodeAutoReset turns a ZCode account's spending of its reset cards
// by itself on or off, at both times; the say about the peak is kept as
// it is, set on its own.
func SetZcodeAutoReset(user string, on bool) error {
	_, _, peak := ZcodeAutoResetWhen(strings.TrimSpace(user))
	return setZcodeAutoReset(user, on, on, peak)
}

// SetZcodeAutoResetCase turns one of a ZCode account's says on or off —
// ResetCaseWeek, ResetCaseExpiring or ResetCasePeak — the others kept as
// they are.
func SetZcodeAutoResetCase(user, when string, on bool) error {
	week, expiring, peak := ZcodeAutoResetWhen(strings.TrimSpace(user))
	switch when {
	case ResetCaseWeek:
		week = on
	case ResetCaseExpiring:
		expiring = on
	case ResetCasePeak:
		peak = on
	default:
		return fmt.Errorf("a ZCode reset card is used by itself when a window is used up (%s) or before one expires (%s), and never during the peak (%s), not %q", ResetCaseWeek, ResetCaseExpiring, ResetCasePeak, when)
	}
	return setZcodeAutoReset(user, week, expiring, peak)
}

func setZcodeAutoReset(user string, week, expiring, peak bool) error {
	user = strings.ToLower(strings.TrimSpace(user))
	s := settings.Load()
	s.ZcodeAutoReset = deleteFold(s.ZcodeAutoReset, user)
	only := map[string]string{}
	for u, c := range s.ZcodeAutoResetOnly {
		if !strings.EqualFold(u, user) {
			only[u] = c
		}
	}
	offPeak := deleteFold(s.ZcodeAutoResetOffPeak, user)
	if user != "" && (week || expiring) {
		s.ZcodeAutoReset = append(s.ZcodeAutoReset, user)
		switch {
		case !expiring:
			only[user] = ResetCaseWeek
		case !week:
			only[user] = ResetCaseExpiring
		}
		if peak {
			offPeak = append(offPeak, user)
		}
	}
	s.ZcodeAutoResetOnly, s.ZcodeAutoResetOffPeak = only, offPeak
	return settings.Save(s)
}

func deleteFold(ss []string, v string) []string {
	out := ss[:0]
	for _, s := range ss {
		if !strings.EqualFold(s, v) {
			out = append(out, s)
		}
	}
	return out
}

// zcodeLoginUser is the ZCode account user: the one in use when "", else
// the one kept under that name.
func zcodeLoginUser(user string) (zcodeLoginKey, bool) {
	ls := zcodeLogins()
	if len(ls) == 0 {
		return zcodeLoginKey{}, false
	}
	if user == "" {
		return ls[0], true
	}
	for _, l := range ls {
		if strings.EqualFold(l.User, user) {
			return l, true
		}
	}
	return zcodeLoginKey{}, false
}

// zcodeResetsOf lists the account's windows and reset cards again, as
// spending one reads them. A team seat's windows need its project's key;
// when that can't be found the resets are still listed, the windows not.
func zcodeResetsOf(ctx context.Context, k zcodeKey) ([]QuotaWindow, *ResetCredits, error) {
	if k.team() {
		root := zcodeBizRoot(k.Base)
		key, err := zcodeTeamKeyOf(ctx, k)
		if err != nil {
			return nil, zhipuTeamResets(ctx, root, k.Token, k.Base, k.Org, k.Project), nil
		}
		ws, err := zhipuTeamWindows(ctx, root, key, k.Base, k.Org, k.Project)
		if err != nil {
			return nil, nil, err
		}
		return ws, zhipuTeamResets(ctx, root, k.Token, k.Base, k.Org, k.Project), nil
	}
	root := zcodeRoot(k.Base)
	var data zhipuLimits
	if err := zcodeGet(ctx, root+"/api/monitor/usage/quota/limit", k.Key, &data); err != nil {
		return nil, nil, err
	}
	return data.windows(), zhipuPersonalResets(ctx, root, k.Key), nil
}

// zhipuResetsOf lists the account's reset cards alone.
func zhipuResetsOf(ctx context.Context, k zcodeKey) *ResetCredits {
	if k.team() {
		return zhipuTeamResets(ctx, zcodeBizRoot(k.Base), k.Token, k.Base, k.Org, k.Project)
	}
	return zhipuPersonalResets(ctx, zcodeRoot(k.Base), k.Key)
}

// zhipuTarget is the use call's targetType: a team's plan or the
// person's own.
func zhipuTarget(k zcodeKey) string {
	if k.team() {
		return "TEAM"
	}
	return "PERSONAL"
}

// firstResetCard is the card that runs out first, of window ("fiveHour"
// or "weekly") when told, any when not: Each is sorted soonest first
// (zhipuResets), one that never runs out last. One with no record id is
// skipped: nothing can spend it (zhipuUse).
func firstResetCard(rs *ResetCredits, window string) (ResetCard, bool) {
	for _, c := range rs.Each {
		if (window == "" || c.Window == window) && zhipuRecordID(c.RecordID) {
			return c, true
		}
	}
	return ResetCard{}, false
}

// zcodeWindowUsedUp is the window that stops the plan and is used up,
// the longest one first (the week before the five hours), and when it
// starts again: kind "" when none is used up. The on-demand ones (Aside)
// never stop the models, and a window whose end went by says nothing.
func zcodeWindowUsedUp(windows []QuotaWindow, now time.Time) (kind string, until *time.Time) {
	var best time.Duration
	for _, w := range windows {
		if w.Aside || w.Span < time.Hour || w.Span <= best || w.Used < 100 || w.ResetsAt == nil || !w.ResetsAt.After(now) {
			continue
		}
		best = w.Span
		until = w.ResetsAt
		if w.Span >= 24*time.Hour {
			kind = "weekly"
		} else {
			kind = "fiveHour"
		}
	}
	return kind, until
}

// zhipuUse spends card of the account k holds — as the vendor's own page
// asks it: the card's record id and grant type as the list said them,
// and a fresh request id, so a retry of the same request spends one card
// once. The five hours or the week starts again, as the card's window
// says. A card with no record id is never spent: the use call takes a
// missing one to mean the card that runs out first and spends it at once,
// and one spent by mistake can't be taken back.
func zhipuUse(ctx context.Context, k zcodeKey, card ResetCard) (ResetOutcome, error) {
	wire := map[string]string{"fiveHour": "FIVE_HOUR", "weekly": "WEEK"}[card.Window]
	if !zhipuRecordID(card.RecordID) || wire == "" {
		return ResetOutcome{Code: "no_credit"}, nil
	}
	auth, hdr, root := k.Key, map[string]string(nil), zcodeRoot(k.Base)
	if k.team() {
		auth, hdr, root = k.Token, zcodeTeamHeaders(k.Base, k.Org, k.Project), zcodeBizRoot(k.Base)
	}
	body := map[string]any{"targetType": zhipuTarget(k), "resetType": wire, "recordId": card.RecordID, "grantType": card.Grant, "requestId": newRedeemID()}
	if err := zcodeCallH(ctx, http.MethodPost, root+"/api/biz/customer-package-reset/use", auth, hdr, body, nil); err != nil {
		return ResetOutcome{}, err
	}
	return ResetOutcome{Code: "reset", Windows: 1}, nil
}

// zhipuRecordID says whether v is a record id a spend can be told: a
// zero one is as none (a redacted list has 0), and the vendor spends its
// first card on a request that says none.
func zhipuRecordID(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		s := strings.TrimSpace(x)
		return s != "" && s != "0"
	case float64:
		return x != 0
	case float32:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	}
	return v != nil
}

// UseZcodeReset spends one of the ZCode account user's reset cards (the
// one in use when ""), the one that runs out first — of window
// ("fiveHour" or "weekly") when told, for a window used up — so none
// that would last longer goes before it. It can't be undone: callers
// ask first. The account's usage is read afresh after, and the use kept
// as one made by hand (ResetsUsed).
func UseZcodeReset(ctx context.Context, user, window string) (ResetOutcome, error) {
	l, ok := zcodeLoginUser(user)
	if !ok {
		return ResetOutcome{}, fmt.Errorf("no ZCode account %q", user)
	}
	card, held := firstResetCard(zhipuResetsOf(ctx, l.key), window)
	if !held {
		return ResetOutcome{Code: "no_credit"}, nil
	}
	out, err := zhipuUse(ctx, l.key, card)
	if err != nil || out.Code != "reset" {
		return out, err
	}
	StaleAllowance("zcode", l.User)
	renewedNow("zcode", l.User)
	noteResetUsed("zcode", l.User, ResetUsedByHand, out)
	return out, nil
}

// AutoUseZcodeReset spends one of the ZCode account user's reset cards
// (the one in use when "") if the user turned that on for it, a window
// that stops the plan is used up, it holds a card of that window, and
// none was spent by itself for that window yet (autoReset.use). Code is
// "" when none was tried; the caller asks again only on "reset". None is
// spent during the peak the user kept clear: the request goes unanswered
// now as it would have, and the next one after the peak spends one.
func AutoUseZcodeReset(ctx context.Context, user string) (ResetOutcome, error) {
	if user == "" {
		live, ok := ZcodeSignedIn()
		if !ok {
			return ResetOutcome{}, nil
		}
		user = live
	}
	if week, _, _ := ZcodeAutoResetWhen(user); !week || zcodePeakHeld(user, time.Now()) {
		return ResetOutcome{}, nil
	}
	var card ResetCard
	return autoReset.use("zcode", user, func(now time.Time) (*time.Time, bool, error) {
		l, ok := zcodeLoginUser(user)
		if !ok {
			return nil, false, fmt.Errorf("no ZCode account %q", user)
		}
		ws, rs, err := zcodeResetsOf(ctx, l.key)
		if err != nil {
			return nil, false, err
		}
		kind, until := zcodeWindowUsedUp(ws, now)
		c, held := firstResetCard(rs, kind)
		card = c
		return until, held, nil
	}, func() (ResetOutcome, error) {
		l, ok := zcodeLoginUser(user)
		if !ok {
			return ResetOutcome{}, fmt.Errorf("no ZCode account %q", user)
		}
		out, err := zhipuUse(ctx, l.key, card)
		if err != nil || out.Code != "reset" {
			return out, err
		}
		StaleAllowance("zcode", l.User)
		renewedNow("zcode", l.User)
		noteResetUsed("zcode", l.User, ResetUsedWeek, out)
		return out, nil
	})
}

// SpendExpiringZcodeResets goes over the ZCode accounts the user lets
// spend their reset cards and spends one about to run out unused, as
// SpendExpiringCodexResets does Codex's (codex_expiring.go): as late as
// is safe, or at once when the account is held up past then — and for an
// account kept off the peak, never during it (zhipuSpentAt).
func SpendExpiringZcodeResets(ctx context.Context) {
	for _, user := range settings.Load().ZcodeAutoReset {
		if ctx.Err() != nil {
			return
		}
		_, atBoth, peak := ZcodeAutoResetWhen(user)
		if !atBoth {
			continue // kept to a window used up, as Codex's are (#2078)
		}
		out, err := expiring.check("zcode", user, time.Now(), func() ([]QuotaWindow, *ResetCredits, error) {
			l, ok := zcodeLoginUser(user)
			if !ok {
				return nil, nil, fmt.Errorf("no ZCode account %q", user)
			}
			return zcodeResetsOf(ctx, l.key)
		}, func() (ResetOutcome, error) {
			l, ok := zcodeLoginUser(user)
			if !ok {
				return ResetOutcome{}, fmt.Errorf("no ZCode account %q", user)
			}
			// one spend at a time, with a window's used-up one too
			autoReset.Lock()
			defer autoReset.Unlock()
			card, held := firstResetCard(zhipuResetsOf(ctx, l.key), "")
			if !held {
				return ResetOutcome{}, nil
			}
			out, err := zhipuUse(ctx, l.key, card)
			if err != nil || out.Code != "reset" {
				return out, err
			}
			StaleAllowance("zcode", l.User)
			renewedNow("zcode", l.User)
			noteResetUsed("zcode", l.User, ResetUsedExpiring, out)
			return out, nil
		}, peak)
		switch {
		case err != nil:
			log.Printf("zcode reset card about to run out: %s: %v", user, err)
		case out.Code != "":
			log.Printf("zcode reset card about to run out: %s used it: %s", user, out.Text())
		}
	}
}
