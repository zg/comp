package vm

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zg/comp/compiler"
	"github.com/zg/comp/lexer"
	"github.com/zg/comp/object"
	"github.com/zg/comp/parser"
)

func compile(t *testing.T, input string) *compiler.Bytecode {
	t.Helper()
	p := parser.New(lexer.New(input))
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors for %q:\n%s", input, strings.Join(errs, "\n"))
	}
	c := compiler.New()
	if err := c.Compile(program); err != nil {
		t.Fatalf("compile error for %q: %v", input, err)
	}
	return c.Bytecode()
}

// runVM compiles and runs input, returning the value the program left
// behind.
func runVM(t *testing.T, input string) object.Object {
	t.Helper()
	machine := New(context.Background(), compile(t, input), WithMaxSteps(50_000_000))
	if err := machine.Run(); err != nil {
		t.Fatalf("vm error for %q: %v", input, err)
	}
	return machine.LastPopped()
}

func runVMErr(t *testing.T, input string) error {
	t.Helper()
	machine := New(context.Background(), compile(t, input), WithMaxSteps(50_000_000))
	return machine.Run()
}

func wantInt(t *testing.T, obj object.Object, want int64) {
	t.Helper()
	i, ok := obj.(*object.Integer)
	if !ok {
		t.Fatalf("got %T (%s), want Integer", obj, obj.Inspect())
	}
	if i.Value != want {
		t.Errorf("got %d, want %d", i.Value, want)
	}
}

func TestIntegerArithmetic(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1", 1}, {"2", 2}, {"1 + 2", 3}, {"1 - 2", -1},
		{"1 * 2", 2}, {"4 / 2", 2}, {"7 % 3", 1},
		{"50 / 2 * 2 + 10 - 5", 55},
		{"5 * (2 + 10)", 60},
		{"-5", -5}, {"-50 + 100 + -50", 0},
		{"(5 + 10 * 2 + 15 / 3) * 2 + -10", 50},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) { wantInt(t, runVM(t, tt.in), tt.want) })
	}
}

func TestBooleanLogic(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"true", true}, {"false", false},
		{"1 < 2", true}, {"1 > 2", false},
		{"1 <= 1", true}, {"1 >= 2", false},
		{"1 == 1", true}, {"1 != 1", false},
		{"true == true", true}, {"true != false", true},
		{"(1 < 2) == true", true},
		{"!true", false}, {"!!true", true}, {"!null", true},
		{"true && true", true}, {"true && false", false},
		{"false || true", true}, {"false || false", false},
		// Truthy non-booleans must normalise to a boolean, not leak the
		// operand value.
		{"1 && 2", true},
		{"0 || 0", true}, // 0 is truthy in this language
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			b, ok := runVM(t, tt.in).(*object.Boolean)
			if !ok {
				t.Fatalf("got %T", runVM(t, tt.in))
			}
			if b.Value != tt.want {
				t.Errorf("got %v, want %v", b.Value, tt.want)
			}
		})
	}
}

// Short-circuiting must be real: the right operand must not run. If it did,
// these would fail on the undefined variable at compile time or divide by
// zero at run time.
func TestShortCircuit(t *testing.T) {
	wantInt(t, runVM(t, "let x = 0; false && (x = 1); x;"), 0)
	wantInt(t, runVM(t, "let y = 0; true || (y = 1); y;"), 0)
	// And it must not skip when the left does not decide the result.
	wantInt(t, runVM(t, "let z = 0; true && (z = 1); z;"), 1)
}

func TestConditionals(t *testing.T) {
	tests := []struct {
		in   string
		want any
	}{
		{"if (true) { 10 }", int64(10)},
		{"if (true) { 10 } else { 20 }", int64(10)},
		{"if (false) { 10 } else { 20 }", int64(20)},
		{"if (1 < 2) { 10 }", int64(10)},
		{"if (1 > 2) { 10 } else { 20 }", int64(20)},
		// A missing else must still yield a value.
		{"if (1 > 2) { 10 }", nil},
		{"if (false) { 10 }", nil},
		{"if ((if (false) { 10 })) { 10 } else { 20 }", int64(20)},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := runVM(t, tt.in)
			if tt.want == nil {
				if got != Null {
					t.Errorf("got %s, want null", got.Inspect())
				}
				return
			}
			wantInt(t, got, tt.want.(int64))
		})
	}
}

