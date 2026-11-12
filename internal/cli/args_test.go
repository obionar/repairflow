package cli

import (
	"context"
	"strings"
	"testing"

	"repairflow/internal/errs"
)

func TestParseArgsValueAndBoolFlags(t *testing.T) {
	p, err := parseArgs([]string{"--name", "Ada", "--email=a@b.c", "--reopen", "42"},
		[]string{"name", "email"}, []string{"reopen"})
	if err != nil {
		t.Fatal(err)
	}
	if p.get("name") != "Ada" || p.get("email") != "a@b.c" || !p.flag("reopen") {
		t.Fatalf("parsed: %+v", p)
	}
	if len(p.positional) != 1 || p.positional[0] != "42" {
		t.Fatalf("positional: %v", p.positional)
	}
}

func TestParseArgsRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"--bogus"}},
		{"missing value", []string{"--name"}},
		{"empty inline value", []string{"--name="}},
		{"value looks like flag", []string{"--name", "--other"}},
		{"bool with value", []string{"--reopen=1"}},
		{"duplicate value flag", []string{"--name", "a", "--name", "b"}},
	}
	for _, tc := range cases {
		_, err := parseArgs(tc.args, []string{"name", "other"}, []string{"reopen"})
		if err == nil {
			t.Errorf("%s: expected refusal", tc.name)
			continue
		}
		if errs.CodeOf(err) != errs.CodeUsage {
			t.Errorf("%s: expected usage_error, got %v", tc.name, err)
		}
	}
}

func TestParseArgsPositionalsAfterDoubleDash(t *testing.T) {
	p, err := parseArgs([]string{"--", "--not-a-flag"}, nil, nil)
	if err != nil || len(p.positional) != 1 {
		t.Fatalf("double dash: %+v %v", p, err)
	}
}

func TestPaginationArgs(t *testing.T) {
	p, err := parseArgs([]string{"--page", "3", "--per-page=50"}, []string{"page", "per-page"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, perPage, err := paginationArgs(p)
	if err != nil || page != 3 || perPage != 50 {
		t.Fatalf("pagination: page=%d per-page=%d err=%v", page, perPage, err)
	}
	for _, raw := range [][]string{{"--page", "0"}, {"--per-page", "bad"}} {
		p, err := parseArgs(raw, []string{"page", "per-page"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := paginationArgs(p); errs.CodeOf(err) != errs.CodeValidation {
			t.Fatalf("pagination %v: %v", raw, err)
		}
	}
}

func TestHelpListsCommands(t *testing.T) {
	var out, errb strings.Builder
	if code := Run(context.Background(), []string{"help"}, strings.NewReader(""), &out, &errb, func(string) string { return "" }); code != 0 {
		t.Fatalf("help exit %d", code)
	}
	for _, want := range []string{"customer add", "repair add", "repair entry", "seed demo", "doctor", "status", "REPAIRFLOW_DEBUG"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help missing %q", want)
		}
	}
}
