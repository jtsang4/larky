package plugincheck

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestPluginBundlesAreComplete(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	if err := Validate(root); err != nil {
		t.Fatal(err)
	}
}
