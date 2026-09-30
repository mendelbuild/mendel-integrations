package conformance

import (
	"context"
	"strings"
	"testing"
)

// On someone's real account the suite publishes nothing, refreshes and
// revokes nothing, and runs no boundary case: it reads, drafts once per
// kind, shows the draft is not live, takes it back, and says what it left
// alone. A good wrapper passes, and the report says it was read-only.
func TestAReadOnlyRunLeavesTheAccountAsItFoundIt(t *testing.T) {
	p := &publisher{drafts: true, lists: true, calls: map[string]int{}}
	r := runPublisherAs(p, func(t *Target) { t.ReadOnly, t.Revoke = true, true })
	if !r.Passed() || !r.ReadOnly {
		t.Fatalf("a good wrapper, read-only: %+v", r.Checks)
	}
	for _, forbidden := range []string{"publish ", "authorize refresh", "authorize revoke"} {
		if p.calls[forbidden] > 0 {
			t.Errorf("%q was called on a real account", forbidden)
		}
	}
	if p.calls["draft "] != 1 || p.calls["retract "] != 1 || len(p.posts)+len(p.draftsHeld) != 0 {
		t.Errorf("one draft, taken back: calls %v, left %d live and %d drafts", p.calls, len(p.posts), len(p.draftsHeld))
	}
	var warned string
	for _, c := range r.Checks {
		if c.Name == "a read-only run leaves the account as it found it" {
			warned = c.Detail
		}
	}
	for _, named := range []string{"publish", "read_back", "read_metrics", "list_owned", "authorize revoke"} {
		if !strings.Contains(warned, named) {
			t.Errorf("the warning does not name %s: %q", named, warned)
		}
	}
}

// A draft that is live the moment it is made fails, and is taken back at
// once, because on a real account it is already showing.
func TestAReadOnlyDraftThatIsLiveFailsAndIsTakenBack(t *testing.T) {
	p := &publisher{draftIsLive: true, calls: map[string]int{}}
	r := runPublisherAs(p, func(t *Target) { t.ReadOnly = true })
	if r.Passed() {
		t.Fatal("a live draft passed")
	}
	if len(p.posts) != 0 {
		t.Errorf("the live draft was left up: %v", p.posts)
	}
}

// A wrapper with no status cannot show its draft is not live, so on a real
// account it is not drafted at all, and says why.
func TestAReadOnlyRunDoesNotDraftWhatItCannotCheck(t *testing.T) {
	m := (&publisher{drafts: true}).manifest()
	m.Verbs["status"] = m.Verbs["append_update"] // declined
	s := &suite{t: Target{ReadOnly: true}}
	s.readOnlyDrafts(context.Background(), m) // returns before calling anything: no Runner is set
	if len(s.report.Checks) != 0 || !strings.Contains(strings.Join(s.notExercised, ";"), "draft (without status") {
		t.Errorf("drafted without a way to check it: %v %v", s.report.Checks, s.notExercised)
	}
}

// A fake venue answers authorize on the loopback over http, which passes
// only when the target says the venue is a fake.
func TestAFakeVenueMayAuthorizeOnTheLoopback(t *testing.T) {
	p := &publisher{loopbackURL: true}
	if r := runPublisherAs(p, func(t *Target) { t.FakeVenue = true }); !r.Passed() || !r.FakeVenue {
		t.Errorf("a fake venue's loopback URL: %+v", r.Checks)
	}
	if r := runPublisher(&publisher{loopbackURL: true}); r.Passed() {
		t.Error("an http loopback URL passed against a real venue")
	}
}
