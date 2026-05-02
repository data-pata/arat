package output

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJSONRecord_text(t *testing.T) {
	var out, errBuf bytes.Buffer
	w := &Writer{Out: &out, Err: &errBuf, Format: Text}
	w.JSONRecord(map[string]any{"a": 1}, func(o io.Writer) { fmt.Fprint(o, "human text") })
	assert.Equal(t, "human text", out.String())
	assert.Empty(t, errBuf.String())
}

func TestJSONRecord_json(t *testing.T) {
	var out, errBuf bytes.Buffer
	w := &Writer{Out: &out, Err: &errBuf, Format: JSON}
	w.JSONRecord(map[string]any{"a": 1}, func(o io.Writer) {
		t.Errorf("textFn must not run when Format==JSON")
	})
	assert.Contains(t, out.String(), `"a": 1`)
	assert.Empty(t, errBuf.String())
}

func TestErrorf_Warnf_Infof(t *testing.T) {
	var out, errBuf bytes.Buffer
	w := &Writer{Out: &out, Err: &errBuf, Format: Text}
	w.Errorf("boom %d", 42)
	w.Warnf("careful %s", "you")
	w.Infof("hi %s", "there")
	assert.Equal(t, "error: boom 42\nwarn: careful you\nhi there\n", errBuf.String())
	assert.Empty(t, out.String())
}
