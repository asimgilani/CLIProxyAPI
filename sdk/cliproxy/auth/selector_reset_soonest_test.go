package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const weeklyResetSignal = "Anthropic-Ratelimit-Unified-7d-Reset"

func resetSoonestAuth(id string, resetIn time.Duration, now time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Status:   StatusActive,
		Quota: QuotaState{Signals: map[string]string{
			weeklyResetSignal: strconv.FormatInt(now.Add(resetIn).Unix(), 10),
		}},
	}
}

func pickResetSoonest(t *testing.T, now time.Time, auths ...*Auth) string {
	t.Helper()
	selector := &ResetSoonestSelector{now: func() time.Time { return now }}
	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	return got.ID
}

func TestResetSoonestSelectorPicksEarliestWeeklyReset(t *testing.T) {
	t.Parallel()
	now := time.Now()
	day := 24 * time.Hour
	auths := func() []*Auth {
		return []*Auth{
			resetSoonestAuth("a-two-days", 2*day, now),
			resetSoonestAuth("b-six-days", 6*day, now),
			resetSoonestAuth("c-one-day", day, now),
		}
	}

	if got := pickResetSoonest(t, now, auths()...); got != "c-one-day" {
		t.Fatalf("Pick() = %q, want c-one-day", got)
	}

	cooling := auths()
	cooling[2].Quota.Exceeded = true
	cooling[2].Quota.Reason = "credential_quota"
	cooling[2].Quota.NextRecoverAt = now.Add(time.Hour)
	if got := pickResetSoonest(t, now, cooling...); got != "a-two-days" {
		t.Fatalf("Pick() with soonest cooling down = %q, want a-two-days", got)
	}
}

func TestResetSoonestSelectorUnknownResetsGoFirst(t *testing.T) {
	t.Parallel()
	now := time.Now()
	day := 24 * time.Hour

	noSignal := &Auth{ID: "z-no-signal", Provider: "claude", Status: StatusActive}
	if got := pickResetSoonest(t, now, resetSoonestAuth("a-known", day, now), noSignal); got != "z-no-signal" {
		t.Fatalf("Pick() = %q, want the credential without a signal", got)
	}

	past := resetSoonestAuth("m-past", -time.Hour, now)
	if got := pickResetSoonest(t, now, resetSoonestAuth("a-known", day, now), past); got != "m-past" {
		t.Fatalf("Pick() = %q, want the credential whose reset already passed", got)
	}

	garbage := &Auth{ID: "n-garbage", Provider: "claude", Status: StatusActive, Quota: QuotaState{Signals: map[string]string{weeklyResetSignal: "soon"}}}
	if got := pickResetSoonest(t, now, resetSoonestAuth("a-known", day, now), garbage); got != "n-garbage" {
		t.Fatalf("Pick() = %q, want the credential with an unparseable reset", got)
	}

	if got := pickResetSoonest(t, now, noSignal, past, resetSoonestAuth("a-known", day, now)); got != "m-past" {
		t.Fatalf("Pick() among unknowns = %q, want the lowest ID m-past", got)
	}
}

func TestResetSoonestSelectorAcceptsRFC3339AndBreaksTiesByID(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	reset := now.Add(48 * time.Hour)

	rfc := &Auth{ID: "a-rfc", Provider: "claude", Status: StatusActive, Quota: QuotaState{Signals: map[string]string{
		weeklyResetSignal: now.Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}}}
	if got := pickResetSoonest(t, now, resetSoonestAuth("b-later", 72*time.Hour, now), rfc); got != "a-rfc" {
		t.Fatalf("Pick() = %q, want a-rfc", got)
	}

	tieB := resetSoonestAuth("b-tie", 0, now)
	tieA := resetSoonestAuth("a-tie", 0, now)
	tieA.Quota.Signals[weeklyResetSignal] = strconv.FormatInt(reset.Unix(), 10)
	tieB.Quota.Signals[weeklyResetSignal] = strconv.FormatInt(reset.Unix(), 10)
	if got := pickResetSoonest(t, now, tieB, tieA); got != "a-tie" {
		t.Fatalf("Pick() on equal resets = %q, want a-tie", got)
	}
}

func TestResetSoonestSelectorKeepsPriorityTierHard(t *testing.T) {
	t.Parallel()
	now := time.Now()
	day := 24 * time.Hour

	high := resetSoonestAuth("high-late", 6*day, now)
	high.Attributes = map[string]string{"priority": "5"}
	low := resetSoonestAuth("low-soon", day, now)
	low.Attributes = map[string]string{"priority": "1"}
	if got := pickResetSoonest(t, now, low, high); got != "high-late" {
		t.Fatalf("Pick() = %q, want the higher priority tier", got)
	}
}

func TestResetSoonestManagerRoutesByWeeklyResetAndFailsOver(t *testing.T) {
	const model = "claude-reset-soonest-model"
	manager := NewManager(nil, &ResetSoonestSelector{}, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
	if manager.useSchedulerFastPath() {
		t.Fatal("useSchedulerFastPath() = true, want live-auth selection for reset-soonest")
	}

	now := time.Now()
	day := 24 * time.Hour
	for _, auth := range []*Auth{
		resetSoonestAuth("rs-mgr-a-two-days", 2*day, now),
		resetSoonestAuth("rs-mgr-b-six-days", 6*day, now),
		resetSoonestAuth("rs-mgr-c-one-day", day, now),
	} {
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}

	pick := func() string {
		t.Helper()
		got, _, err := manager.pickNext(context.Background(), "claude", model, cliproxyexecutor.Options{}, nil)
		if err != nil {
			t.Fatalf("pickNext() error = %v", err)
		}
		return got.ID
	}
	if got := pick(); got != "rs-mgr-c-one-day" {
		t.Fatalf("pickNext() = %q, want rs-mgr-c-one-day", got)
	}

	retryAfter := time.Hour
	manager.MarkResult(context.Background(), Result{
		AuthID: "rs-mgr-c-one-day", Provider: "claude", Model: model,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
		RetryAfter: &retryAfter,
	})
	if got := pick(); got != "rs-mgr-a-two-days" {
		t.Fatalf("pickNext() after the soonest cooled down = %q, want rs-mgr-a-two-days", got)
	}
}
