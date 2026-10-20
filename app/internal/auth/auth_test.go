package auth

import "testing"

func TestParseBearer(t *testing.T) {
	cases := map[string]bool{"Bearer abc": true, "bearer abc": true, "Bearer ": false, "Basic abc": false, "": false}
	for in, want := range cases {
		if _, ok := ParseBearer(in); ok != want {
			t.Errorf("%q: got %v want %v", in, ok, want)
		}
	}
}

func TestHashToken(t *testing.T) {
	if len(HashToken("x")) != 32 {
		t.Fatal("sha256 must be 32 bytes")
	}
}
