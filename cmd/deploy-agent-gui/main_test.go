package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapOutputKeepsTailAndMarksTruncation(t *testing.T) {
	tail := "tail output"
	got := capOutput(strings.Repeat("x", maxOutputRunes) + tail)

	if !strings.HasPrefix(got, outputTruncatedNotice) {
		t.Fatalf("output prefix = %q, want truncation notice", got[:len(outputTruncatedNotice)])
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatalf("output suffix = %q, want %q", got[len(got)-len(tail):], tail)
	}
	if utf8.RuneCountInString(got) > maxOutputRunes {
		t.Fatalf("output length = %d, want <= %d", utf8.RuneCountInString(got), maxOutputRunes)
	}
}
