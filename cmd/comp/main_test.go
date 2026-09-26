package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want int
	}{
		{"valid", "let x = 1; x + 1;", 0},
		{"parse error", "let = =", 1},
		{"compile error", "undefinedThing", 1},
		{"runtime error", "1 / 0", 1},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if got := Run(context.Background(), tt.src, false, &out, &errOut); got != tt.want {
				t.Errorf("exit code = %d, want %d (stderr: %s)", got, tt.want, errOut.String())
			}
		})
	}
}

func TestRunProducesOutput(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run(context.Background(), `puts("hello")`, false, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("got %q", out.String())
	}
}

func TestDisassembly(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run(context.Background(), "let add = fn(a, b) { a + b }; add(1, 2);", true, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{"== main ==", "OpClosure", "OpCall", "fn add", "== constants =="} {
		if !strings.Contains(got, want) {
			t.Errorf("disassembly missing %q:\n%s", want, got)
		}
	}
}

func TestREPLKeepsState(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("let x = 21\nx * 2\n:quit\n")
	if err := repl(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "42") {
		t.Errorf("state not kept across lines:\n%s", out.String())
	}
}

func TestREPLSurvivesErrors(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("let = =\nundefinedThing\n1 / 0\n2 + 2\n:quit\n")
	if err := repl(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"parse error", "compile error", "runtime error", "4"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}
