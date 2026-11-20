package threadtitle

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapsuleName(t *testing.T) {
	cases := []struct{ message, want string }{
		{"Please fix the header layout so it does not wrap onto two lines.", "Please fix the header layout so it does not"},
		{"#  Fix the **header**.\nThen stop.", "Fix the header"},
		{"Fix the header so it stays on one line.", "Fix the header so it stays on one line"},
		{"   \n\t ", ""},
		{"`*_~>", ""},
		{"Fix‮ the​ header\u0007", "Fix the header"},
		{"Supercalifragilisticexpialidociousandmoreandmoreandmore words", "Supercalifragilisticexpialidociousandmoreandmore"},
	}
	for _, tc := range cases {
		if got := CapsuleName(tc.message); got != tc.want {
			t.Errorf("CapsuleName(%q) = %q, want %q", tc.message, got, tc.want)
		}
	}
}

func TestCapsuleNameFitsTheAPI(t *testing.T) {
	name := CapsuleName(strings.Repeat("𝔐", 80))
	if len(name) > maxNameBytes || !utf8.ValidString(name) {
		t.Fatalf("name is %d bytes or invalid UTF-8: %q", len(name), name)
	}
	if name == "" {
		t.Fatal("name is empty")
	}
}
