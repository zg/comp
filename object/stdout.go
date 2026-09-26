package object

import (
	"io"
	"os"
)

func newStdout() io.Writer { return os.Stdout }
