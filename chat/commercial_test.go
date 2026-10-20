package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestComAllowlist(t *testing.T) {
	id := "0b8f6a2e-1c3d-4e5f-8a9b-0c1d2e3f4a5b"
	ok := [][2]string{{"GET", "/me"}, {"POST", "/quotes"}, {"POST", "/quotes/" + id + "/approve"}, {"GET", "/quotes/" + id + "/events"}, {"PATCH", "/settings"}, {"POST", "/intent"}, {"GET", "/exports/" + id}, {"POST", "/quotes/" + id + "/export"}, {"POST", "/catalog/" + id + "/prices"}}
	bad := [][2]string{{"DELETE", "/quotes/" + id}, {"GET", "/../admin"}, {"POST", "/quotes/" + id + "/delete"}, {"GET", "/quotes/x"}, {"GET", "/me/"}, {"PUT", "/settings"}, {"DELETE", "/catalog/" + id}, {"GET", "/exports/../x"}}
	for _, c := range ok {
		if !comAllowed(c[0], c[1]) {
			t.Errorf("should allow %v", c)
		}
	}
	for _, c := range bad {
		if comAllowed(c[0], c[1]) {
			t.Errorf("should deny %v", c)
		}
	}
}

func TestComMuxNoConflict(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /chat/", func(http.ResponseWriter, *http.Request) {})
	for _, m := range []string{"GET", "POST", "PATCH"} {
		mux.HandleFunc(m+" /chat/api/commercial/", func(http.ResponseWriter, *http.Request) {})
	}
}

func TestCommercialUIFlag(t *testing.T) {
	b, _ := static.ReadFile("static/chat.html")
	off := string(versioned((&app{}).withCommercialUI(b)))
	on := string(versioned((&app{com: &commercialProxy{uiFlag: true}}).withCommercialUI(b)))
	if strings.Contains(off, "quotes.js") || !strings.Contains(on, `/chat/static/quotes.js?v=`+assetVer["static/quotes.js"]) || !strings.Contains(on, "quotes.css?v=") {
		t.Fatal("flag injection wrong")
	}
}
