package cmd

import "testing"

func TestMergeTaskFlagsCanRemoveOverride(t *testing.T) {
	existing := map[string]string{
		"implementation":  "grok",
		"security_review": "fable",
	}
	got, err := mergeTaskFlags(existing, []string{"security_review="})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["security_review"]; ok {
		t.Errorf("security_review override was not removed: %v", got)
	}
	if got["implementation"] != "grok" {
		t.Errorf("unrelated override changed: %v", got)
	}
	if existing["security_review"] != "fable" {
		t.Errorf("input map was mutated: %v", existing)
	}
}
