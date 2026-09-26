// Package code defines the bytecode instruction set.
//
// The VM is a stack machine: operands are pushed, an opcode consumes them
// and pushes its result. That keeps instruction encoding simple at the cost
// of more instructions than a register machine would need.
package code

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Instructions is a flat byte slice holding encoded instructions.
type Instructions []byte

// Opcode identifies an operation.
type Opcode byte

const (
	OpConstant Opcode = iota // push constant[operand]
	OpPop                    // discard the top of stack

	OpAdd
	OpSub
	OpMul
	OpDiv
	OpMod

	OpTrue
	OpFalse
	OpNull

	OpEqual
	OpNotEqual
	OpGreaterThan // `<` is compiled by swapping operands, so no OpLessThan
	OpGreaterEqual

	OpMinus // unary -
	OpBang  // unary !

	OpJump          // unconditional jump to operand
	OpJumpNotTruthy // jump if the popped value is falsey

	OpGetGlobal
	OpSetGlobal
	OpGetLocal
	OpSetLocal
	OpGetBuiltin
	OpGetFree // read a free (captured) variable of the current closure
	OpSetFree // assign to a free variable

	// The *Cell variants push the box itself rather than its contents.
	// They are emitted only when building a closure, to hand the callee a
	// shared reference to the variable.
	OpGetLocalCell
	OpGetFreeCell

	OpArray
	OpHash
	OpIndex
	OpSetIndex

	OpCall
	OpReturnValue // return the value on top of the stack
	OpReturn      // return null

	OpClosure        // build a closure: operands are (constIndex, freeCount)
	OpCurrentClosure // push the running closure, for recursion
)

// Definition describes an opcode's name and operand widths in bytes.
type Definition struct {
	Name          string
	OperandWidths []int
}

var definitions = map[Opcode]*Definition{
	OpConstant:       {"OpConstant", []int{2}},
	OpPop:            {"OpPop", nil},
	OpAdd:            {"OpAdd", nil},
	OpSub:            {"OpSub", nil},
	OpMul:            {"OpMul", nil},
	OpDiv:            {"OpDiv", nil},
	OpMod:            {"OpMod", nil},
	OpTrue:           {"OpTrue", nil},
	OpFalse:          {"OpFalse", nil},
	OpNull:           {"OpNull", nil},
	OpEqual:          {"OpEqual", nil},
	OpNotEqual:       {"OpNotEqual", nil},
	OpGreaterThan:    {"OpGreaterThan", nil},
	OpGreaterEqual:   {"OpGreaterEqual", nil},
	OpMinus:          {"OpMinus", nil},
	OpBang:           {"OpBang", nil},
	OpJump:           {"OpJump", []int{2}},
	OpJumpNotTruthy:  {"OpJumpNotTruthy", []int{2}},
	OpGetGlobal:      {"OpGetGlobal", []int{2}},
	OpSetGlobal:      {"OpSetGlobal", []int{2}},
	OpGetLocal:       {"OpGetLocal", []int{1}},
	OpSetLocal:       {"OpSetLocal", []int{1}},
	OpGetBuiltin:     {"OpGetBuiltin", []int{1}},
	OpGetFree:        {"OpGetFree", []int{1}},
	OpSetFree:        {"OpSetFree", []int{1}},
	OpGetLocalCell:   {"OpGetLocalCell", []int{1}},
	OpGetFreeCell:    {"OpGetFreeCell", []int{1}},
	OpArray:          {"OpArray", []int{2}},
	OpHash:           {"OpHash", []int{2}},
	OpIndex:          {"OpIndex", nil},
	OpSetIndex:       {"OpSetIndex", nil},
	OpCall:           {"OpCall", []int{1}},
	OpReturnValue:    {"OpReturnValue", nil},
	OpReturn:         {"OpReturn", nil},
	OpClosure:        {"OpClosure", []int{2, 1}},
	OpCurrentClosure: {"OpCurrentClosure", nil},
}

// Lookup returns the definition of op.
func Lookup(op byte) (*Definition, error) {
	def, ok := definitions[Opcode(op)]
	if !ok {
		return nil, fmt.Errorf("opcode %d undefined", op)
	}
	return def, nil
}

// Make encodes an instruction. Operands wider than their declared width are
// truncated, so the compiler must not emit one — the constant pool and
// jump offsets are bounds-checked by the compiler instead.
func Make(op Opcode, operands ...int) Instructions {
	def, ok := definitions[op]
	if !ok {
		return Instructions{}
	}

	length := 1
	for _, w := range def.OperandWidths {
		length += w
	}

	ins := make(Instructions, length)
	ins[0] = byte(op)
	offset := 1
	for i, o := range operands {
		if i >= len(def.OperandWidths) {
			break
		}
		switch def.OperandWidths[i] {
		case 2:
			binary.BigEndian.PutUint16(ins[offset:], uint16(o))
		case 1:
			ins[offset] = byte(o)
		}
		offset += def.OperandWidths[i]
	}
	return ins
}

// ReadOperands decodes the operands following an opcode, returning them and
// the number of bytes read.
func ReadOperands(def *Definition, ins Instructions) ([]int, int) {
	operands := make([]int, len(def.OperandWidths))
	offset := 0
	for i, width := range def.OperandWidths {
		switch width {
		case 2:
			operands[i] = int(ReadUint16(ins[offset:]))
		case 1:
			operands[i] = int(ReadUint8(ins[offset:]))
		}
		offset += width
	}
	return operands, offset
}

func ReadUint16(ins Instructions) uint16 { return binary.BigEndian.Uint16(ins) }
func ReadUint8(ins Instructions) uint8   { return ins[0] }

// String disassembles the instructions, which is what makes compiler tests
// readable and is invaluable when debugging codegen.
func (ins Instructions) String() string {
	var out strings.Builder
	i := 0
	for i < len(ins) {
		def, err := Lookup(ins[i])
		if err != nil {
			fmt.Fprintf(&out, "ERROR: %s\n", err)
			i++
			continue
		}
		operands, read := ReadOperands(def, ins[i+1:])
		fmt.Fprintf(&out, "%04d %s\n", i, fmtInstruction(def, operands))
		i += 1 + read
	}
	return out.String()
}

func fmtInstruction(def *Definition, operands []int) string {
	if len(operands) != len(def.OperandWidths) {
		return fmt.Sprintf("ERROR: operand count %d does not match defined %d",
			len(operands), len(def.OperandWidths))
	}
	switch len(operands) {
	case 0:
		return def.Name
	case 1:
		return fmt.Sprintf("%s %d", def.Name, operands[0])
	case 2:
		return fmt.Sprintf("%s %d %d", def.Name, operands[0], operands[1])
	}
	return fmt.Sprintf("ERROR: unhandled operand count for %s", def.Name)
}
