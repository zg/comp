// Package compiler turns an AST into bytecode.
package compiler

import (
	"fmt"
	"sort"

	"github.com/zg/comp/ast"
	"github.com/zg/comp/code"
	"github.com/zg/comp/object"
)

// EmittedInstruction remembers what was last emitted, so the compiler can
// patch or remove it (for example dropping a trailing OpPop in a function
// body so the value becomes the return value).
type EmittedInstruction struct {
	Opcode   code.Opcode
	Position int
}

// scope is one function's worth of instructions being compiled.
type scope struct {
	instructions        code.Instructions
	lastInstruction     EmittedInstruction
	previousInstruction EmittedInstruction
}

// Compiler compiles an AST to bytecode.
type Compiler struct {
	constants []object.Object

	symbolTable *SymbolTable
	scopes      []scope
	scopeIndex  int
}

// Bytecode is the compiler's output.
type Bytecode struct {
	Instructions code.Instructions
	Constants    []object.Object
}

func New() *Compiler {
	st := NewSymbolTable()
	for i, b := range object.Builtins {
		st.DefineBuiltin(i, b.Name)
	}
	return &Compiler{
		constants:   []object.Object{},
		symbolTable: st,
		scopes:      []scope{{}},
	}
}

// NewWithState reuses a symbol table and constant pool, which is what lets
// the REPL compile one line at a time without losing earlier bindings.
func NewWithState(s *SymbolTable, constants []object.Object) *Compiler {
	c := New()
	c.symbolTable = s
	c.constants = constants
	return c
}

func (c *Compiler) Bytecode() *Bytecode {
	return &Bytecode{Instructions: c.currentInstructions(), Constants: c.constants}
}

// SymbolTable exposes the table for REPL state reuse.
func (c *Compiler) SymbolTable() *SymbolTable { return c.symbolTable }

// Constants exposes the pool for REPL state reuse.
func (c *Compiler) Constants() []object.Object { return c.constants }

func (c *Compiler) Compile(node ast.Node) error {
	switch node := node.(type) {
	case *ast.Program:
		for _, s := range node.Statements {
			if err := c.Compile(s); err != nil {
				return err
			}
		}

	case *ast.ExpressionStatement:
		if err := c.Compile(node.Expression); err != nil {
			return err
		}
		// An expression statement's value is discarded; without this the
		// stack would grow without bound across a long program.
		c.emit(code.OpPop)

	case *ast.BlockStatement:
		for _, s := range node.Statements {
			if err := c.Compile(s); err != nil {
				return err
			}
		}

	case *ast.LetStatement:
		// Define before compiling the value so a recursive function can
		// refer to its own name.
		symbol := c.symbolTable.Define(node.Name.Value)
		if err := c.Compile(node.Value); err != nil {
			return err
		}
		if symbol.Scope == GlobalScope {
			c.emit(code.OpSetGlobal, symbol.Index)
		} else {
			c.emit(code.OpSetLocal, symbol.Index)
		}

	case *ast.ReturnStatement:
		if node.ReturnValue == nil {
			c.emit(code.OpReturn)
			return nil
		}
		if err := c.Compile(node.ReturnValue); err != nil {
			return err
		}
		c.emit(code.OpReturnValue)

	case *ast.WhileStatement:
		return c.compileWhile(node)

	case *ast.AssignExpression:
		return c.compileAssign(node)

	case *ast.Identifier:
		symbol, ok := c.symbolTable.Resolve(node.Value)
		if !ok {
			return fmt.Errorf("undefined variable %s", node.Value)
		}
		c.loadSymbol(symbol)

	case *ast.IntegerLiteral:
		return c.emitConstant(&object.Integer{Value: node.Value})
	case *ast.FloatLiteral:
		return c.emitConstant(&object.Float{Value: node.Value})
	case *ast.StringLiteral:
		return c.emitConstant(&object.String{Value: node.Value})

	case *ast.Boolean:
		if node.Value {
			c.emit(code.OpTrue)
		} else {
			c.emit(code.OpFalse)
		}
	case *ast.Null:
		c.emit(code.OpNull)

	case *ast.PrefixExpression:
		if err := c.Compile(node.Right); err != nil {
			return err
		}
		switch node.Operator {
		case "!":
			c.emit(code.OpBang)
		case "-":
			c.emit(code.OpMinus)
		default:
			return fmt.Errorf("unknown prefix operator %s", node.Operator)
		}

	case *ast.InfixExpression:
		return c.compileInfix(node)

	case *ast.IfExpression:
		return c.compileIf(node)

	case *ast.ArrayLiteral:
		for _, el := range node.Elements {
			if err := c.Compile(el); err != nil {
				return err
			}
		}
		c.emit(code.OpArray, len(node.Elements))

	case *ast.HashLiteral:
		// Iterate node.Order, not the map: Go randomises map iteration and
		// the emitted bytecode must be deterministic.
		for _, k := range node.Order {
			if err := c.Compile(k); err != nil {
				return err
			}
			if err := c.Compile(node.Pairs[k]); err != nil {
				return err
			}
		}
		c.emit(code.OpHash, len(node.Order)*2)

	case *ast.IndexExpression:
		if err := c.Compile(node.Left); err != nil {
			return err
		}
		if err := c.Compile(node.Index); err != nil {
			return err
		}
		c.emit(code.OpIndex)

	case *ast.FunctionLiteral:
		return c.compileFunction(node)

	case *ast.CallExpression:
		if err := c.Compile(node.Function); err != nil {
			return err
		}
		for _, a := range node.Arguments {
			if err := c.Compile(a); err != nil {
				return err
			}
		}
		c.emit(code.OpCall, len(node.Arguments))

	default:
		return fmt.Errorf("unhandled AST node %T", node)
	}
	return nil
}

