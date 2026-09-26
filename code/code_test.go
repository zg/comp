package code

import "testing"

func TestMake(t *testing.T) {
	tests := []struct {
		op       Opcode
		operands []int
		want     []byte
	}{
		{OpConstant, []int{65534}, []byte{byte(OpConstant), 255, 254}},
		{OpAdd, nil, []byte{byte(OpAdd)}},
		{OpGetLocal, []int{255}, []byte{byte(OpGetLocal), 255}},
		{OpClosure, []int{65534, 255}, []byte{byte(OpClosure), 255, 254, 255}},
	}
	for _, tt := range tests {
		ins := Make(tt.op, tt.operands...)
		if len(ins) != len(tt.want) {
			t.Fatalf("length = %d, want %d", len(ins), len(tt.want))
		}
		for i, b := range tt.want {
			if ins[i] != b {
				t.Errorf("byte %d = %d, want %d", i, ins[i], b)
			}
		}
	}
}

// Every instruction must decode back to exactly what was encoded, or the
// VM and compiler would silently disagree.
func TestReadOperandsRoundTrip(t *testing.T) {
	tests := []struct {
		op        Opcode
		operands  []int
		bytesRead int
	}{
		{OpConstant, []int{65535}, 2},
		{OpGetLocal, []int{255}, 1},
		{OpClosure, []int{65535, 255}, 3},
	}
	for _, tt := range tests {
		ins := Make(tt.op, tt.operands...)
		def, err := Lookup(byte(tt.op))
		if err != nil {
			t.Fatal(err)
		}
		got, n := ReadOperands(def, ins[1:])
		if n != tt.bytesRead {
			t.Errorf("read %d bytes, want %d", n, tt.bytesRead)
		}
		for i, want := range tt.operands {
			if got[i] != want {
				t.Errorf("operand %d = %d, want %d", i, got[i], want)
			}
		}
	}
}

// Every defined opcode must round-trip, so a newly added one cannot be
// forgotten in ReadOperands.
func TestAllOpcodesRoundTrip(t *testing.T) {
	for op, def := range definitions {
		operands := make([]int, len(def.OperandWidths))
		for i, w := range def.OperandWidths {
			if w == 1 {
				operands[i] = 200
			} else {
				operands[i] = 40000
			}
		}
		ins := Make(op, operands...)
		if len(ins) == 0 {
			t.Errorf("%s: Make returned nothing", def.Name)
			continue
		}
		got, _ := ReadOperands(def, ins[1:])
		for i := range operands {
			if got[i] != operands[i] {
				t.Errorf("%s operand %d: got %d, want %d", def.Name, i, got[i], operands[i])
			}
		}
	}
}

func TestInstructionsString(t *testing.T) {
	ins := Instructions{}
	for _, i := range []Instructions{
		Make(OpAdd),
		Make(OpGetLocal, 1),
		Make(OpConstant, 2),
		Make(OpClosure, 65535, 255),
	} {
		ins = append(ins, i...)
	}
	want := `0000 OpAdd
0001 OpGetLocal 1
0003 OpConstant 2
0006 OpClosure 65535 255
`
	if got := ins.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestLookupUnknownOpcode(t *testing.T) {
	if _, err := Lookup(254); err == nil {
		t.Error("expected an error for an undefined opcode")
	}
}
