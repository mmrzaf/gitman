package handlers

import "testing"

func TestEmbeddedTemplatesLoad(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) == 0 {
		t.Fatal("no embedded page templates loaded")
	}
}