func (c *Compiler) compileInfix(node *ast.InfixExpression) error {
	// `a < b` is compiled as `b > a`: one comparison opcode covers both
	// directions, halving the instruction set for no cost.
	if node.Operator == "<" || node.Operator == "<=" {
		if err := c.Compile(node.Right); err != nil {
			return err
		}
		if err := c.Compile(node.Left); err != nil {
			return err
		}
		if node.Operator == "<" {
			c.emit(code.OpGreaterThan)
		} else {
			c.emit(code.OpGreaterEqual)
		}
		return nil
	}

	// && and || must short-circuit, so they compile to jumps rather than
	// to an opcode that would need both operands evaluated.
	if node.Operator == "&&" || node.Operator == "||" {
		return c.compileLogical(node)
	}

	if err := c.Compile(node.Left); err != nil {
		return err
	}
	if err := c.Compile(node.Right); err != nil {
		return err
	}
	switch node.Operator {
	case "+":
		c.emit(code.OpAdd)
	case "-":
		c.emit(code.OpSub)
	case "*":
		c.emit(code.OpMul)
	case "/":
		c.emit(code.OpDiv)
	case "%":
		c.emit(code.OpMod)
	case ">":
		c.emit(code.OpGreaterThan)
	case ">=":
		c.emit(code.OpGreaterEqual)
	case "==":
		c.emit(code.OpEqual)
	case "!=":
		c.emit(code.OpNotEqual)
	default:
		return fmt.Errorf("unknown operator %s", node.Operator)
	}
	return nil
}

// compileLogical emits short-circuiting && and ||.
func (c *Compiler) compileLogical(node *ast.InfixExpression) error {
	if err := c.Compile(node.Left); err != nil {
		return err
	}

	if node.Operator == "&&" {
		// If the left is falsey, skip the right and yield false.
		jumpNotTruthy := c.emit(code.OpJumpNotTruthy, 9999)
		if err := c.Compile(node.Right); err != nil {
			return err
		}
		// Normalise the right operand to a boolean with !! so that
		// `1 && 2` yields true rather than 2.
		c.emit(code.OpBang)
		c.emit(code.OpBang)
		jumpEnd := c.emit(code.OpJump, 9999)
		c.changeOperand(jumpNotTruthy, len(c.currentInstructions()))
		c.emit(code.OpFalse)
		c.changeOperand(jumpEnd, len(c.currentInstructions()))
		return nil
	}

	// ||: if the left is falsey evaluate the right, otherwise yield true.
	jumpNotTruthy := c.emit(code.OpJumpNotTruthy, 9999)
	c.emit(code.OpTrue)
	jumpEnd := c.emit(code.OpJump, 9999)
	c.changeOperand(jumpNotTruthy, len(c.currentInstructions()))
	if err := c.Compile(node.Right); err != nil {
		return err
	}
	c.emit(code.OpBang)
	c.emit(code.OpBang)
	c.changeOperand(jumpEnd, len(c.currentInstructions()))
	return nil
}