func TestGlobalsAndAssignment(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"let one = 1; one", 1},
		{"let one = 1; let two = 2; one + two", 3},
		{"let one = 1; let two = one + one; one + two", 3},
		{"let a = 1; a = 5; a", 5},
		{"let a = 1; a = a + 10; a", 11},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) { wantInt(t, runVM(t, tt.in), tt.want) })
	}
}

func TestWhileLoop(t *testing.T) {
	wantInt(t, runVM(t, `
		let i = 0; let sum = 0;
		while (i < 10) { sum = sum + i; i = i + 1; }
		sum
	`), 45)
	// The loop must terminate and leave the counter visible afterwards.
	wantInt(t, runVM(t, "let i = 0; while (i < 3) { i = i + 1; } i"), 3)
}

func TestStrings(t *testing.T) {
	tests := []struct{ in, want string }{
		{`"monkey"`, "monkey"},
		{`"mon" + "key"`, "monkey"},
		{`"mon" + "key" + "banana"`, "monkeybanana"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			s, ok := runVM(t, tt.in).(*object.String)
			if !ok {
				t.Fatalf("got %T", runVM(t, tt.in))
			}
			if s.Value != tt.want {
				t.Errorf("got %q, want %q", s.Value, tt.want)
			}
		})
	}
}

func TestArraysAndHashes(t *testing.T) {
	arr, ok := runVM(t, "[1, 2 * 2, 3 + 3]").(*object.Array)
	if !ok {
		t.Fatal("not an array")
	}
	if len(arr.Elements) != 3 {
		t.Fatalf("got %d elements", len(arr.Elements))
	}
	wantInt(t, arr.Elements[1], 4)

	hash, ok := runVM(t, "{1: 2, 3: 4}").(*object.Hash)
	if !ok {
		t.Fatal("not a hash")
	}
	if len(hash.Pairs) != 2 {
		t.Fatalf("got %d pairs", len(hash.Pairs))
	}

	wantInt(t, runVM(t, "[1, 2, 3][1]"), 2)
	wantInt(t, runVM(t, "[[1, 1, 1]][0][0]"), 1)
	wantInt(t, runVM(t, "{1: 1, 2: 2}[2]"), 2)

	// Out of range and missing keys yield null rather than erroring.
	for _, in := range []string{"[1,2,3][99]", "[1,2,3][-1]", "[][0]", "{1:1}[0]", "{}[0]"} {
		if got := runVM(t, in); got != Null {
			t.Errorf("%s = %s, want null", in, got.Inspect())
		}
	}
}

func TestIndexAssignment(t *testing.T) {
	wantInt(t, runVM(t, "let a = [1,2,3]; a[1] = 42; a[1]"), 42)
	wantInt(t, runVM(t, `let h = {"a": 1}; h["b"] = 2; h["b"]`), 2)
	if err := runVMErr(t, "let a = [1]; a[9] = 1;"); err == nil {
		t.Error("out-of-range index assignment should error")
	}
}

func TestFunctionCalls(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"let f = fn() { 5 + 10 }; f();", 15},
		{"let one = fn() { 1 }; let two = fn() { 2 }; one() + two()", 3},
		{"let a = fn() { 1 }; let b = fn() { a() + 1 }; let c = fn() { b() + 1 }; c();", 3},
		{"let f = fn() { return 99; 100; }; f();", 99},
		{"let f = fn() { return 99; return 100; }; f();", 99},
		{"let identity = fn(a) { a; }; identity(4);", 4},
		{"let sum = fn(a, b) { a + b; }; sum(1, 2);", 3},
		{"let sum = fn(a, b) { let c = a + b; c; }; sum(1, 2) + sum(3, 4);", 10},
		{"let g = fn() { 10 }; let sum = fn(a,b) { let c = a+b; c + g() }; sum(1,2) + sum(3,4);", 30},
		// Arguments and locals must not collide on the stack.
		{"let f = fn(a) { let b = a * 2; let c = b * 2; c }; f(1) + f(2)", 12},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) { wantInt(t, runVM(t, tt.in), tt.want) })
	}
}

// A function with no return value yields null, not whatever happened to be
// on the stack.
func TestFunctionsWithoutReturnValue(t *testing.T) {
	for _, in := range []string{
		"let f = fn() { }; f();",
		"let f = fn() { }; let g = fn() { f(); }; g();",
	} {
		if got := runVM(t, in); got != Null {
			t.Errorf("%s = %s, want null", in, got.Inspect())
		}
	}
}

