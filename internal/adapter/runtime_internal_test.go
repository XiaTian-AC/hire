package adapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestCacheKey pins the compile-cache key formula from the spec:
// sha256(source content + "\0" + build command string) — pure Go, no
// toolchain needed.
func TestCacheKey(t *testing.T) {
	src := []byte("int main() { return 0; }")
	build := `g++ -std=c++20 -O2 -o {out} {src}`
	h := sha256.New()
	h.Write(src)
	h.Write([]byte{0})
	h.Write([]byte(build))
	want := hex.EncodeToString(h.Sum(nil))
	if got := cacheKey(src, build); got != want {
		t.Errorf("cacheKey formula mismatch: got %s, want %s", got, want)
	}
	if cacheKey(src, build) == cacheKey(append(bytes.Clone(src), '\n'), build) {
		t.Error("different source content must change the key")
	}
	if cacheKey(src, build) == cacheKey(src, build+" -g") {
		t.Error("different build string must change the key")
	}
}