func (c *Compiler) compileIf(node *ast.IfExpression) error {
	if err := c.Compile(node.Condition); err != nil {
		return err
	}
	// Operands are placeholders; the real offsets are only known once the
	// branches have been compiled, so they are back-patched below.
	jumpNotTruthy := c.emit(code.OpJumpNotTruthy, 9999)

	if err := c.Compile(node.Consequence); err != nil {
		return err
	}
	// An if is an expression, so its branches must leave exactly one value
	// on the stack. Drop the trailing pop the block statement emitted.
	if c.lastInstructionIs(code.OpPop) {
		c.removeLastPop()
	}

	jumpEnd := c.emit(code.OpJump, 9999)
	c.changeOperand(jumpNotTruthy, len(c.currentInstructions()))

	if node.Alternative == nil {
		// A missing else still has to yield a value.
		c.emit(code.OpNull)
	} else {
		if err := c.Compile(node.Alternative); err != nil {
			return err
		}
		if c.lastInstructionIs(code.OpPop) {
			c.removeLastPop()
		}
	}
	c.changeOperand(jumpEnd, len(c.currentInstructions()))
	return nil
}

func (c *Compiler) compileWhile(node *ast.WhileStatement) error {
	conditionPos := len(c.currentInstructions())
	if err := c.Compile(node.Condition); err != nil {
		return err
	}
	jumpOut := c.emit(code.OpJumpNotTruthy, 9999)

	if err := c.Compile(node.Body); err != nil {
		return err
	}
	c.emit(code.OpJump, conditionPos)
	c.changeOperand(jumpOut, len(c.currentInstructions()))
	return nil
}

func (c *Compiler) compileAssign(node *ast.AssignExpression) error {
	switch target := node.Target.(type) {
	case *ast.Identifier:
		symbol, ok := c.symbolTable.Resolve(target.Value)
		if !ok {
			return fmt.Errorf("cannot assign to undeclared variable %s", target.Value)
		}
		if err := c.Compile(node.Value); err != nil {
			return err
		}
		switch symbol.Scope {
		case GlobalScope:
			c.emit(code.OpSetGlobal, symbol.Index)
		case LocalScope:
			c.emit(code.OpSetLocal, symbol.Index)
		case FreeScope:
			c.emit(code.OpSetFree, symbol.Index)
		default:
			return fmt.Errorf("cannot assign to %s (%s scope)", target.Value, symbol.Scope)
		}
		// Assignment is an expression and must leave its value behind.
		c.loadSymbol(symbol)
		return nil

	case *ast.IndexExpression:
		if err := c.Compile(target.Left); err != nil {
			return err
		}
		if err := c.Compile(target.Index); err != nil {
			return err
		}
		if err := c.Compile(node.Value); err != nil {
			return err
		}
		c.emit(code.OpSetIndex)
		return nil
	}
	return fmt.Errorf("invalid assignment target %T", node.Target)
}

func (c *Compiler) compileFunction(node *ast.FunctionLiteral) error {
	c.enterScope()

	if node.Name != "" {
		c.symbolTable.DefineFunctionName(node.Name)
	}
	for _, p := range node.Parameters {
		c.symbolTable.Define(p.Value)
	}

	if err := c.Compile(node.Body); err != nil {
		return err
	}
	// A body ending in an expression returns it; the trailing pop becomes
	// a return.
	if c.lastInstructionIs(code.OpPop) {
		c.replaceLastPopWithReturn()
	}
	// A body with no return at all returns null.
	if !c.lastInstructionIs(code.OpReturnValue) && !c.lastInstructionIs(code.OpReturn) {
		c.emit(code.OpReturn)
	}

	freeSymbols := c.symbolTable.FreeSymbols
	numLocals := c.symbolTable.NumDefinitions()
	instructions := c.leaveScope()

	// Load each captured symbol as its *cell*, so the closure shares the
	// variable with the enclosing scope rather than copying its value.
	for _, s := range freeSymbols {
		c.loadSymbolCell(s)
	}

	fn := &object.CompiledFunction{
		Instructions:  instructions,
		NumLocals:     numLocals,
		NumParameters: len(node.Parameters),
		Name:          node.Name,
	}
	idx, err := c.addConstant(fn)
	if err != nil {
		return err
	}
	c.emit(code.OpClosure, idx, len(freeSymbols))
	return nil
}

