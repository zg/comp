# comp

A compiler: software that transforms code written in one language (the
source language) into another (the target language).

This one compiles a small dynamically typed language to bytecode for a stack
virtual machine, which then executes it. It includes a disassembler, so you
can see exactly what was emitted.

## Install

```sh
go install github.com/zg/comp/cmd/comp@latest
```

## Use

```sh
comp program.c        # compile and run
comp -d program.c     # disassemble
comp                  # REPL (:d toggles disassembly, :quit exits)
```

```
$ comp -d -
let add = fn(a, b) { a + b }; add(1, 2);

== main ==
0000 OpClosure 0 0
0004 OpSetGlobal 0
0007 OpGetGlobal 0
0010 OpConstant 1
0013 OpConstant 2
0016 OpCall 2
0018 OpPop

== constant 0: fn add (params 2, locals 2) ==
0000 OpGetLocal 0
0002 OpGetLocal 1
0004 OpAdd
0005 OpReturnValue
```

## Pipeline

```
source → lexer → parser → AST → compiler → bytecode → VM
```

The language is the same one `interp` implements — `let`, `fn`, closures,
`if`/`else`, `while`, arrays, hashes, strings — but executed by compiling to
instructions rather than walking the tree.

## Design notes

**Closures capture by reference, via cells.** This is the subtlest part of
the implementation, and the first version got it wrong. Capturing a
variable's *value* breaks two things: a closure cannot assign to what it
captured, and two closures over the same variable never see each other's
writes. Locals are therefore boxed in a `Cell`, and capture copies the box
pointer rather than the value. `OpGetLocalCell` and `OpGetFreeCell` exist
solely to push the box when building a closure.

A second bug followed from the first fix: a returning frame leaves its cells
in the stack array, so the *next* call at the same stack offset adopted them
and two invocations of the same function shared one variable. Frames now
start with fresh slots. Both behaviours are pinned by tests
(`TestClosuresShareCapturedVariable`, `TestClosuresAreIndependent`) — they
are easy to get wrong and silent when wrong.

**`a < b` compiles to `b > a`.** Swapping the operands at compile time means
one comparison opcode covers both directions. The test asserts on the
constant *order* to prove the swap actually happened.

**Jumps are back-patched.** A conditional's jump target is unknown until its
branches have been compiled, so a placeholder operand is emitted and
overwritten once the offset is known.

**`&&` and `||` compile to jumps**, not opcodes, because they must not
evaluate their right operand when the left already decides the result. The
tests verify this observably: `false && (x = 1)` must leave `x` at 0.

**Recursion resolves to the running closure.** A recursive call cannot
resolve to the global binding, because that binding does not exist while the
function literal is still being compiled. `OpCurrentClosure` pushes the
closure that is executing.

**Runaway programs terminate.** Integer division by zero returns an error
rather than panicking the host; unbounded recursion hits a frame limit;
`WithMaxSteps` bounds total instructions; and the VM checks its context
every 1024 instructions so a long run can be cancelled.

**The stack is left clean.** Every expression statement ends in `OpPop`. A
test runs 3,000 statements and asserts the stack pointer is back at zero —
without that discipline a long program would overflow the stack.

## Tests

```sh
go test -race ./...
go test -bench=. ./...
```

Compiler tests compare *disassembly* rather than raw bytes, so a failure
shows which instruction differs instead of which byte.
