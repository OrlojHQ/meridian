package harnessadapter

import "testing"

func TestParseMockCoordination(t *testing.T) {
	plan, ok := parseMockCoordination("coordinate mock-structured 2 Review the fixture")
	if !ok || plan.harness != "mock-structured" || plan.count != 2 || plan.task != "Review the fixture" || !plan.wait {
		t.Fatalf("plan = %+v, %v", plan, ok)
	}
	if plan, ok := parseMockCoordination("coordinate-nowait mock 1 task"); !ok || plan.wait {
		t.Fatalf("nowait plan = %+v, %v", plan, ok)
	}
	for _, prompt := range []string{
		"coordinate mock 0 task",
		"coordinate mock 5 task",
		"coordinate mock two task",
		"coordinate mock 1",
		"coordinate mock 1  ",
		"please coordinate mock 1 task",
	} {
		if _, ok := parseMockCoordination(prompt); ok {
			t.Errorf("%q parsed", prompt)
		}
	}
}
