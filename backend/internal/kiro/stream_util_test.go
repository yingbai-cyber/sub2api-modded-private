package kiro

import "testing"

func TestComputeCumulativeDelta(t *testing.T) {
	cases := []struct {
		name     string
		chunk    string
		previous string
		want     string
	}{
		{"empty previous returns chunk", "hello", "", "hello"},
		{"identical returns empty", "hello", "hello", ""},
		{"cumulative growth returns suffix", "hello world", "hello", " world"},
		{"chunk shorter and is prefix returns empty", "hel", "hello", ""},
		// Regression: chunk wholly contained in previous's suffix used to index
		// chunk[len(chunk)] and panic with "index out of range".
		{"chunk equals previous suffix returns empty", "XYZ", "abcXYZ", ""},
		{"single char suffix returns empty", "c", "abc", ""},
		{"partial overlap returns new tail", "cdef", "abcd", "ef"},
		{"no overlap returns chunk", "xyz", "abc", "xyz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computeCumulativeDelta(c.chunk, c.previous)
			if got != c.want {
				t.Errorf("computeCumulativeDelta(%q, %q) = %q; want %q",
					c.chunk, c.previous, got, c.want)
			}
		})
	}
}

// TestComputeCumulativeDeltaNeverPanics reproduces the shape seen in the
// production panic (chunk fully overlapping previous's suffix) and sweeps
// arbitrary byte offsets — including mid-rune ones — so any future bounds
// regression fails here instead of in a live stream.
func TestComputeCumulativeDeltaNeverPanics(t *testing.T) {
	prod := "这是一段思考文本内容需要处理"
	for i := range prod {
		_ = computeCumulativeDelta(prod[i:], prod)
		_ = computeCumulativeDelta(prod[:i], prod)
		_ = computeCumulativeDelta(prod, prod[i:])
		_ = computeCumulativeDelta(prod, prod[:i])
	}

	corpus := []string{"", "a", "ab", "abc", "abcd", "XYZ", "abcXYZ", "aXYZa", "你好世界"}
	for _, prev := range corpus {
		for _, chunk := range corpus {
			_ = computeCumulativeDelta(chunk, prev)
		}
	}
}
