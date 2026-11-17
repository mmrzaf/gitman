package web

import "testing"

func TestFlashNoticeCannotBeForged(t *testing.T) {
	app := &App{flashSignKey: []byte("private random test signing key 123")}
	payload := []byte(`{"k":"success","m":"All systems passed."}`)
	signed := app.signedFlash(payload)
	if got, ok := app.verifyFlash(signed); !ok || string(got) != string(payload) {
		t.Fatal("signed flash did not verify")
	}
	if _, ok := app.verifyFlash("eyJrIjoic3VjY2VzcyIsIm0iOiJGb3JnZWQifQ"); ok {
		t.Fatal("unsigned flash was accepted")
	}
	if _, ok := app.verifyFlash("garbage." + signed); ok {
		t.Fatal("malformed flash was accepted")
	}
	tampered := []byte(signed)
	tampered[0] = 'A'
	if string(tampered) == signed {
		tampered[0] = 'B'
	}
	if _, ok := app.verifyFlash(string(tampered)); ok {
		t.Fatal("tampered flash signature was accepted")
	}
}
