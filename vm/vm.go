// Package vm executes compiled bytecode on a stack machine.
package vm

import (
	"context"
	"fmt"
	"math"

	"github.com/zg/comp/code"
	"github.com/zg/comp/compiler"
	"github.com/zg/comp/object"
)

const (
	StackSize   = 2048
	GlobalsSize = 1 << 16 // matches the two-byte OpSetGlobal operand
	MaxFrames   = 1024
)

// Singletons, so comparisons can be by pointer and booleans do not allocate.
var (
	True  = &object.Boolean{Value: true}
	False = &object.Boolean{Value: false}
	Null  = &object.Null{}
)

// VM executes bytecode.
type VM struct {
	constants []object.Object

	stack []object.Object
	sp    int // always points to the next free slot; stack[sp-1] is the top

	globals []object.Object

	frames      []*Frame
	framesIndex int

	ctx context.Context
	// steps bounds total work so a runaway program terminates.
	steps    int64
	maxSteps int64
}

// Option configures a VM.
type Option func(*VM)

// WithMaxSteps bounds the number of instructions executed.
func WithMaxSteps(n int64) Option { return func(v *VM) { v.maxSteps = n } }

// WithGlobals reuses a globals slice, which is how the REPL keeps state.
func WithGlobals(g []object.Object) Option { return func(v *VM) { v.globals = g } }

