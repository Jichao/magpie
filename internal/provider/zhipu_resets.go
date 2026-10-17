package provider

// A GLM Coding Plan behind a provider's own key spends its reset cards
// as a ZCode account does (zcode_resets.go): the plan's key lists the
// cards and spends one at the same endpoints, so a provider the user
// holds no account sign-in for — just the key, as the zhipu and zai
// presets are set up — can spend them too. Such a key is told apart by
// its provider's id, with "/<key name>" when the provider holds more
// than one key, as its Usage card is named (settings.ZhipuAutoReset).

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// ZhipuAutoResetWhen says at which of the two times the GLM plan key id
// spends a reset card by itself, and whether it spends none during the
// peak, as ZcodeAutoResetWhen does a ZCode account's.
func ZhipuAutoResetWhen(id string) (week, expiring, peak bool) {
	id = strings.ToLower(id)
	s := settings.Load()
	if id == "" || !containsFold(s.ZhipuAutoReset, id) {
		return false, false, false
	}
	switch s.ZhipuAutoResetOnly[id] {
	case ResetCaseWeek:
		week = true
	case ResetCaseExpiring:
		expiring = true
	default:
		week, expiring = true, true
	}
	peak = containsFold(s.ZhipuAutoResetOffPeak, id)
	return
}

// ZhipuAutoResetWeek says whether the GLM plan key id spends a reset
// card by itself once a window that stops the plan is used up.
func ZhipuAutoResetWeek(id string) bool {
	week, _, _ := ZhipuAutoResetWhen(id)
	return week
}

// zhipuPeakHeld is zcodePeakHeld for a GLM plan key: the user kept the
// peak clear for it and now falls inside.
func zhipuPeakHeld(id string, now time.Time) bool {
	_, _, peak := ZhipuAutoResetWhen(id)
	return peak && inZhipuPeak(now)
}

// SetZhipuAutoReset turns a GLM plan key's spending of its reset cards
// by itself on or off, at both times; the say about the peak is kept as
// it is, set on its own.
func SetZhipuAutoReset(id string, on bool) error {
	_, _, peak := ZhipuAutoResetWhen(strings.TrimSpace(id))
	return setZhipuAutoReset(id, on, on, peak)
}

// SetZhipuAutoResetCase turns one of a GLM plan key's says on or off —
// ResetCaseWeek, ResetCaseExpiring or ResetCasePeak — the others kept as
// they are.
func SetZhipuAutoResetCase(id, when string, on bool) error {
	week, expiring, peak := ZhipuAutoResetWhen(strings.TrimSpace(id))
	switch when {
	case ResetCaseWeek:
		week = on
	case ResetCaseExpiring:
		expiring = on
	case ResetCasePeak:
		peak = on
	default:
		return fmt.Errorf("a GLM plan's reset card is used by itself when a window is used up (%s) or before one expires (%s), and never during the peak (%s), not %q", ResetCaseWeek, ResetCaseExpiring, ResetCasePeak, when)
	}
	return setZhipuAutoReset(id, week, expiring, peak)
}

func setZhipuAutoReset(id string, week, expiring, peak bool) error {
	id = strings.ToLower(strings.TrimSpace(id))
	s := settings.Load()
	s.ZhipuAutoReset = deleteFold(s.ZhipuAutoReset, id)
	only := map[string]string{}
	for u, c := range s.ZhipuAutoResetOnly {
		if !strings.EqualFold(u, id) {
			only[u] = c
		}
	}
	offPeak := deleteFold(s.ZhipuAutoResetOffPeak, id)
	if id != "" && (week || expiring) {
		s.ZhipuAutoReset = append(s.ZhipuAutoReset, id)
		switch {
		case !expiring:
			only[id] = ResetCaseWeek
		case !week:
			only[id] = ResetCaseExpiring
		}
		if peak {
			offPeak = append(offPeak, id)
		}
	}
	s.ZhipuAutoResetOnly, s.ZhipuAutoResetOffPeak = only, offPeak
	return settings.Save(s)
}

// zhipuKeysOf lists a provider's keys on, named as the Usage page's
// cards are (planQuotas): the one in use first under its name, the
// others under theirs.
func zhipuKeysOf(p Provider) (keys, names []string) {
	keys, names = []string{p.Key}, []string{p.KeyName}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" && k.Key != p.Key {
			keys, names = append(keys, k.Key), append(names, k.Name)
		}
	}
	return
}

