// Command comp compiles and runs a program, disassembles it, or starts a
// REPL.
//
//	comp program.c        run
//	comp -d program.c     disassemble
//	comp                  REPL
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/zg/comp/compiler"
	"github.com/zg/comp/lexer"
	"github.com/zg/comp/object"
	"github.com/zg/comp/parser"
	"github.com/zg/comp/vm"
)

func main() {
	disasm := flag.Bool("d", false, "disassemble instead of running")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if flag.NArg() == 0 {
		if err := repl(ctx, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "comp:", err)
			os.Exit(1)
		}
		return
	}

	src, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "comp:", err)
		os.Exit(1)
	}
	if code := Run(ctx, string(src), *disasm, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

// Run compiles src and either runs or disassembles it, returning an exit
// code.
func Run(ctx context.Context, src string, disasm bool, stdout, stderr io.Writer) int {
	p := parser.New(lexer.New(src))
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		fmt.Fprintln(stderr, "parse errors:")
		for _, e := range errs {
			fmt.Fprintln(stderr, "  "+e)
		}
		return 1
	}

	c := compiler.New()
	if err := c.Compile(program); err != nil {
		fmt.Fprintln(stderr, "compile error:", err)
		return 1
	}
	bc := c.Bytecode()

	if disasm {
		fmt.Fprint(stdout, Disassemble(bc))
		return 0
	}

	object.Stdout = stdout
	machine := vm.New(ctx, bc)
	if err := machine.Run(); err != nil {
		fmt.Fprintln(stderr, "runtime error:", err)
		return 1
	}
	if result := machine.LastPopped(); result != nil && result.Type() != object.NULL {
		fmt.Fprintln(stdout, result.Inspect())
	}
	return 0
}

// Disassemble renders the bytecode, including nested functions, which is
// the main tool for understanding what the compiler produced.
func Disassemble(bc *compiler.Bytecode) string {
	var sb strings.Builder
	sb.WriteString("== main ==\n")
	sb.WriteString(bc.Instructions.String())

	for i, c := range bc.Constants {
		fn, ok := c.(*object.CompiledFunction)
		if !ok {
			continue
		}
		name := fn.Name
		if name == "" {
			name = "anonymous"
		}
		fmt.Fprintf(&sb, "\n== constant %d: fn %s (params %d, locals %d) ==\n",
			i, name, fn.NumParameters, fn.NumLocals)
		sb.WriteString(fn.Instructions.String())
	}

	sb.WriteString("\n== constants ==\n")
	for i, c := range bc.Constants {
		fmt.Fprintf(&sb, "%04d %s %s\n", i, c.Type(), c.Inspect())
	}
	return sb.String()
}

// repl compiles and runs one line at a time, carrying the symbol table,
// constant pool and globals across lines so bindings persist.
func repl(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)

	constants := []object.Object{}
	globals := make([]object.Object, vm.GlobalsSize)
	symbolTable := compiler.NewSymbolTable()
	for i, b := range object.Builtins {
		symbolTable.DefineBuiltin(i, b.Name)
	}

	object.Stdout = out
	fmt.Fprintln(out, "comp — compiles to bytecode. :quit to exit, :d to toggle disassembly")

	showDisasm := false
	for {
		fmt.Fprint(out, ">> ")
		if !scanner.Scan() {
			fmt.Fprintln(out)
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		switch line {
		case "":
			continue
		case ":quit", ":q":
			return nil
		case ":d":
			showDisasm = !showDisasm
			fmt.Fprintf(out, "disassembly %v\n", showDisasm)
			continue
		}

		p := parser.New(lexer.New(line))
		program := p.ParseProgram()
		if errs := p.Errors(); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintln(out, "  parse error:", e)
			}
			continue
		}

		c := compiler.NewWithState(symbolTable, constants)
		if err := c.Compile(program); err != nil {
			fmt.Fprintln(out, "  compile error:", err)
			continue
		}
		// Keep the grown constant pool even on a later failure, so indexes
		// already baked into earlier bytecode stay valid.
		constants = c.Constants()

		bc := c.Bytecode()
		if showDisasm {
			fmt.Fprint(out, Disassemble(bc))
		}

		machine := vm.New(ctx, bc, vm.WithGlobals(globals))
		if err := machine.Run(); err != nil {
			fmt.Fprintln(out, "  runtime error:", err)
			continue
		}
		if result := machine.LastPopped(); result != nil && result.Type() != object.NULL {
			fmt.Fprintln(out, result.Inspect())
		}
	}
}
