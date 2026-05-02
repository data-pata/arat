// Package output handles human/JSON output formatting for arat commands.
package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Format is human-readable text or JSON.
type Format int

const (
	Text Format = iota
	JSON
)

// Writer writes results in the requested format. Stdout is for results;
// stderr is for operational messages (Errorf, Warnf, Infof).
type Writer struct {
	Out    io.Writer
	Err    io.Writer
	Format Format
}

// JSONRecord prints v as pretty JSON when Format==JSON, otherwise calls textFn.
func (w *Writer) JSONRecord(v any, textFn func(out io.Writer)) {
	if w.Format == JSON {
		enc := json.NewEncoder(w.Out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		return
	}
	textFn(w.Out)
}

func (w *Writer) Errorf(format string, args ...any) {
	fmt.Fprintf(w.Err, "error: "+format+"\n", args...)
}

func (w *Writer) Warnf(format string, args ...any) {
	fmt.Fprintf(w.Err, "warn: "+format+"\n", args...)
}

func (w *Writer) Infof(format string, args ...any) {
	fmt.Fprintf(w.Err, format+"\n", args...)
}
