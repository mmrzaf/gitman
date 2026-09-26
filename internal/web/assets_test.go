package web

import (
	"strings"
	"testing"
	"testing/fstest"
)

// TestModuleImportsPointAtFingerprintedFiles covers a module importing
// another: the import is rewritten to the file's fingerprinted URL, and
// changing the imported file changes the importer's URL too, so a
// browser can never pair a new module with a cached old one.
func TestModuleImportsPointAtFingerprintedFiles(t *testing.T) {
	files := fstest.MapFS{
		"js/main.js": {Data: []byte("import \"./dom.js\";\nimport { on } from \"./dom.js\";\non();\n")},
		"js/dom.js":  {Data: []byte("export function on() {}\n")},
	}
	a, err := loadAssetsFrom(files)
	if err != nil {
		t.Fatal(err)
	}
	dom, main := a.urls["js/dom.js"], a.urls["js/main.js"]
	body := string(a.files[strings.TrimPrefix(main, "/assets/static/")])
	if strings.Count(body, `"`+dom+`"`) != 2 || strings.Contains(body, "./dom.js") {
		t.Fatalf("main.js = %q; want both imports of ./dom.js rewritten to %s", body, dom)
	}

	files["js/dom.js"] = &fstest.MapFile{Data: []byte("export function on() { return 1; }\n")}
	changed, err := loadAssetsFrom(files)
	if err != nil {
		t.Fatal(err)
	}
	if changed.urls["js/main.js"] == main {
		t.Fatal("changing dom.js left main.js at the same URL")
	}
}

func TestModuleImportsMustResolve(t *testing.T) {
	for name, files := range map[string]fstest.MapFS{
		"a missing file": {"js/main.js": {Data: []byte(`import "./gone.js";`)}},
		"a cycle": {
			"js/a.js": {Data: []byte(`import "./b.js";`)},
			"js/b.js": {Data: []byte(`import "./a.js";`)},
		},
	} {
		if _, err := loadAssetsFrom(files); err == nil {
			t.Errorf("%s: loaded without an error", name)
		}
	}
}

// TestShippedAssetsLoad covers the real static files: every module's
// imports resolve, and every one of them is served.
func TestShippedAssetsLoad(t *testing.T) {
	a, err := loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	for plain, u := range a.urls {
		body := string(a.files[strings.TrimPrefix(u, "/assets/static/")])
		if strings.Contains(body, `from "./`) || strings.Contains(body, `import "./`) {
			t.Errorf("%s still has an unrewritten relative import", plain)
		}
	}
}
