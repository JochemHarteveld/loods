package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func store(t *testing.T) *ClaimStore {
	t.Helper()
	return newClaimStore(filepath.Join(t.TempDir(), "claims.json"))
}

func TestClaimTakeHeartbeatRelease(t *testing.T) {
	cs := store(t)
	c, err := cs.Take(Claim{Project: "g/p", Task: 4, Text: "ship it", Agent: "claude", Session: "sess-one"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Beat.IsZero() || c.ClaimedAt.IsZero() {
		t.Fatalf("claim without timestamps: %+v", c)
	}

	// A second store (the server reading what the CLI wrote) sees it.
	other := newClaimStore(cs.path)
	other.Refresh()
	got, ok := other.Get("g/p", 4)
	if !ok || got.Text != "ship it" || got.Stale {
		t.Fatalf("read back %+v (ok %v)", got, ok)
	}
	if list := other.Of("g/p"); len(list) != 1 || list[0].Task != 4 {
		t.Fatalf("Of = %+v", list)
	}

	if _, err := cs.Heartbeat("g/p", 4, "sess-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Heartbeat("g/p", 4, "someone-else"); err == nil {
		t.Error("another session got to heartbeat this claim")
	}
	if _, err := cs.Heartbeat("g/p", 9, "sess-one"); err == nil {
		t.Error("heartbeat on an unclaimed task was accepted")
	}

	if _, had, err := cs.Release("g/p", 4); err != nil || !had {
		t.Fatalf("release: had %v, err %v", had, err)
	}
	if _, ok := cs.Get("g/p", 4); ok {
		t.Error("claim survived its release")
	}
	if _, had, _ := cs.Release("g/p", 4); had {
		t.Error("releasing twice reported a claim")
	}
}

func TestClaimRefusesLiveWorkUnlessStolen(t *testing.T) {
	cs := store(t)
	first := Claim{Project: "g/p", Task: 4, Agent: "claude", Session: "sess-one"}
	started, err := cs.Take(first, false)
	if err != nil {
		t.Fatal(err)
	}

	// Same session: just carries on, and keeps the original start time, so the
	// board keeps showing how long this work has been going.
	again, err := cs.Take(first, false)
	if err != nil {
		t.Fatalf("the same session could not retake its own claim: %v", err)
	}
	if !again.ClaimedAt.Equal(started.ClaimedAt) {
		t.Errorf("retaking restarted the clock: %v, was %v", again.ClaimedAt, started.ClaimedAt)
	}

	second := Claim{Project: "g/p", Task: 4, Agent: "claude", Session: "sess-two"}
	if _, err = cs.Take(second, false); err == nil {
		t.Fatal("a second session took a task that was being worked on")
	}
	if !strings.Contains(err.Error(), "--steal") {
		t.Errorf("error should say how to take it over: %v", err)
	}
	if got, err := cs.Take(second, true); err != nil || got.Session != "sess-two" {
		t.Fatalf("steal: %+v, %v", got, err)
	}
}

func TestStaleClaimCanBeTakenOver(t *testing.T) {
	cs := store(t)
	if _, err := cs.Take(Claim{Project: "g/p", Task: 4, Agent: "claude", Session: "gone"}, false); err != nil {
		t.Fatal(err)
	}
	// Backdate the heartbeat the way an abandoned session leaves it.
	if err := cs.update(func(m map[string]Claim) error {
		c := m["g/p#4"]
		c.Beat = time.Now().Add(-2 * claimStale)
		m["g/p#4"] = c
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c, _ := cs.Get("g/p", 4); !c.Stale {
		t.Fatalf("claim without a heartbeat is not stale: %+v", c)
	}
	if _, err := cs.Take(Claim{Project: "g/p", Task: 4, Agent: "claude", Session: "fresh"}, false); err != nil {
		t.Errorf("a stale claim should not block new work: %v", err)
	}
	if c, _ := cs.Get("g/p", 4); c.Stale || c.Session != "fresh" {
		t.Errorf("after taking over: %+v", c)
	}
}

func TestClaimsDropWithTheirProject(t *testing.T) {
	cs := store(t)
	for _, rel := range []string{"g/p", "gone/project"} {
		if _, err := cs.Take(Claim{Project: rel, Task: 1, Agent: "claude"}, false); err != nil {
			t.Fatal(err)
		}
	}
	cs.ReleaseGone([]string{"g/p"})
	if _, ok := cs.Get("gone/project", 1); ok {
		t.Error("claim of a project that left the board survived")
	}
	if _, ok := cs.Get("g/p", 1); !ok {
		t.Error("claim of a project still on the board was dropped")
	}
	cs.ReleaseGone(nil) // an empty board is a failed scan, not an empty world
	if _, ok := cs.Get("g/p", 1); !ok {
		t.Error("an empty project list cleared the claims")
	}
}

func TestClaimNeedsProjectAndTask(t *testing.T) {
	cs := store(t)
	if _, err := cs.Take(Claim{Task: 1}, false); err == nil {
		t.Error("claim without a project accepted")
	}
	if _, err := cs.Take(Claim{Project: "g/p"}, false); err == nil {
		t.Error("claim without a task number accepted")
	}
}
