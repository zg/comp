package compiler

import (
	"strings"
	"testing"

	"github.com/zg/comp/code"
	"github.com/zg/comp/lexer"
	"github.com/zg/comp/object"
	"github.com/zg/comp/parser"
)

func compile(t *testing.T, input string) *Bytecode {
	t.Helper()
	p := parser.New(lexer.New(input))
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors: %s", strings.Join(errs, "\n"))
	}
	c := New()
	if err := c.Compile(program); err != nil {
		t.Fatalf("compile error: %v", err)
	}
	return c.Bytecode()
}

func concat(chunks ...code.Instructions) code.Instructions {
	var out code.Instructions
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

// Comparing disassembly rather than raw bytes makes a failure readable.
func wantInstructions(t *testing.T, got, want code.Instructions) {
	t.Helper()
	if got.String() != want.String() {
		t.Errorf("wrong instructions\ngot:\n%swant:\n%s", got.String(), want.String())
	}
}

func TestIntegerArithmetic(t *testing.T) {
	bc := compile(t, "1 + 2")
	wantInstructions(t, bc.Instructions, concat(
		code.Make(code.OpConstant, 0),
		code.Make(code.OpConstant, 1),
		code.Make(code.OpAdd),
		code.Make(code.OpPop),
	))
	if len(bc.Constants) != 2 {
		t.Fatalf("got %d constants, want 2", len(bc.Constants))
	}
}

// `a < b` is compiled as `b > a`, so one opcode covers both directions.
func TestLessThanIsCompiledAsGreaterThanWithSwappedOperands(t *testing.T) {
	bc := compile(t, "1 < 2")
	wantInstructions(t, bc.Instructions, concat(
		code.Make(code.OpConstant, 0), // 2 is pushed first
		code.Make(code.OpConstant, 1), // then 1
		code.Make(code.OpGreaterThan),
		code.Make(code.OpPop),
	))
	// The constant order proves the swap happened.
	if bc.Constants[0].(*object.Integer).Value != 2 {
		t.Errorf("operands were not swapped: first constant is %s", bc.Constants[0].Inspect())
	}
}

// An if with no else must still produce a value on the stack.
func TestConditionalWithoutAlternativeEmitsNull(t *testing.T) {
	bc := compile(t, "if (true) { 10 }; 3333;")
	if !strings.Contains(bc.Instructions.String(), "OpNull") {
		t.Errorf("missing OpNull for the absent else branch:\n%s", bc.Instructions)
	}
}

func TestGlobalLetStatements(t *testing.T) {
	bc := compile(t, "let one = 1; let two = 2;")
	wantInstructions(t, bc.Instructions, concat(
		code.Make(code.OpConstant, 0),
		code.Make(code.OpSetGlobal, 0),
		code.Make(code.OpConstant, 1),
		code.Make(code.OpSetGlobal, 1),
	))
}

func TestFunctionCompilesToConstant(t *testing.T) {
	bc := compile(t, "fn() { return 5 + 10 }")
	fn, ok := bc.Constants[2].(*object.CompiledFunction)
	if !ok {
		t.Fatalf("constant 2 is %T, want CompiledFunction", bc.Constants[2])
	}
	wantInstructions(t, fn.Instructions, concat(
		code.Make(code.OpConstant, 0),
		code.Make(code.OpConstant, 1),
		code.Make(code.OpAdd),
		code.Make(code.OpReturnValue),
	))
}

// A body ending in an expression returns it, rather than popping it.
func TestImplicitReturn(t *testing.T) {
	bc := compile(t, "fn() { 1 }")
	fn := bc.Constants[1].(*object.CompiledFunction)
	if !strings.Contains(fn.Instructions.String(), "OpReturnValue") {
		t.Errorf("trailing expression was not turned into a return:\n%s", fn.Instructions)
	}
	if strings.Contains(fn.Instructions.String(), "OpPop") {
		t.Errorf("trailing pop was not removed:\n%s", fn.Instructions)
	}
}

// An empty body returns null rather than leaving the stack untouched.
func TestEmptyFunctionReturnsNull(t *testing.T) {
	bc := compile(t, "fn() { }")
	fn := bc.Constants[0].(*object.CompiledFunction)
	wantInstructions(t, fn.Instructions, code.Make(code.OpReturn))
}

func TestFunctionLocalsCounted(t *testing.T) {
	bc := compile(t, "fn() { let a = 1; let b = 2; a + b }")
	var fn *object.CompiledFunction
	for _, c := range bc.Constants {
		if f, ok := c.(*object.CompiledFunction); ok {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("no compiled function found")
	}
	if fn.NumLocals != 2 {
		t.Errorf("NumLocals = %d, want 2", fn.NumLocals)
	}
}

func TestBuiltinsCompileToIndex(t *testing.T) {
	bc := compile(t, "len([]);")
	if !strings.Contains(bc.Instructions.String(), "OpGetBuiltin") {
		t.Errorf("builtin was not resolved to an index:\n%s", bc.Instructions)
	}
}

// Capture must load the variable's cell, not its value.
func TestClosureCapturesCells(t *testing.T) {
	bc := compile(t, "fn(a) { fn(b) { a + b } }")
	var outer *object.CompiledFunction
	for _, c := range bc.Constants {
		if f, ok := c.(*object.CompiledFunction); ok && strings.Contains(f.Instructions.String(), "OpClosure") {
			outer = f
		}
	}
	if outer == nil {
		t.Fatal("no enclosing function found")
	}
	if !strings.Contains(outer.Instructions.String(), "OpGetLocalCell") {
		t.Errorf("capture did not load a cell:\n%s", outer.Instructions)
	}
}

func TestRecursiveFunctionUsesCurrentClosure(t *testing.T) {
	bc := compile(t, "let countDown = fn(x) { countDown(x - 1) }; countDown(1);")
	var fn *object.CompiledFunction
	for _, c := range bc.Constants {
		if f, ok := c.(*object.CompiledFunction); ok {
			fn = f
		}
	}
	if !strings.Contains(fn.Instructions.String(), "OpCurrentClosure") {
		t.Errorf("recursion did not resolve to the current closure:\n%s", fn.Instructions)
	}
}

func TestCompileErrors(t *testing.T) {
	tests := []string{
		"undefinedVariable",
		"x = 5;", // assignment to an undeclared name
	}
	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			p := parser.New(lexer.New(in))
			program := p.ParseProgram()
			c := New()
			if err := c.Compile(program); err == nil {
				t.Error("expected a compile error")
			}
		})
	}
}