func TestFirstClassFunctions(t *testing.T) {
	wantInt(t, runVM(t, `
		let returnsOne = fn() { 1; };
		let returnsOneReturner = fn() { returnsOne; };
		returnsOneReturner()();
	`), 1)
}

func TestClosures(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"let newClosure = fn(a) { fn() { a; } }; let c = newClosure(99); c();", 99},
		{"let adder = fn(a, b) { fn(c) { a + b + c } }; adder(1, 2)(8);", 11},
		{"let adder = fn(a, b) { let c = a + b; fn(d) { c + d } }; adder(1, 2)(8);", 11},
		// Nested capture: the innermost closure must see all three levels.
		{"let a = fn(x) { fn(y) { fn(z) { x + y + z } } }; a(1)(2)(3);", 6},
		{`
			let global = 10;
			let outer = fn() {
				let a = 1; let b = 2;
				let inner = fn() { let c = 3; let d = 4; global + a + b + c + d };
				inner();
			};
			outer();
		`, 20},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) { wantInt(t, runVM(t, tt.in), tt.want) })
	}
}

// Each closure instance must capture its own binding, not share one.
func TestClosuresAreIndependent(t *testing.T) {
	wantInt(t, runVM(t, `
		let counter = fn() { let n = 0; fn() { n = n + 1; n } };
		let a = counter();
		let b = counter();
		a(); a(); a();
		b();
	`), 1)
}

// Recursion must resolve the function's own name even though the global is
// not bound until after the literal is compiled.
func TestRecursion(t *testing.T) {
	wantInt(t, runVM(t, `
		let fact = fn(n) { if (n <= 1) { 1 } else { n * fact(n - 1) } };
		fact(10);
	`), 3628800)

	wantInt(t, runVM(t, `
		let fib = fn(n) { if (n < 2) { n } else { fib(n-1) + fib(n-2) } };
		fib(20);
	`), 6765)

	// A recursive function defined inside another function.
	wantInt(t, runVM(t, `
		let wrapper = fn() {
			let countDown = fn(x) { if (x == 0) { 0 } else { countDown(x - 1) } };
			countDown(10);
		};
		wrapper();
	`), 0)
}

func TestBuiltins(t *testing.T) {
	tests := []struct {
		in   string
		want any
	}{
		{`len("")`, int64(0)},
		{`len("four")`, int64(4)},
		{`len([1, 2, 3])`, int64(3)},
		{`first([1, 2, 3])`, int64(1)},
		{`last([1, 2, 3])`, int64(3)},
		{`len(rest([1, 2, 3]))`, int64(2)},
		{`len(push([], 1))`, int64(1)},
		{`int("42")`, int64(42)},
		{`type(1)`, "INTEGER"},
		{`join(sort([3,1,2]), ",")`, "1,2,3"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := runVM(t, tt.in)
			switch want := tt.want.(type) {
			case int64:
				wantInt(t, got, want)
			case string:
				s, ok := got.(*object.String)
				if !ok {
					t.Fatalf("got %T", got)
				}
				if s.Value != want {
					t.Errorf("got %q, want %q", s.Value, want)
				}
			}
		})
	}
	// first/last on an empty array yield null.
	for _, in := range []string{"first([])", "last([])", "rest([])"} {
		if got := runVM(t, in); got != Null {
			t.Errorf("%s = %s, want null", in, got.Inspect())
		}
	}
}

func TestPutsWritesToStdout(t *testing.T) {
	var buf bytes.Buffer
	old := object.Stdout
	object.Stdout = &buf
	defer func() { object.Stdout = old }()

	runVM(t, `puts("hello", 42)`)
	if got := buf.String(); got != "hello 42\n" {
		t.Errorf("got %q", got)
	}
}

func TestRuntimeErrors(t *testing.T) {
	tests := []struct{ in, contains string }{
		{"1 + true", "unsupported types"},
		{"1 / 0", "division by zero"},
		{"1 % 0", "modulo by zero"},
		{"let f = fn(a) { a }; f();", "wrong number of arguments"},
		{"let f = fn(a) { a }; f(1, 2);", "wrong number of arguments"},
		{"let n = 1; n();", "calling non-function"},
		{`{[1]: 1}`, "unusable as hash key"},
		{"len(1)", "must be STRING"},
		{`"a" - "b"`, "unknown string operator"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			err := runVMErr(t, tt.in)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error %q does not mention %q", err, tt.contains)
			}
		})
	}
}

