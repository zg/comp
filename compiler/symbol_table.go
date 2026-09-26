package compiler

// Scope describes where a symbol lives, which determines the opcode used to
// read it.
type Scope string

const (
	GlobalScope   Scope = "GLOBAL"
	LocalScope    Scope = "LOCAL"
	BuiltinScope  Scope = "BUILTIN"
	FreeScope     Scope = "FREE"
	FunctionScope Scope = "FUNCTION" // the currently executing function, for recursion
)

// Symbol is a resolved name.
type Symbol struct {
	Name  string
	Scope Scope
	Index int
}

// SymbolTable maps names to storage locations, one table per lexical scope.
type SymbolTable struct {
	Outer *SymbolTable

	// FreeSymbols records, in order, the enclosing-scope symbols this scope
	// captured. The compiler emits code to push each one before OpClosure.
	FreeSymbols []Symbol

	store          map[string]Symbol
	numDefinitions int
}

func NewSymbolTable() *SymbolTable {
	return &SymbolTable{store: map[string]Symbol{}, FreeSymbols: []Symbol{}}
}

func NewEnclosedSymbolTable(outer *SymbolTable) *SymbolTable {
	s := NewSymbolTable()
	s.Outer = outer
	return s
}

// Define binds name in this scope.
func (s *SymbolTable) Define(name string) Symbol {
	// Redefining a name in the same scope reuses its slot rather than
	// allocating a second one, so `let x = 1; let x = 2;` does not leak.
	if existing, ok := s.store[name]; ok && existing.Scope != BuiltinScope {
		return existing
	}
	sym := Symbol{Name: name, Index: s.numDefinitions}
	if s.Outer == nil {
		sym.Scope = GlobalScope
	} else {
		sym.Scope = LocalScope
	}
	s.store[name] = sym
	s.numDefinitions++
	return sym
}

// DefineBuiltin binds a builtin at a fixed index.
func (s *SymbolTable) DefineBuiltin(index int, name string) Symbol {
	sym := Symbol{Name: name, Index: index, Scope: BuiltinScope}
	s.store[name] = sym
	return sym
}

// DefineFunctionName binds the name of the function currently being
// compiled, so a recursive call resolves to the closure itself rather than
// to a global that does not exist yet.
func (s *SymbolTable) DefineFunctionName(name string) Symbol {
	sym := Symbol{Name: name, Index: 0, Scope: FunctionScope}
	s.store[name] = sym
	return sym
}

// defineFree records a captured symbol and returns its free-scope handle.
func (s *SymbolTable) defineFree(original Symbol) Symbol {
	s.FreeSymbols = append(s.FreeSymbols, original)
	sym := Symbol{Name: original.Name, Index: len(s.FreeSymbols) - 1, Scope: FreeScope}
	s.store[original.Name] = sym
	return sym
}

// Resolve looks name up, walking outward. A local of an enclosing function
// becomes a free variable of this one, which is how closures capture.
func (s *SymbolTable) Resolve(name string) (Symbol, bool) {
	sym, ok := s.store[name]
	if ok || s.Outer == nil {
		return sym, ok
	}

	sym, ok = s.Outer.Resolve(name)
	if !ok {
		return sym, false
	}
	// Globals and builtins are reachable from anywhere, so they are not
	// captured; only an enclosing function's locals and frees are.
	if sym.Scope == GlobalScope || sym.Scope == BuiltinScope {
		return sym, true
	}
	return s.defineFree(sym), true
}

// NumDefinitions is the number of slots this scope needs.
func (s *SymbolTable) NumDefinitions() int { return s.numDefinitions }