func TestSymbolTableDefineAndResolve(t *testing.T) {
	global := NewSymbolTable()
	a := global.Define("a")
	if a.Scope != GlobalScope || a.Index != 0 {
		t.Errorf("a = %+v", a)
	}

	local := NewEnclosedSymbolTable(global)
	b := local.Define("b")
	if b.Scope != LocalScope || b.Index != 0 {
		t.Errorf("b = %+v", b)
	}

	// A global stays global when resolved from an inner scope; it is not
	// captured, because it is already reachable everywhere.
	got, ok := local.Resolve("a")
	if !ok || got.Scope != GlobalScope {
		t.Errorf("resolving a from inner scope gave %+v", got)
	}
}

// An enclosing function's local becomes a free variable of the inner one.
func TestSymbolTableFreeVariables(t *testing.T) {
	global := NewSymbolTable()
	global.Define("g")

	first := NewEnclosedSymbolTable(global)
	first.Define("a")

	second := NewEnclosedSymbolTable(first)
	sym, ok := second.Resolve("a")
	if !ok {
		t.Fatal("a should resolve")
	}
	if sym.Scope != FreeScope {
		t.Errorf("scope = %s, want FREE", sym.Scope)
	}
	if len(second.FreeSymbols) != 1 {
		t.Fatalf("got %d free symbols, want 1", len(second.FreeSymbols))
	}
	if second.FreeSymbols[0].Scope != LocalScope {
		t.Errorf("captured symbol should have been a local of the enclosing scope")
	}
}

// Redefining a name in the same scope must reuse its slot rather than
// leaking a new one on every redefinition.
func TestSymbolTableRedefineReusesSlot(t *testing.T) {
	st := NewSymbolTable()
	first := st.Define("x")
	second := st.Define("x")
	if first.Index != second.Index {
		t.Errorf("redefinition allocated a new slot: %d then %d", first.Index, second.Index)
	}
	if st.NumDefinitions() != 1 {
		t.Errorf("NumDefinitions = %d, want 1", st.NumDefinitions())
	}
}

func TestSymbolTableUnresolved(t *testing.T) {
	if _, ok := NewSymbolTable().Resolve("nope"); ok {
		t.Error("unbound name should not resolve")
	}
}

func TestBuiltinNamesSorted(t *testing.T) {
	names := sortedBuiltinNames()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("not sorted: %v", names)
		}
	}
}
