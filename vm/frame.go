package vm

import (
	"github.com/zg/comp/code"
	"github.com/zg/comp/object"
)

// Frame is one function activation: the closure being run, its instruction
// pointer, and where its locals start on the stack.
type Frame struct {
	cl *object.Closure
	ip int
	// basePointer is the stack slot where this frame's locals begin.
	// Locals live on the stack rather than in a separate structure, so a
	// return can discard everything above it in one assignment.
	basePointer int
}

func NewFrame(cl *object.Closure, basePointer int) *Frame {
	return &Frame{cl: cl, ip: -1, basePointer: basePointer}
}

func (f *Frame) Instructions() code.Instructions { return f.cl.Fn.Instructions }
