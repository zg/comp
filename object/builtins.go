package object

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Builtins is an ordered slice rather than a map: the compiler emits
// OpGetBuiltin with an index, so the order must be stable across compiler
// and VM.
var Builtins = []struct {
	Name    string
	Builtin *Builtin
}{
	{"len", &Builtin{Name: "len", Fn: builtinLen}},
	{"puts", &Builtin{Name: "puts", Fn: builtinPuts}},
	{"first", &Builtin{Name: "first", Fn: builtinFirst}},
	{"last", &Builtin{Name: "last", Fn: builtinLast}},
	{"rest", &Builtin{Name: "rest", Fn: builtinRest}},
	{"push", &Builtin{Name: "push", Fn: builtinPush}},
	{"keys", &Builtin{Name: "keys", Fn: builtinKeys}},
	{"values", &Builtin{Name: "values", Fn: builtinValues}},
	{"str", &Builtin{Name: "str", Fn: builtinStr}},
	{"int", &Builtin{Name: "int", Fn: builtinInt}},
	{"type", &Builtin{Name: "type", Fn: builtinType}},
	{"split", &Builtin{Name: "split", Fn: builtinSplit}},
	{"join", &Builtin{Name: "join", Fn: builtinJoin}},
	{"sort", &Builtin{Name: "sort", Fn: builtinSort}},
}

// GetBuiltinByName looks a builtin up for the compiler's symbol table.
func GetBuiltinByName(name string) *Builtin {
	for _, b := range Builtins {
		if b.Name == name {
			return b.Builtin
		}
	}
	return nil
}

// BuiltinIndex returns the index the compiler should emit, or -1.
func BuiltinIndex(name string) int {
	for i, b := range Builtins {
		if b.Name == name {
			return i
		}
	}
	return -1
}

// Stdout is where puts writes; a variable so tests can capture it.
var Stdout = newStdout()

func errf(format string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, args...)}
}

func wantArgs(name string, n int, args []Object) Object {
	if len(args) != n {
		return errf("wrong number of arguments to %s: want %d, got %d", name, n, len(args))
	}
	return nil
}

func builtinLen(args ...Object) Object {
	if e := wantArgs("len", 1, args); e != nil {
		return e
	}
	switch arg := args[0].(type) {
	case *String:
		return &Integer{Value: int64(len([]rune(arg.Value)))}
	case *Array:
		return &Integer{Value: int64(len(arg.Elements))}
	case *Hash:
		return &Integer{Value: int64(len(arg.Pairs))}
	default:
		return errf("argument to len must be STRING, ARRAY or HASH, got %s", args[0].Type())
	}
}

func builtinPuts(args ...Object) Object {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = a.Inspect()
	}
	fmt.Fprintln(Stdout, strings.Join(parts, " "))
	return nil
}

func builtinFirst(args ...Object) Object {
	if e := wantArgs("first", 1, args); e != nil {
		return e
	}
	arr, ok := args[0].(*Array)
	if !ok {
		return errf("argument to first must be ARRAY, got %s", args[0].Type())
	}
	if len(arr.Elements) == 0 {
		return nil
	}
	return arr.Elements[0]
}

func builtinLast(args ...Object) Object {
	if e := wantArgs("last", 1, args); e != nil {
		return e
	}
	arr, ok := args[0].(*Array)
	if !ok {
		return errf("argument to last must be ARRAY, got %s", args[0].Type())
	}
	if len(arr.Elements) == 0 {
		return nil
	}
	return arr.Elements[len(arr.Elements)-1]
}

func builtinRest(args ...Object) Object {
	if e := wantArgs("rest", 1, args); e != nil {
		return e
	}
	arr, ok := args[0].(*Array)
	if !ok {
		return errf("argument to rest must be ARRAY, got %s", args[0].Type())
	}
	if len(arr.Elements) == 0 {
		return nil
	}
	// Copy rather than reslice: sharing the backing array would let a
	// later push mutate both slices.
	out := make([]Object, len(arr.Elements)-1)
	copy(out, arr.Elements[1:])
	return &Array{Elements: out}
}

func builtinPush(args ...Object) Object {
	if e := wantArgs("push", 2, args); e != nil {
		return e
	}
	arr, ok := args[0].(*Array)
	if !ok {
		return errf("argument to push must be ARRAY, got %s", args[0].Type())
	}
	out := make([]Object, len(arr.Elements), len(arr.Elements)+1)
	copy(out, arr.Elements)
	return &Array{Elements: append(out, args[1])}
}

