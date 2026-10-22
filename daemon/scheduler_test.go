package main

import (
	"strings"
	"testing"
	"time"

	"autoipo/internal/flow"
	"autoipo/internal/schedule"
)

func newTestScheduler(env *testEnv) *scheduler {
	return &scheduler{
		store:   env.bot.schedules,
		flows:   env.bot.flows,
		secrets: env.bot.secrets,
		runner:  env.bot.runner,
		out:     env.out,
		log:     env.bot.log,
	}
}

func TestSchedulerRunsDueEntry(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, nil)
	f := testFlow("Nightly")
	saveFlows(t, f)
	addUser(t, env.bot.secrets, "Bob", testPAN)
	due, err := env.bot.schedules.Add(schedule.Entry{FlowID: f.ID, FlowName: f.Name, UserPAN: testPAN, UserName: "Bob", At: env.now.Add(-time.Minute), Source: "app"})
	if err != nil {
		t.Fatal(err)
	}
	later, err := env.bot.schedules.Add(schedule.Entry{FlowID: f.ID, FlowName: f.Name, At: env.now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	s := newTestScheduler(env)
	s.tick(t.Context(), env.now)

	entries, _ := env.bot.schedules.List()
	byID := map[string]schedule.Entry{}
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	if got := byID[due.ID]; got.Status != schedule.StatusDone || !strings.HasPrefix(got.Result, "succeeded in") || got.FinishedAt.IsZero() {
		t.Fatalf("due entry = %+v", got)
	}
	if got := byID[later.ID]; got.Status != schedule.StatusPending {
		t.Fatalf("future entry must stay pending: %+v", got)
	}
	texts := env.out.allTexts()
	if len(texts) != 2 || texts[0] != "starting scheduled run Nightly as Bob ("+due.ID+")" || !strings.Contains(texts[1], "Nightly succeeded at") || !strings.Contains(texts[1], "(as Bob)") {
		t.Fatalf("texts = %q", texts)
	}
	last, err := flow.ReadLastRun()
	if err != nil || last.Source != "schedule" || last.User != "Bob" {
		t.Fatalf("last run = %+v, %v", last, err)
	}
	if env.bot.runner.busy() || flow.RunActive() {
		t.Fatal("run lock must be released")
	}
	s.tick(t.Context(), env.now)
	if len(env.out.allTexts()) != 2 {
		t.Fatal("a second tick must not find anything due")
	}
}

func TestSchedulerFailsEntriesThatCannotStart(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, nil)
	f := testFlow("Nightly")
	saveFlows(t, f)
	ghostFlow, _ := env.bot.schedules.Add(schedule.Entry{FlowID: "ghost", FlowName: "Deleted", At: env.now.Add(-2 * time.Minute)})
	ghostUser, _ := env.bot.schedules.Add(schedule.Entry{FlowID: f.ID, FlowName: f.Name, UserPAN: testPAN, At: env.now.Add(-time.Minute)})

	s := newTestScheduler(env)
	s.tick(t.Context(), env.now)
	s.tick(t.Context(), env.now)

	entries, _ := env.bot.schedules.List()
	for _, entry := range entries {
		switch entry.ID {
		case ghostFlow.ID:
			if entry.Status != schedule.StatusFailed || !strings.Contains(entry.Result, `flow "Deleted" no longer exists`) {
				t.Errorf("ghost flow entry = %+v", entry)
			}
		case ghostUser.ID:
			if entry.Status != schedule.StatusFailed || !strings.Contains(entry.Result, "user ABCDE••••F no longer exists") {
				t.Errorf("ghost user entry = %+v", entry)
			}
		}
	}
	texts := strings.Join(env.out.allTexts(), "\n")
	if !strings.Contains(texts, "could not start: flow \"Deleted\" no longer exists") || strings.Contains(texts, testPAN) {
		t.Fatalf("texts = %q", texts)
	}
}

func TestSchedulerFailureReportsScreenshot(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{fail: true, shot: []byte("png")}, nil)
	f := testFlow("Nightly")
	saveFlows(t, f)
	entry, _ := env.bot.schedules.Add(schedule.Entry{FlowID: f.ID, FlowName: f.Name, At: env.now.Add(-time.Minute)})
	newTestScheduler(env).tick(t.Context(), env.now)
	entries, _ := env.bot.schedules.List()
	if entries[0].ID != entry.ID || entries[0].Status != schedule.StatusFailed || entries[0].Result != "open failed: element not found" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if docs := env.out.allDocs(); len(docs) != 1 || docs[0].caption != "open failed: element not found" {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestSchedulerWaitsWhileAnotherRunIsActive(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{block: true}, nil)
	f := testFlow("Slow")
	saveFlows(t, f)
	if _, err := env.bot.schedules.Add(schedule.Entry{FlowID: f.ID, FlowName: f.Name, At: env.now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	env.send(t, "/run Slow")
	s := newTestScheduler(env)
	s.tick(t.Context(), env.now)
	entries, _ := env.bot.schedules.List()
	if entries[0].Status != schedule.StatusPending {
		t.Fatalf("entry must stay pending while a run is active: %+v", entries[0])
	}
	if texts := env.out.allTexts(); len(texts) != 1 {
		t.Fatalf("scheduler must stay silent while waiting, got %q", texts)
	}
	env.bot.runner.stop()
	env.bot.runner.wait()
}

func TestSchedulerRecoversInterruptedEntries(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, nil)
	entry, _ := env.bot.schedules.Add(schedule.Entry{FlowID: "f", FlowName: "Flow", At: env.now.Add(-time.Minute)})
	if _, ok, err := env.bot.schedules.ClaimDue(env.now); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	newTestScheduler(env).recoverInterrupted()
	entries, _ := env.bot.schedules.List()
	if entries[0].ID != entry.ID || entries[0].Status != schedule.StatusFailed || entries[0].Result != "interrupted by a daemon restart" {
		t.Fatalf("entry = %+v", entries[0])
	}
}
