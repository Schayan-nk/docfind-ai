package agent

import (
	"context"
	"testing"
)

// who is a test Principal holding the listed permissions.
type who struct {
	name  string
	perms map[string]bool
}

func (w who) Username() string  { return w.name }
func (w who) Can(p string) bool { return w.perms[p] }
func person(perms ...string) who {
	m := map[string]bool{}
	for _, p := range perms {
		m[p] = true
	}
	return who{name: "tester", perms: m}
}

func noop(context.Context, *Call) (*Output, error) { return &Output{}, nil }

func TestRegisterRefusesAToolThatWouldWeakenARule(t *testing.T) {
	base := Tool{Name: "ok_tool", Title: "t", Class: Read, Permissions: []string{"p"}, Audit: "a", Run: noop}
	cases := map[string]func(*Tool){
		"bad name":              func(t *Tool) { t.Name = "Bad Name" },
		"no function":           func(t *Tool) { t.Run = nil },
		"no class":              func(t *Tool) { t.Class = "" },
		"writes without asking": func(t *Tool) { t.Class, t.Confirm = Execute, false },
		"writes and retries":    func(t *Tool) { t.Class, t.Confirm, t.Retries = Execute, true, 1 },
		"no permission":         func(t *Tool) { t.Permissions = nil },
		"no audit event":        func(t *Tool) { t.Audit = "" },
		"bad parameter name":    func(t *Tool) { t.Params = []Param{{Name: "Drop Table", Kind: Text}} },
		"parameter twice":       func(t *Tool) { t.Params = []Param{{Name: "q", Kind: Text}, {Name: "q", Kind: Text}} },
	}
	for name, spoil := range cases {
		tool := base
		spoil(&tool)
		if err := NewRegistry().Register(tool); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
	r := NewRegistry()
	r.MustRegister(base)
	if err := r.Register(base); err == nil {
		t.Error("the same name registered twice")
	}
}

func TestAnExecuteToolIsNeverAPlanStep(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(Tool{Name: "send_it", Title: "t", Class: Execute, Confirm: true,
		Permissions: []string{"p"}, Audit: "a", Run: noop})
	if _, err := r.Plannable("send_it"); err == nil {
		t.Error("an Execute tool is plannable")
	}
	if _, err := r.Plannable("made_up"); err == nil {
		t.Error("an unregistered tool is plannable")
	}
}

func TestValidateIsStrictAboutEveryArgument(t *testing.T) {
	tool := &Tool{Title: "آزمون", Params: []Param{
		{Name: "ref", Label: "شماره", Kind: Ref, Required: true},
		{Name: "n", Label: "تعداد", Kind: Int, Min: 1, Max: 50},
		{Name: "to", Label: "گیرنده", Kind: Email},
		{Name: "phone", Label: "شماره تلفن", Kind: Phone},
		{Name: "q", Label: "عبارت", Kind: Text, Max: 10},
		{Name: "mode", Label: "حالت", Kind: Choice, Choices: []string{"short", "full"}},
		{Name: "at", Label: "زمان", Kind: Time},
	}}
	good, err := tool.Validate(map[string]string{"ref": "۶۳۸۳۶۳۸/۹", "n": "۲۰", "to": "a@b.de",
		"phone": "+98 912-000-1111", "q": "آلفا", "mode": "full", "at": "2026-09-26T09:00:00+03:30"})
	if err != nil {
		t.Fatal(err)
	}
	// The identifier's digits are folded and NOTHING else: the separator the
	// person used is kept, so it is shown back the way they said it.
	if good["ref"] != "6383638/9" || good["n"] != "20" || good["phone"] != "+989120001111" ||
		good["at"] != "2026-09-26T05:30:00Z" {
		t.Errorf("cleaned wrongly: %v", good)
	}

	bad := []map[string]string{
		{},                                   // required ref missing
		{"ref": "1234", "extra": "x"},        // undeclared argument
		{"ref": "1234'; DROP TABLE users--"}, // an identifier is not SQL
		{"ref": "12"},                        // too few digits to be one
		{"ref": "1234", "n": "0"},            // below the bound
		{"ref": "1234", "n": "51"},           // above it
		{"ref": "1234", "n": "twenty"},       // not a number
		{"ref": "1234", "to": "a@b.de\r\nBcc: x@y.z"}, // a header smuggled in
		{"ref": "1234", "to": "Name <a@b.de>"},        // one bare address only
		{"ref": "1234", "phone": "call me"},
		{"ref": "1234", "q": "یک عبارت خیلی بلند"},
		{"ref": "1234", "q": "a\x00b"}, // a control character
		{"ref": "1234", "mode": "everything"},
		{"ref": "1234", "at": "tomorrow"},
	}
	for _, args := range bad {
		if _, err := tool.Validate(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestLatinDigitsLeavesLettersAlone(t *testing.T) {
	if got := LatinDigits("سفارش ۱۲۳۴ و ٥٦"); got != "سفارش 1234 و 56" {
		t.Errorf("got %q", got)
	}
}
