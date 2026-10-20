package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestVecRoundTrip(t *testing.T) {
	v, err := ParseVec(VecLiteral([]float32{0.5, -1, 3e-5}))
	if err != nil || len(v) != 3 || v[0] != 0.5 || v[1] != -1 {
		t.Fatalf("%v %v", v, err)
	}
}

func TestChunk(t *testing.T) {
	text := strings.Repeat("palabra ", 600) + "\n\nfinal"
	cs := Chunk(text, 1000)
	if len(cs) < 5 {
		t.Fatalf("expected several chunks, got %d", len(cs))
	}
	for _, c := range cs {
		if len(c) > 1000 {
			t.Fatalf("chunk too long: %d", len(c))
		}
	}
	if Chunk("  ", 100) != nil {
		t.Fatal("empty text must give no chunks")
	}
}

func TestChunkUTF8(t *testing.T) {
	s := strings.Repeat("ñá", 3000)
	for _, c := range Chunk(s, 1001) {
		if !utf8.ValidString(c) {
			t.Fatal("chunk split a rune")
		}
	}
}