func New(ctx context.Context, bytecode *compiler.Bytecode, opts ...Option) *VM {
	mainFn := &object.CompiledFunction{Instructions: bytecode.Instructions}
	mainClosure := &object.Closure{Fn: mainFn}

	frames := make([]*Frame, MaxFrames)
	frames[0] = NewFrame(mainClosure, 0)

	if ctx == nil {
		ctx = context.Background()
	}
	v := &VM{
		constants:   bytecode.Constants,
		stack:       make([]object.Object, StackSize),
		sp:          0,
		globals:     make([]object.Object, GlobalsSize),
		frames:      frames,
		framesIndex: 1,
		ctx:         ctx,
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// Globals exposes the globals slice for REPL state reuse.
func (vm *VM) Globals() []object.Object { return vm.globals }

// LastPopped returns the value most recently popped. Because an expression
// statement ends with OpPop, this is how tests observe a program's result
// without the VM having to special-case the final statement.
func (vm *VM) LastPopped() object.Object { return vm.stack[vm.sp] }

// StackTop returns the current top of stack, or nil.
func (vm *VM) StackTop() object.Object {
	if vm.sp == 0 {
		return nil
	}
	return vm.stack[vm.sp-1]
}

func (vm *VM) currentFrame() *Frame { return vm.frames[vm.framesIndex-1] }

func (vm *VM) pushFrame(f *Frame) error {
	if vm.framesIndex >= MaxFrames {
		return fmt.Errorf("stack overflow: maximum call depth %d exceeded", MaxFrames)
	}
	vm.frames[vm.framesIndex] = f
	vm.framesIndex++
	return nil
}

func (vm *VM) popFrame() *Frame {
	vm.framesIndex--
	return vm.frames[vm.framesIndex]
}

// Run executes until the main frame returns.
func (vm *VM) Run() error {
	for vm.currentFrame().ip < len(vm.currentFrame().Instructions())-1 {
		vm.steps++
		if vm.maxSteps > 0 && vm.steps > vm.maxSteps {
			return fmt.Errorf("step limit exceeded (%d)", vm.maxSteps)
		}
		// Checking the context every 1024 instructions keeps cancellation
		// responsive without paying for a select on every single one.
		if vm.steps&1023 == 0 {
			select {
			case <-vm.ctx.Done():
				return fmt.Errorf("execution cancelled: %w", vm.ctx.Err())
			default:
			}
		}

		frame := vm.currentFrame()
		frame.ip++
		ip := frame.ip
		ins := frame.Instructions()
		op := code.Opcode(ins[ip])

		if err := vm.execute(op, ins, ip); err != nil {
			return err
		}
	}
	return nil
}

func (vm *VM) execute(op code.Opcode, ins code.Instructions, ip int) error {
	frame := vm.currentFrame()

	switch op {
	case code.OpConstant:
		constIndex := code.ReadUint16(ins[ip+1:])
		frame.ip += 2
		if int(constIndex) >= len(vm.constants) {
			return fmt.Errorf("constant index %d out of range", constIndex)
		}
		return vm.push(vm.constants[constIndex])

	case code.OpPop:
		vm.pop()
		return nil

	case code.OpAdd, code.OpSub, code.OpMul, code.OpDiv, code.OpMod:
		return vm.binaryOp(op)

	case code.OpTrue:
		return vm.push(True)
	case code.OpFalse:
		return vm.push(False)
	case code.OpNull:
		return vm.push(Null)

	case code.OpEqual, code.OpNotEqual, code.OpGreaterThan, code.OpGreaterEqual:
		return vm.comparison(op)

	case code.OpBang:
		return vm.push(nativeBool(!isTruthy(vm.pop())))

	case code.OpMinus:
		operand := vm.pop()
		switch o := operand.(type) {
		case *object.Integer:
			return vm.push(&object.Integer{Value: -o.Value})
		case *object.Float:
			return vm.push(&object.Float{Value: -o.Value})
		default:
			return fmt.Errorf("unsupported type for negation: %s", operand.Type())
		}

	case code.OpJump:
		pos := int(code.ReadUint16(ins[ip+1:]))
		frame.ip = pos - 1
		return nil

	case code.OpJumpNotTruthy:
		pos := int(code.ReadUint16(ins[ip+1:]))
		frame.ip += 2
		if !isTruthy(vm.pop()) {
			frame.ip = pos - 1
		}
		return nil

	case code.OpSetGlobal:
		globalIndex := code.ReadUint16(ins[ip+1:])
		frame.ip += 2
		vm.globals[globalIndex] = vm.pop()
		return nil

	case code.OpGetGlobal:
		globalIndex := code.ReadUint16(ins[ip+1:])
		frame.ip += 2
		val := vm.globals[globalIndex]
		if val == nil {
			return fmt.Errorf("global %d read before it was set", globalIndex)
		}
		return vm.push(val)

	case code.OpSetLocal:
		localIndex := code.ReadUint8(ins[ip+1:])
		frame.ip++
		vm.localCell(frame, int(localIndex)).Value = vm.pop()
		return nil

	case code.OpGetLocal:
		localIndex := code.ReadUint8(ins[ip+1:])
		frame.ip++
		val := vm.localCell(frame, int(localIndex)).Value
		if val == nil {
			val = Null
		}
		return vm.push(val)

	case code.OpGetLocalCell:
		localIndex := code.ReadUint8(ins[ip+1:])
		frame.ip++
		return vm.push(vm.localCell(frame, int(localIndex)))

	case code.OpGetBuiltin:
		builtinIndex := code.ReadUint8(ins[ip+1:])
		frame.ip++
		if int(builtinIndex) >= len(object.Builtins) {
			return fmt.Errorf("builtin index %d out of range", builtinIndex)
		}
		return vm.push(object.Builtins[builtinIndex].Builtin)

	case code.OpGetFree:
		freeIndex := code.ReadUint8(ins[ip+1:])
		frame.ip++
		cell, err := vm.freeCell(frame, int(freeIndex))
		if err != nil {
			return err
		}
		val := cell.Value
		if val == nil {
			val = Null
		}
		return vm.push(val)

	case code.OpSetFree:
		freeIndex := code.ReadUint8(ins[ip+1:])
		frame.ip++
		cell, err := vm.freeCell(frame, int(freeIndex))
		if err != nil {
			return err
		}
		cell.Value = vm.pop()
		return nil

	case code.OpGetFreeCell:
		freeIndex := code.ReadUint8(ins[ip+1:])
		frame.ip++
		cell, err := vm.freeCell(frame, int(freeIndex))
		if err != nil {
			return err
		}
		return vm.push(cell)

	case code.OpCurrentClosure:
		return vm.push(frame.cl)

	case code.OpArray:
		numElements := int(code.ReadUint16(ins[ip+1:]))
		frame.ip += 2
		elements := make([]object.Object, numElements)
		copy(elements, vm.stack[vm.sp-numElements:vm.sp])
		vm.sp -= numElements
		return vm.push(&object.Array{Elements: elements})

	case code.OpHash:
		numElements := int(code.ReadUint16(ins[ip+1:]))
		frame.ip += 2
		hash, err := vm.buildHash(vm.sp-numElements, vm.sp)
		if err != nil {
			return err
		}
		vm.sp -= numElements
		return vm.push(hash)

	case code.OpIndex:
		index := vm.pop()
		left := vm.pop()
		return vm.executeIndex(left, index)

	case code.OpSetIndex:
		value := vm.pop()
		index := vm.pop()
		left := vm.pop()
		if err := vm.executeSetIndex(left, index, value); err != nil {
			return err
		}
		return vm.push(value)

	case code.OpCall:
		numArgs := int(code.ReadUint8(ins[ip+1:]))
		frame.ip++
		return vm.callFunction(numArgs)

	case code.OpReturnValue:
		returnValue := vm.pop()
		f := vm.popFrame()
		// Discarding down to basePointer-1 drops the locals *and* the
		// callee itself in one step.
		vm.sp = f.basePointer - 1
		return vm.push(returnValue)

	case code.OpReturn:
		f := vm.popFrame()
		vm.sp = f.basePointer - 1
		return vm.push(Null)

	case code.OpClosure:
		constIndex := code.ReadUint16(ins[ip+1:])
		numFree := int(code.ReadUint8(ins[ip+3:]))
		frame.ip += 3
		return vm.pushClosure(int(constIndex), numFree)
	}

	return fmt.Errorf("unknown opcode %d", op)
}

// localCell returns the box for a local slot, creating it on first use.
// Boxing every local costs an allocation per local per call, but means a
// captured variable is shared rather than copied — which is what makes
// closures behave correctly.
func (vm *VM) localCell(f *Frame, index int) *object.Cell {
	slot := f.basePointer + index
	if cell, ok := vm.stack[slot].(*object.Cell); ok {
		return cell
	}
	// An argument arrives as a bare value on the stack; box it in place.
	cell := &object.Cell{Value: vm.stack[slot]}
	vm.stack[slot] = cell
	return cell
}

func (vm *VM) freeCell(f *Frame, index int) (*object.Cell, error) {
	if index >= len(f.cl.Free) {
		return nil, fmt.Errorf("free variable index %d out of range", index)
	}
	cell, ok := f.cl.Free[index].(*object.Cell)
	if !ok {
		return nil, fmt.Errorf("free variable %d is not a cell", index)
	}
	return cell, nil
}

func (vm *VM) pushClosure(constIndex, numFree int) error {
	if constIndex >= len(vm.constants) {
		return fmt.Errorf("constant index %d out of range", constIndex)
	}
	fn, ok := vm.constants[constIndex].(*object.CompiledFunction)
	if !ok {
		return fmt.Errorf("not a function: %s", vm.constants[constIndex].Type())
	}
	free := make([]object.Object, numFree)
	copy(free, vm.stack[vm.sp-numFree:vm.sp])
	vm.sp -= numFree
	return vm.push(&object.Closure{Fn: fn, Free: free})
}

func (vm *VM) callFunction(numArgs int) error {
	callee := vm.stack[vm.sp-1-numArgs]
	switch callee := callee.(type) {
	case *object.Closure:
		if numArgs != callee.Fn.NumParameters {
			name := callee.Fn.Name
			if name == "" {
				name = "anonymous function"
			}
			return fmt.Errorf("wrong number of arguments to %s: want %d, got %d",
				name, callee.Fn.NumParameters, numArgs)
		}
		frame := NewFrame(callee, vm.sp-numArgs)
		if err := vm.pushFrame(frame); err != nil {
			return err
		}
		// Reserve stack space for locals beyond the arguments already
		// sitting there.
		vm.sp = frame.basePointer + callee.Fn.NumLocals
		if vm.sp >= StackSize {
			return fmt.Errorf("stack overflow")
		}

		// Give the frame fresh local slots. A returning frame leaves its
		// cells behind in the stack array, and the next call at the same
		// offset would otherwise adopt them — making two invocations share
		// one variable. Arguments are boxed in place; the rest start empty.
		for i := range callee.Fn.NumLocals {
			slot := frame.basePointer + i
			if i < numArgs {
				vm.stack[slot] = &object.Cell{Value: vm.stack[slot]}
			} else {
				vm.stack[slot] = nil
			}
		}
		return nil

	case *object.Builtin:
		args := vm.stack[vm.sp-numArgs : vm.sp]
		result := callee.Fn(args...)
		vm.sp = vm.sp - numArgs - 1
		if result == nil {
			return vm.push(Null)
		}
		if err, ok := result.(*object.Error); ok {
			return fmt.Errorf("%s", err.Message)
		}
		return vm.push(result)

	default:
		return fmt.Errorf("calling non-function: %s", callee.Type())
	}
}

func (vm *VM) binaryOp(op code.Opcode) error {
	right := vm.pop()
	left := vm.pop()

	switch {
	case left.Type() == object.INTEGER && right.Type() == object.INTEGER:
		return vm.integerOp(op, left.(*object.Integer).Value, right.(*object.Integer).Value)
	case isNumeric(left) && isNumeric(right):
		return vm.floatOp(op, toFloat(left), toFloat(right))
	case left.Type() == object.STRING && right.Type() == object.STRING:
		if op != code.OpAdd {
			return fmt.Errorf("unknown string operator: %d", op)
		}
		return vm.push(&object.String{Value: left.(*object.String).Value + right.(*object.String).Value})
	default:
		return fmt.Errorf("unsupported types for binary operation: %s and %s", left.Type(), right.Type())
	}
}

func (vm *VM) integerOp(op code.Opcode, l, r int64) error {
	var result int64
	switch op {
	case code.OpAdd:
		result = l + r
	case code.OpSub:
		result = l - r
	case code.OpMul:
		result = l * r
	case code.OpDiv:
		// Go panics on integer division by zero; return an error so the
		// VM reports it rather than crashing the host.
		if r == 0 {
			return fmt.Errorf("division by zero")
		}
		result = l / r
	case code.OpMod:
		if r == 0 {
			return fmt.Errorf("modulo by zero")
		}
		result = l % r
	default:
		return fmt.Errorf("unknown integer operator: %d", op)
	}
	return vm.push(&object.Integer{Value: result})
}

func (vm *VM) floatOp(op code.Opcode, l, r float64) error {
	var result float64
	switch op {
	case code.OpAdd:
		result = l + r
	case code.OpSub:
		result = l - r
	case code.OpMul:
		result = l * r
	case code.OpDiv:
		// Float division by zero is defined (±Inf), unlike the integer case.
		result = l / r
	case code.OpMod:
		result = math.Mod(l, r)
	default:
		return fmt.Errorf("unknown float operator: %d", op)
	}
	return vm.push(&object.Float{Value: result})
}

func (vm *VM) comparison(op code.Opcode) error {
	right := vm.pop()
	left := vm.pop()

	switch {
	case left.Type() == object.INTEGER && right.Type() == object.INTEGER:
		l, r := left.(*object.Integer).Value, right.(*object.Integer).Value
		switch op {
		case code.OpEqual:
			return vm.push(nativeBool(l == r))
		case code.OpNotEqual:
			return vm.push(nativeBool(l != r))
		case code.OpGreaterThan:
			return vm.push(nativeBool(l > r))
		case code.OpGreaterEqual:
			return vm.push(nativeBool(l >= r))
		}

	case isNumeric(left) && isNumeric(right):
		l, r := toFloat(left), toFloat(right)
		switch op {
		case code.OpEqual:
			return vm.push(nativeBool(l == r))
		case code.OpNotEqual:
			return vm.push(nativeBool(l != r))
		case code.OpGreaterThan:
			return vm.push(nativeBool(l > r))
		case code.OpGreaterEqual:
			return vm.push(nativeBool(l >= r))
		}

	case left.Type() == object.STRING && right.Type() == object.STRING:
		l, r := left.(*object.String).Value, right.(*object.String).Value
		switch op {
		case code.OpEqual:
			return vm.push(nativeBool(l == r))
		case code.OpNotEqual:
			return vm.push(nativeBool(l != r))
		case code.OpGreaterThan:
			return vm.push(nativeBool(l > r))
		case code.OpGreaterEqual:
			return vm.push(nativeBool(l >= r))
		}

	default:
		// Booleans and null compare by identity thanks to the singletons.
		switch op {
		case code.OpEqual:
			return vm.push(nativeBool(left == right))
		case code.OpNotEqual:
			return vm.push(nativeBool(left != right))
		}
		return fmt.Errorf("unsupported comparison: %s and %s", left.Type(), right.Type())
	}
	return fmt.Errorf("unknown comparison operator: %d", op)
}

func (vm *VM) buildHash(startIndex, endIndex int) (object.Object, error) {
	hash := &object.Hash{Pairs: map[object.HashKey]object.HashPair{}}
	for i := startIndex; i < endIndex; i += 2 {
		key, value := vm.stack[i], vm.stack[i+1]
		hashKey, ok := key.(object.Hashable)
		if !ok {
			return nil, fmt.Errorf("unusable as hash key: %s", key.Type())
		}
		hash.Set(hashKey.HashKey(), object.HashPair{Key: key, Value: value})
	}
	return hash, nil
}

func (vm *VM) executeIndex(left, index object.Object) error {
	switch {
	case left.Type() == object.ARRAY && index.Type() == object.INTEGER:
		arr := left.(*object.Array)
		i := index.(*object.Integer).Value
		// Out of range yields null so "walk until null" loops work.
		if i < 0 || i >= int64(len(arr.Elements)) {
			return vm.push(Null)
		}
		return vm.push(arr.Elements[i])

	case left.Type() == object.STRING && index.Type() == object.INTEGER:
		runes := []rune(left.(*object.String).Value)
		i := index.(*object.Integer).Value
		if i < 0 || i >= int64(len(runes)) {
			return vm.push(Null)
		}
		return vm.push(&object.String{Value: string(runes[i])})

	case left.Type() == object.HASH:
		hash := left.(*object.Hash)
		key, ok := index.(object.Hashable)
		if !ok {
			return fmt.Errorf("unusable as hash key: %s", index.Type())
		}
		pair, ok := hash.Pairs[key.HashKey()]
		if !ok {
			return vm.push(Null)
		}
		return vm.push(pair.Value)

	default:
		return fmt.Errorf("index operator not supported: %s", left.Type())
	}
}

func (vm *VM) executeSetIndex(left, index, value object.Object) error {
	switch container := left.(type) {
	case *object.Array:
		i, ok := index.(*object.Integer)
		if !ok {
			return fmt.Errorf("array index must be INTEGER, got %s", index.Type())
		}
		if i.Value < 0 || i.Value >= int64(len(container.Elements)) {
			return fmt.Errorf("index out of range: %d (length %d)", i.Value, len(container.Elements))
		}
		container.Elements[i.Value] = value
		return nil
	case *object.Hash:
		key, ok := index.(object.Hashable)
		if !ok {
			return fmt.Errorf("unusable as hash key: %s", index.Type())
		}
		container.Set(key.HashKey(), object.HashPair{Key: index, Value: value})
		return nil
	default:
		return fmt.Errorf("cannot index-assign into %s", left.Type())
	}
}

func (vm *VM) push(o object.Object) error {
	if vm.sp >= StackSize {
		return fmt.Errorf("stack overflow")
	}
	vm.stack[vm.sp] = o
	vm.sp++
	return nil
}

func (vm *VM) pop() object.Object {
	if vm.sp == 0 {
		return Null
	}
	o := vm.stack[vm.sp-1]
	vm.sp--
	return o
}

func nativeBool(b bool) *object.Boolean {
	if b {
		return True
	}
	return False
}

// isTruthy: only null and false are falsey; 0 and "" are truthy.
func isTruthy(obj object.Object) bool {
	switch obj := obj.(type) {
	case *object.Boolean:
		return obj.Value
	case *object.Null:
		return false
	default:
		return true
	}
}

func isNumeric(o object.Object) bool {
	return o.Type() == object.INTEGER || o.Type() == object.FLOAT
}

func toFloat(o object.Object) float64 {
	switch o := o.(type) {
	case *object.Integer:
		return float64(o.Value)
	case *object.Float:
		return o.Value
	}
	return 0
}