// zhipuKeyIdentity is the GLM plan key's identity in the settings: the
// provider's id, with the key's name when the provider holds more than
// one key — as the card the user switches it on from is named.
func zhipuKeyIdentity(p Provider, key string) string {
	keys, names := zhipuKeysOf(p)
	if len(keys) <= 1 {
		return p.ID
	}
	name := ""
	for i, k := range keys {
		if k == key {
			name = names[i]
		}
	}
	if name == "" {
		name = Mask(key)
	}
	return p.ID + "/" + name
}

// ZhipuPlanKey says whether p is a provider key on a GLM Coding Plan —
// its quota read from Zhipu's own plan endpoint, no account sign-in —
// whose reset cards the key can spend.
func ZhipuPlanKey(p Provider) bool {
	if p.Account != nil || p.Key == "" {
		return false
	}
	src, ok := planQuotaSourceOf(p)
	return ok && strings.HasSuffix(src.url, "/api/monitor/usage/quota/limit")
}

// ZhipuPlanIdentityOf is the identity of the GLM plan key p serves with,
// as the settings keep it; ok false when p is none.
func ZhipuPlanIdentityOf(p Provider) (string, bool) {
	if !ZhipuPlanKey(p) {
		return "", false
	}
	return zhipuKeyIdentity(p, p.Key), true
}

// zhipuPlanProvider resolves the identity a card carries — the
// provider's id, with the key's name when it holds more than one key —
// to the provider, its Key set to that card's, and the plan's quota
// source.
func zhipuPlanProvider(id string) (p Provider, src planQuotaSource, ok bool) {
	pid, name := pidName(id)
	if pid == "" {
		return Provider{}, planQuotaSource{}, false
	}
	for _, p := range All() {
		// the settings keep the identity lower-case; the provider's id
		// itself may not be
		if !strings.EqualFold(p.ID, pid) || p.Account != nil || !ZhipuPlanKey(p) {
			continue
		}
		src, _ := planQuotaSourceOf(p)
		keys, names := zhipuKeysOf(p)
		if name == "" {
			if p.Key == "" {
				continue
			}
			k := p
			k.Key, k.KeyName = keys[0], names[0]
			return k, src, true
		}
		for i, key := range keys {
			nm := names[i]
			if nm == "" {
				nm = Mask(key)
			}
			if strings.EqualFold(nm, name) {
				k := p
				k.Key, k.KeyName = key, nm
				return k, src, true
			}
		}
	}
	return Provider{}, planQuotaSource{}, false
}

// pidName splits a GLM plan key's identity into its provider's id and
// the key's name, "" when the provider holds one key.
func pidName(id string) (pid, name string) {
	pid, name, _ = strings.Cut(strings.TrimSpace(id), "/")
	return pid, name
}

// zhipuKeyRead lists the key's windows and its reset cards, as a spend
// decides with: the windows routing goes by (planKeyWindows), the cards
// the plan's own page lists.
func zhipuKeyRead(ctx context.Context, p Provider, src planQuotaSource) ([]QuotaWindow, *ResetCredits, error) {
	_, ws, _, err := planKeyWindows(ctx, p, src, p.Key)
	if err != nil {
		return nil, nil, err
	}
	return ws, zhipuPersonalResets(ctx, zcodeRoot(src.url), p.Key), nil
}

// zhipuKeyUse spends card of the GLM plan behind p's key (zhipuUse).
func zhipuKeyUse(ctx context.Context, p Provider, src planQuotaSource, card ResetCard) (ResetOutcome, error) {
	return zhipuUse(ctx, zcodeKey{Key: p.Key, Base: src.url}, card)
}

// zhipuKeyRenew notes what routing goes by again after a spend: the
// key's windows read afresh, so its candidate serves again at once
// rather than resting out the window the card started again.
func zhipuKeyRenew(ctx context.Context, p Provider, src planQuotaSource) {
	_, ws, _, err := planKeyWindows(ctx, p, src, p.Key)
	if err != nil {
		return
	}
	noteKeyAllowance(p, ws, time.Now())
}

