package finglish

import (
	"strings"
	"testing"
)

// Apostrophes side by side once recursed until the stack overflowed -- a
// crash no recover catches, found by the CEO-behaviour analysis on a real
// archived subject. Every apostrophe form, doubled, tripled and at the edges.
func TestApostrophesSideBySideDoNotRecurse(t *testing.T) {
	for _, s := range []string{
		"don''t", "a’’b", "salam'' khobi", "''", "'", "x'", "'x", "ta'min", "ta''min",
		"sefaresh ``" + strings.Repeat("'", 500) + "`` chie", "ʼʼʼ‘‘’’",
	} {
		_ = Is(s)
		_ = ToPersian(s)
	}
	if got := ToPersian("ta'min konande"); got == "" {
		t.Error("an apostrophe inside a word stopped being part of it")
	}
}