func builtinKeys(args ...Object) Object {
	if e := wantArgs("keys", 1, args); e != nil {
		return e
	}
	h, ok := args[0].(*Hash)
	if !ok {
		return errf("argument to keys must be HASH, got %s", args[0].Type())
	}
	out := make([]Object, 0, len(h.Order))
	for _, k := range h.Order {
		out = append(out, h.Pairs[k].Key)
	}
	return &Array{Elements: out}
}

func builtinValues(args ...Object) Object {
	if e := wantArgs("values", 1, args); e != nil {
		return e
	}
	h, ok := args[0].(*Hash)
	if !ok {
		return errf("argument to values must be HASH, got %s", args[0].Type())
	}
	out := make([]Object, 0, len(h.Order))
	for _, k := range h.Order {
		out = append(out, h.Pairs[k].Value)
	}
	return &Array{Elements: out}
}

func builtinStr(args ...Object) Object {
	if e := wantArgs("str", 1, args); e != nil {
		return e
	}
	return &String{Value: args[0].Inspect()}
}

func builtinInt(args ...Object) Object {
	if e := wantArgs("int", 1, args); e != nil {
		return e
	}
	switch arg := args[0].(type) {
	case *Integer:
		return arg
	case *Float:
		return &Integer{Value: int64(arg.Value)}
	case *String:
		n, err := strconv.ParseInt(strings.TrimSpace(arg.Value), 10, 64)
		if err != nil {
			return errf("int: cannot parse %q", arg.Value)
		}
		return &Integer{Value: n}
	default:
		return errf("argument to int must be INTEGER, FLOAT or STRING, got %s", args[0].Type())
	}
}

func builtinType(args ...Object) Object {
	if e := wantArgs("type", 1, args); e != nil {
		return e
	}
	return &String{Value: string(args[0].Type())}
}

func builtinSplit(args ...Object) Object {
	if e := wantArgs("split", 2, args); e != nil {
		return e
	}
	s, ok := args[0].(*String)
	if !ok {
		return errf("argument to split must be STRING, got %s", args[0].Type())
	}
	sep, ok := args[1].(*String)
	if !ok {
		return errf("separator for split must be STRING, got %s", args[1].Type())
	}
	var parts []string
	if sep.Value == "" {
		parts = strings.Fields(s.Value)
	} else {
		parts = strings.Split(s.Value, sep.Value)
	}
	out := make([]Object, len(parts))
	for i, p := range parts {
		out[i] = &String{Value: p}
	}
	return &Array{Elements: out}
}

func builtinJoin(args ...Object) Object {
	if e := wantArgs("join", 2, args); e != nil {
		return e
	}
	arr, ok := args[0].(*Array)
	if !ok {
		return errf("argument to join must be ARRAY, got %s", args[0].Type())
	}
	sep, ok := args[1].(*String)
	if !ok {
		return errf("separator for join must be STRING, got %s", args[1].Type())
	}
	parts := make([]string, len(arr.Elements))
	for i, e := range arr.Elements {
		parts[i] = e.Inspect()
	}
	return &String{Value: strings.Join(parts, sep.Value)}
}

func builtinSort(args ...Object) Object {
	if e := wantArgs("sort", 1, args); e != nil {
		return e
	}
	arr, ok := args[0].(*Array)
	if !ok {
		return errf("argument to sort must be ARRAY, got %s", args[0].Type())
	}
	out := make([]Object, len(arr.Elements))
	copy(out, arr.Elements)

	var sortErr Object
	num := func(o Object) (float64, bool) {
		switch o := o.(type) {
		case *Integer:
			return float64(o.Value), true
		case *Float:
			return o.Value, true
		}
		return 0, false
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lok := num(out[i])
		ri, rok := num(out[j])
		if lok && rok {
			return li < ri
		}
		ls, lsok := out[i].(*String)
		rs, rsok := out[j].(*String)
		if lsok && rsok {
			return ls.Value < rs.Value
		}
		if sortErr == nil {
			sortErr = errf("sort: cannot compare %s with %s", out[i].Type(), out[j].Type())
		}
		return false
	})
	if sortErr != nil {
		return sortErr
	}
	return &Array{Elements: out}
}