// loadSymbolCell pushes the box holding s, for handing to OpClosure.
func (c *Compiler) loadSymbolCell(s Symbol) {
	switch s.Scope {
	case LocalScope:
		c.emit(code.OpGetLocalCell, s.Index)
	case FreeScope:
		// Capturing something this scope itself captured: pass along the
		// same box so all levels share one variable.
		c.emit(code.OpGetFreeCell, s.Index)
	default:
		// Globals and builtins are already shared; no box is needed.
		c.loadSymbol(s)
	}
}

func (c *Compiler) loadSymbol(s Symbol) {
	switch s.Scope {
	case GlobalScope:
		c.emit(code.OpGetGlobal, s.Index)
	case LocalScope:
		c.emit(code.OpGetLocal, s.Index)
	case BuiltinScope:
		c.emit(code.OpGetBuiltin, s.Index)
	case FreeScope:
		c.emit(code.OpGetFree, s.Index)
	case FunctionScope:
		c.emit(code.OpCurrentClosure)
	}
}

func (c *Compiler) emitConstant(obj object.Object) error {
	idx, err := c.addConstant(obj)
	if err != nil {
		return err
	}
	c.emit(code.OpConstant, idx)
	return nil
}

// addConstant interns obj and returns its index. The index is encoded in
// two bytes, so the pool is bounded; exceeding it must be a clear error
// rather than a silently truncated operand.
func (c *Compiler) addConstant(obj object.Object) (int, error) {
	if len(c.constants) >= 1<<16 {
		return 0, fmt.Errorf("constant pool overflow: more than %d constants", 1<<16)
	}
	c.constants = append(c.constants, obj)
	return len(c.constants) - 1, nil
}

func (c *Compiler) emit(op code.Opcode, operands ...int) int {
	ins := code.Make(op, operands...)
	pos := c.addInstruction(ins)
	c.setLastInstruction(op, pos)
	return pos
}

func (c *Compiler) addInstruction(ins []byte) int {
	pos := len(c.currentInstructions())
	c.scopes[c.scopeIndex].instructions = append(c.currentInstructions(), ins...)
	return pos
}

func (c *Compiler) currentInstructions() code.Instructions {
	return c.scopes[c.scopeIndex].instructions
}

func (c *Compiler) setLastInstruction(op code.Opcode, pos int) {
	s := &c.scopes[c.scopeIndex]
	s.previousInstruction = s.lastInstruction
	s.lastInstruction = EmittedInstruction{Opcode: op, Position: pos}
}

func (c *Compiler) lastInstructionIs(op code.Opcode) bool {
	if len(c.currentInstructions()) == 0 {
		return false
	}
	return c.scopes[c.scopeIndex].lastInstruction.Opcode == op
}

func (c *Compiler) removeLastPop() {
	s := &c.scopes[c.scopeIndex]
	s.instructions = s.instructions[:s.lastInstruction.Position]
	s.lastInstruction = s.previousInstruction
}

func (c *Compiler) replaceInstruction(pos int, newInstruction []byte) {
	ins := c.currentInstructions()
	copy(ins[pos:], newInstruction)
}

func (c *Compiler) replaceLastPopWithReturn() {
	pos := c.scopes[c.scopeIndex].lastInstruction.Position
	c.replaceInstruction(pos, code.Make(code.OpReturnValue))
	c.scopes[c.scopeIndex].lastInstruction.Opcode = code.OpReturnValue
}

// changeOperand back-patches a jump target once it is known.
func (c *Compiler) changeOperand(opPos, operand int) {
	op := code.Opcode(c.currentInstructions()[opPos])
	c.replaceInstruction(opPos, code.Make(op, operand))
}

func (c *Compiler) enterScope() {
	c.scopes = append(c.scopes, scope{})
	c.scopeIndex++
	c.symbolTable = NewEnclosedSymbolTable(c.symbolTable)
}

func (c *Compiler) leaveScope() code.Instructions {
	ins := c.currentInstructions()
	c.scopes = c.scopes[:len(c.scopes)-1]
	c.scopeIndex--
	c.symbolTable = c.symbolTable.Outer
	return ins
}

// sortedBuiltinNames is used by tests and tooling.
func sortedBuiltinNames() []string {
	names := make([]string, 0, len(object.Builtins))
	for _, b := range object.Builtins {
		names = append(names, b.Name)
	}
	sort.Strings(names)
	return names
}