// UseZhipuReset spends one of the GLM plan key id's reset cards, the one
// that runs out first — of window ("fiveHour" or "weekly") when told —
// as UseZcodeReset does a ZCode account's. It can't be undone: callers
// ask first. The use is kept as one made by hand (ResetsUsed).
func UseZhipuReset(ctx context.Context, id, window string) (ResetOutcome, error) {
	p, src, ok := zhipuPlanProvider(id)
	if !ok {
		return ResetOutcome{}, fmt.Errorf("no GLM plan key %q", id)
	}
	card, held := firstResetCard(zhipuPersonalResets(ctx, zcodeRoot(src.url), p.Key), window)
	if !held {
		return ResetOutcome{Code: "no_credit"}, nil
	}
	out, err := zhipuKeyUse(ctx, p, src, card)
	if err != nil || out.Code != "reset" {
		return out, err
	}
	zhipuKeyRenew(ctx, p, src)
	noteResetUsed("zhipu", id, ResetUsedByHand, out)
	return out, nil
}

// AutoUseZhipuReset spends one of the GLM plan key id's reset cards if
// the user turned that on for it, a window that stops the plan is used
// up, it holds a card of that window, and none was spent by itself for
// that window yet (autoReset.use) — as AutoUseZcodeReset does a ZCode
// account's. Code is "" when none was tried; the caller asks again only
// on "reset". None is spent during the peak the user kept clear.
func AutoUseZhipuReset(ctx context.Context, id string) (ResetOutcome, error) {
	week, _, peak := ZhipuAutoResetWhen(id)
	if !week || (peak && inZhipuPeak(time.Now())) {
		return ResetOutcome{}, nil
	}
	p, src, ok := zhipuPlanProvider(id)
	if !ok {
		return ResetOutcome{}, fmt.Errorf("no GLM plan key %q", id)
	}
	var card ResetCard
	return autoReset.use("zhipu", id, func(now time.Time) (*time.Time, bool, error) {
		ws, rs, err := zhipuKeyRead(ctx, p, src)
		if err != nil {
			return nil, false, err
		}
		kind, until := zcodeWindowUsedUp(ws, now)
		c, held := firstResetCard(rs, kind)
		card = c
		return until, held, nil
	}, func() (ResetOutcome, error) {
		out, err := zhipuKeyUse(ctx, p, src, card)
		if err != nil || out.Code != "reset" {
			return out, err
		}
		zhipuKeyRenew(ctx, p, src)
		noteResetUsed("zhipu", id, ResetUsedWeek, out)
		return out, nil
	})
}

// SpendExpiringZhipuResets goes over the GLM plan keys the user lets
// spend their reset cards and spends one about to run out unused, as
// SpendExpiringZcodeResets does ZCode's: as late as is safe, or at once
// when the key is held up past then — and for one kept off the peak,
// never during it.
func SpendExpiringZhipuResets(ctx context.Context) {
	for _, id := range settings.Load().ZhipuAutoReset {
		if ctx.Err() != nil {
			return
		}
		_, atBoth, peak := ZhipuAutoResetWhen(id)
		if !atBoth {
			continue // kept to a window used up, as Codex's are (#2078)
		}
		p, src, ok := zhipuPlanProvider(id)
		if !ok {
			continue // the provider or its key is gone; the say lapses with it
		}
		out, err := expiring.check("zhipu", id, time.Now(), func() ([]QuotaWindow, *ResetCredits, error) {
			return zhipuKeyRead(ctx, p, src)
		}, func() (ResetOutcome, error) {
			// one spend at a time, with a window's used-up one too
			autoReset.Lock()
			defer autoReset.Unlock()
			card, held := firstResetCard(zhipuPersonalResets(ctx, zcodeRoot(src.url), p.Key), "")
			if !held {
				return ResetOutcome{}, nil
			}
			out, err := zhipuKeyUse(ctx, p, src, card)
			if err != nil || out.Code != "reset" {
				return out, err
			}
			zhipuKeyRenew(ctx, p, src)
			noteResetUsed("zhipu", id, ResetUsedExpiring, out)
			return out, nil
		}, peak)
		switch {
		case err != nil:
			log.Printf("zhipu reset card about to run out: %s: %v", id, err)
		case out.Code != "":
			log.Printf("zhipu reset card about to run out: %s used it: %s", id, out.Text())
		}
	}
}
