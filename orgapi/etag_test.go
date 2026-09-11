package orgapi

import "testing"

func TestETagMatches(t *testing.T) {
	const v = 48123
	cases := map[string]bool{
		`"48123"`:           true,
		`48123`:             true, // bare integer: what a client that stored the number sends
		`W/"48123"`:         true,
		`*`:                 true,
		`"1", "48123"`:      true,
		`"48122"`:           false,
		`48124`:             false,
		``:                  false,
		`"abc"`:             false,
		`"48123x"`:          false,
		` "48123" `:         true,
		`"1",   W/"48123" `: true,
	}
	for in, want := range cases {
		if got := ETagMatches(in, v); got != want {
			t.Errorf("ETagMatches(%q, %d) = %v, want %v", in, v, got, want)
		}
	}
	if got := ETag(v); got != `"48123"` {
		t.Errorf("ETag = %s", got)
	}
}

func TestParseETag(t *testing.T) {
	for in, want := range map[string]int64{`"7"`: 7, `7`: 7, `W/"7"`: 7, ` "7" `: 7} {
		if got, ok := ParseETag(in); !ok || got != want {
			t.Errorf("ParseETag(%q) = %d, %v", in, got, ok)
		}
	}
	for _, in := range []string{``, `*`, `"x"`, `"7x"`} {
		if _, ok := ParseETag(in); ok {
			t.Errorf("ParseETag(%q) accepted", in)
		}
	}
}
