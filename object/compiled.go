package object

import (
	"fmt"

	"github.com/zg/comp/code"
)

const (
	COMPILED_FUNCTION Type = "COMPILED_FUNCTION"
	CLOSURE           Type = "CLOSURE"
)

// CompiledFunction is a function's bytecode plus the frame layout it needs.
type CompiledFunction struct {
	Instructions code.Instructions
	// NumLocals sizes the call frame; NumParameters is checked at call time.
	NumLocals     int
	NumParameters int
	Name          string
}

func (cf *CompiledFunction) Type() Type { return COMPILED_FUNCTION }
func (cf *CompiledFunction) Inspect() string {
	return fmt.Sprintf("compiled function %s[%p]", cf.Name, cf)
}

// Closure pairs a compiled function with the free variables it captured.
// Functions that capture nothing still become closures, so the VM has one
// calling convention rather than two.
type Closure struct {
	Fn   *CompiledFunction
	Free []Object
}

func (c *Closure) Type() Type { return CLOSURE }
func (c *Closure) Inspect() string {
	return fmt.Sprintf("closure[%p]", c)
}

const CELL Type = "CELL"

// Cell is a mutable box holding a variable that a closure captured.
//
// Capturing by value would be wrong: two closures over the same variable
// must observe each other's writes, and a closure must be able to assign to
// what it captured. Boxing the variable and sharing the box gives both.
type Cell struct{ Value Object }

func (c *Cell) Type() Type { return CELL }
func (c *Cell) Inspect() string {
	if c.Value == nil {
		return "cell(nil)"
	}
	return "cell(" + c.Value.Inspect() + ")"
}
