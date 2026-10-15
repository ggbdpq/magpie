package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The group page keeps the row picked in view: a group with more members
// and rules than the terminal has rows can't scroll the highlight off and
// freeze the page, as the library page did (#2220) and the agents page
// before it. What the terminal shows of a view taller than itself is its
// last h lines — the top scrolls away.
func TestGroupViewKeepsTheRowPicked(t *testing.T) {
	home(t)
	var members []string
	for i := 0; i < 30; i++ {
		members = append(members, fmt.Sprintf("p/m-%02d", i))
	}
	if err := provider.SaveGroup(provider.Group{ID: "many", Name: "Many", Members: members}); err != nil {
		t.Fatal(err)
	}
	m := press(t, model{w: 120, h: 24}, "3", "enter")
	if m.mode != modeGroup {
		t.Fatalf("mode %d, want the group page", m.mode)
	}
	screen := func(v string, h int) string {
		lines := strings.Split(v, "\n")
		if len(lines) > h {
			lines = lines[len(lines)-h:]
		}
		return strings.Join(lines, "\n")
	}
	// a plain 80×24 terminal: thirty members are rows past it
	for _, pick := range []int{0, 4, 8, 15, 22, 29} {
		m.gsel = pick
		if v := screen(m.View(), m.h); !strings.Contains(v, fmt.Sprintf("m-%02d", pick)) {
			t.Fatalf("member %d picked, but the screen stops short of it:\n%s", pick, v)
		}
	}
	// the page starts at its top while it all fits
	m.h = 60
	m.gsel = 29
	if v := screen(m.View(), m.h); !strings.Contains(v, "m-00") {
		t.Fatalf("a tall terminal dropped the top of the list:\n%s", v)
	}
}

// A pick among the rules stays in view too, with the models' tail above it.
func TestGroupViewKeepsTheRulePicked(t *testing.T) {
	home(t)
	members := []string{"p/m-00"}
	var rules []provider.Rule
	for i := 0; i < 24; i++ {
		rules = append(rules, provider.Rule{Use: "p/m-00", Agents: []string{fmt.Sprintf("agent-%02d", i)}})
	}
	if err := provider.SaveGroup(provider.Group{ID: "ruled", Name: "Ruled", Members: members, Rules: rules}); err != nil {
		t.Fatal(err)
	}
	m := press(t, model{w: 120, h: 24}, "3", "enter")
	if m.mode != modeGroup {
		t.Fatalf("mode %d, want the group page", m.mode)
	}
	screen := func(v string, h int) string {
		lines := strings.Split(v, "\n")
		if len(lines) > h {
			lines = lines[len(lines)-h:]
		}
		return strings.Join(lines, "\n")
	}
	for i, pick := range []int{0, 8, 23} {
		m.gsel = len(members) + pick
		if v := screen(m.View(), m.h); !strings.Contains(v, fmt.Sprintf("agent-%02d", pick)) {
			t.Fatalf("rule %d picked, but the screen stops short of it (try %d):\n%s", pick, i, v)
		}
	}
}
