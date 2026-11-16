package domain

import "testing"

func TestCheckBudgetAndReworkStop(t *testing.T) {
	if CheckBudget(0) != 1 || CheckBudget(3) != 3 {
		t.Fatalf("budget = %d %d", CheckBudget(0), CheckBudget(3))
	}
	// The first failure of a 3-try budget still goes back to the agent.
	if !ShouldAutoRework(true, false, CheckTriggerTurnIdle, CapsuleMain, 0, 3) {
		t.Fatal("first failure should rework")
	}
	if !ShouldAutoRework(true, false, CheckTriggerTurnIdle, CapsuleMain, 1, 3) {
		t.Fatal("second try should still rework")
	}
	if ShouldAutoRework(true, false, CheckTriggerTurnIdle, CapsuleMain, 2, 3) {
		t.Fatal("third failure is the last try")
	}
	if ShouldAutoRework(true, false, CheckTriggerTurnIdle, CapsuleMain, 0, 0) {
		t.Fatal("zero attempts runs once and does not rework")
	}
	for _, trigger := range []CheckTrigger{CheckTriggerManual, CheckTriggerRunExit} {
		if ShouldAutoRework(true, false, trigger, CapsuleMain, 0, 3) {
			t.Fatalf("%s must not rework", trigger)
		}
	}
	if ShouldAutoRework(true, true, CheckTriggerTurnIdle, CapsuleMain, 0, 3) {
		t.Fatal("stopped capsule must not rework")
	}
	if ShouldAutoRework(false, false, CheckTriggerDelivery, CapsuleMain, 0, 3) {
		t.Fatal("auto rework off")
	}
	if ShouldAutoRework(true, false, CheckTriggerTurnIdle, CapsuleSub, 0, 3) {
		t.Fatal("subagent must not rework")
	}
}

func TestCheckPhaseExhaustedVersusReworking(t *testing.T) {
	if got := CheckPhaseOf(CheckFailed, true, true, false, CheckTriggerTurnIdle, CapsuleMain, 0, 3); got != CheckPhaseReworking {
		t.Fatalf("phase = %s", got)
	}
	if got := CheckPhaseOf(CheckFailed, true, true, false, CheckTriggerTurnIdle, CapsuleMain, 2, 3); got != CheckPhaseFailing {
		t.Fatalf("exhausted phase = %s", got)
	}
	if got := CheckPhaseOf(CheckPassed, false, true, false, CheckTriggerTurnIdle, CapsuleMain, 0, 3); got != CheckPhasePassed {
		t.Fatalf("passed phase = %s", got)
	}
	if got := CheckPhaseOf(CheckFailed, false, true, false, CheckTriggerManual, CapsuleMain, 0, 3); got != CheckPhaseFailing {
		t.Fatalf("manual phase = %s", got)
	}
}

func TestProjectChecksRejectsShellShapedAndUnboundedCommands(t *testing.T) {
	ok := ProjectChecks{
		Checks:            []CheckCommand{{Name: "test", Argv: []string{"make", "test"}}},
		MaxReworkAttempts: 3, AutoRework: true,
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	empty := ProjectChecks{MaxReworkAttempts: 3, AutoRework: true}
	if err := empty.Validate(); err != nil || empty.Enabled() {
		t.Fatal(err)
	}
	cases := []ProjectChecks{
		{Checks: []CheckCommand{{Name: "", Argv: []string{"make"}}}, MaxReworkAttempts: 3},
		{Checks: []CheckCommand{{Name: "test", Argv: nil}}, MaxReworkAttempts: 3},
		{Checks: []CheckCommand{{Name: "test", Argv: []string{"make", "a\x00b"}}}, MaxReworkAttempts: 3},
		{Checks: []CheckCommand{{Name: "test", TimeoutSeconds: MaxCheckTimeoutSeconds + 1, Argv: []string{"make"}}}, MaxReworkAttempts: 3},
		{MaxReworkAttempts: 11},
		{Checks: []CheckCommand{
			{Name: "test", Argv: []string{"make"}},
			{Name: "test", Argv: []string{"make"}},
		}, MaxReworkAttempts: 3},
	}
	for _, item := range cases {
		if err := item.Validate(); err == nil {
			t.Fatalf("accepted %#v", item)
		}
	}
}