// Unbounded recursion must produce an error rather than crashing the host.
func TestCallDepthLimit(t *testing.T) {
	err := runVMErr(t, "let f = fn(n) { f(n + 1) }; f(0);")
	if err == nil {
		t.Fatal("expected a stack overflow error")
	}
	if !strings.Contains(err.Error(), "overflow") && !strings.Contains(err.Error(), "depth") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStepLimitStopsInfiniteLoop(t *testing.T) {
	machine := New(context.Background(), compile(t, "while (true) { }"), WithMaxSteps(10000))
	err := machine.Run()
	if err == nil || !strings.Contains(err.Error(), "step limit") {
		t.Fatalf("got %v, want a step-limit error", err)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	machine := New(ctx, compile(t, "let i = 0; while (true) { i = i + 1; }"))
	done := make(chan error, 1)
	go func() { done <- machine.Run() }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("got %v, want a cancellation error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("VM did not stop on cancellation")
	}
}

// The stack must be left clean after each statement, or a long program
// would eventually overflow it.
func TestStackDoesNotGrowAcrossStatements(t *testing.T) {
	var sb strings.Builder
	for range 3000 {
		sb.WriteString("1 + 1;\n")
	}
	machine := New(context.Background(), compile(t, sb.String()), WithMaxSteps(10_000_000))
	if err := machine.Run(); err != nil {
		t.Fatalf("stack was not kept clean: %v", err)
	}
	if machine.sp != 0 {
		t.Errorf("stack pointer = %d after program, want 0", machine.sp)
	}
}

func TestGlobalsReusedAcrossRuns(t *testing.T) {
	globals := make([]object.Object, GlobalsSize)

	st := compiler.NewSymbolTable()
	for i, b := range object.Builtins {
		st.DefineBuiltin(i, b.Name)
	}
	constants := []object.Object{}

	runLine := func(src string) object.Object {
		p := parser.New(lexer.New(src))
		program := p.ParseProgram()
		c := compiler.NewWithState(st, constants)
		if err := c.Compile(program); err != nil {
			t.Fatal(err)
		}
		constants = c.Constants()
		machine := New(context.Background(), c.Bytecode(), WithGlobals(globals))
		if err := machine.Run(); err != nil {
			t.Fatal(err)
		}
		return machine.LastPopped()
	}

	runLine("let x = 21;")
	if got := runLine("x * 2"); got.(*object.Integer).Value != 42 {
		t.Errorf("state not carried between runs: got %s", got.Inspect())
	}
}

func BenchmarkFib(b *testing.B) {
	p := parser.New(lexer.New("let fib = fn(n) { if (n < 2) { n } else { fib(n-1) + fib(n-2) } }; fib(20);"))
	program := p.ParseProgram()
	c := compiler.New()
	if err := c.Compile(program); err != nil {
		b.Fatal(err)
	}
	bc := c.Bytecode()
	b.ResetTimer()
	for b.Loop() {
		machine := New(context.Background(), bc)
		if err := machine.Run(); err != nil {
			b.Fatal(err)
		}
	}
}

// Two closures over the same variable must observe each other's writes.
// This is what capture-by-value gets wrong, and why locals are boxed.
func TestClosuresShareCapturedVariable(t *testing.T) {
	wantInt(t, runVM(t, `
		let make = fn() {
			let n = 0;
			let inc = fn() { n = n + 1; n };
			let get = fn() { n };
			inc(); inc(); inc();
			get()
		};
		make();
	`), 3)

	// The sharing must survive being returned and called later.
	wantInt(t, runVM(t, `
		let make = fn() {
			let n = 100;
			[fn() { n = n + 1; n }, fn() { n }]
		};
		let pair = make();
		let inc = pair[0];
		let get = pair[1];
		inc(); inc();
		get()
	`), 102)

	// ...but two separate invocations must not share.
	wantInt(t, runVM(t, `
		let make = fn() { let n = 0; fn() { n = n + 1; n } };
		let a = make();
		let b = make();
		a(); a(); a(); a(); a();
		b()
	`), 1)
}

// A closure assigning to a variable captured through two levels must reach
// the original, not a copy at the middle level.
func TestNestedCaptureAssignment(t *testing.T) {
	wantInt(t, runVM(t, `
		let outer = fn() {
			let n = 0;
			let mid = fn() {
				let inner = fn() { n = n + 10; n };
				inner(); inner();
			};
			mid();
			n
		};
		outer();
	`), 20)
}
