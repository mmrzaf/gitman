package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/git"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

//go:embed templates
var templateFiles embed.FS

// views holds one parsed template set per page: the shared layout and
// partials, plus that page's own "content" block.
type views struct {
	pages map[string]*template.Template
}

func loadViews(assets *assets) (*views, error) {
	funcs := template.FuncMap{
		"asset":          assets.url,
		"ago":            ago,
		"iso":            func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"datetime":       func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
		"short":          shortHash,
		"field":          newField,
		"dict":           dict,
		"eventText":      eventText,
		"add":            func(a, b int) int { return a + b },
		"duration":       formatDuration,
		"deref":          func(p *int) int { return *p },
		"lineKind":       lineKind,
		"dirOf":          dirOf,
		"refURL":         refURL,
		"historyURL":     historyURL,
		"compareURL":     compareURL,
		"compareFormURL": compareFormURL,
		"diffTotals":     diffTotals,
	}
	base, err := template.New("").Funcs(funcs).ParseFS(templateFiles, "templates/layout.html", "templates/partials.html")
	if err != nil {
		return nil, fmt.Errorf("parse layout: %w", err)
	}
	names, err := fs.Glob(templateFiles, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	v := &views{pages: map[string]*template.Template{}}
	for _, name := range names {
		set, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if set, err = set.ParseFS(templateFiles, name); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		v.pages[strings.TrimSuffix(path.Base(name), ".html")] = set
	}
	return v, nil
}

// pageData is what the layout sees; Data is the page's own view model.
type pageData struct {
	Title  string
	Name   string
	Person *auth.Person
	Flash  *flash
	Data   any
	// EventStream is the event stream the page listens to for live
	// updates, or empty for a page that does not change on its own.
	EventStream string
	// Frame is the repository a page belongs to, for the bar that names
	// it and links its sections; nil on a page outside any repository.
	Frame *repoFrame
}

// liveSource is implemented by a page's data when the page updates
// itself while open.
type liveSource interface {
	LiveEvents() string
}

// repoFrame is embedded in the data of every page inside a repository:
// the repository, and which of its sections — "overview", "files",
// "history", "runs" or "settings" — the page is, if any.
type repoFrame struct {
	Repo    *reposvc.Repo
	Section string
}

func (f repoFrame) frame() *repoFrame { return &f }

type framed interface{ frame() *repoFrame }

// oneOf returns value if it is one of options, else fallback. Pages use
// it to read which tab or dialog a URL asks for, so an unknown value is
// the default view rather than an error.
func oneOf(value, fallback string, options ...string) string {
	for _, o := range options {
		if value == o {
			return value
		}
	}
	return fallback
}

// tabFrom is the tab a request's ?tab= selects among tabs, the first
// being the default.
func tabFrom(r *http.Request, tabs ...string) string {
	return oneOf(r.URL.Query().Get("tab"), tabs[0], tabs...)
}

// dialogFrom is the dialog a request's ?dialog= opens, if it is one of
// dialogs: a link to a form that lives in a dialog, which works with
// scripting off too.
func dialogFrom(r *http.Request, dialogs ...string) string {
	return oneOf(r.URL.Query().Get("dialog"), "", dialogs...)
}

// render executes a page into a buffer first, so a template failure
// becomes a clean error response instead of half a page.
func (a *App) render(w http.ResponseWriter, r *http.Request, status int, name, title string, data any) {
	set, ok := a.views.pages[name]
	if !ok {
		a.log.Error("unknown page template", "page", name)
		http.Error(w, "Internal error.", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	page := pageData{Title: title, Name: name, Person: personFrom(r), Flash: flashFrom(r), Data: data}
	if live, ok := data.(liveSource); ok {
		page.EventStream = live.LiveEvents()
	}
	if f, ok := data.(framed); ok {
		page.Frame = f.frame()
	}
	err := set.ExecuteTemplate(&buf, "layout", page)
	if err != nil {
		a.log.Error("render page", "page", name, "error", err)
		http.Error(w, "Internal error.", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// Pages are revalidated on every visit but still kept for Back and
	// Forward, which restore them instantly from the browser's cache — all
	// but a page showing a secret, which noStore has already marked.
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// noStore keeps the page about to be rendered out of every cache,
// including the browser's history: it shows a secret meant to be seen
// once.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// failForm puts a refused submission's reason on f — on field, or on the
// form as a whole when field is empty — and reports whether it did. Only
// a refusal the person can act on goes on the form: any other error is
// left to the caller to return, so it becomes an error page and is
// logged.
func failForm(f *form, field string, err error) bool {
	switch apperr.KindOf(err) {
	case apperr.KindInvalid, apperr.KindConflict:
	default:
		return false
	}
	message := sentence(apperr.PublicMessage(err))
	if field == "" {
		f.Error = message
	} else {
		f.Fail(field, message)
	}
	return true
}

// sentence makes a validation message read as a sentence on a page:
// capitalized, and ending in a full stop.
func sentence(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[size:]
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, "!") {
		s += "."
	}
	return s
}

// form carries a submission's values and its validation errors, so a
// form that fails is shown again with what the person typed and the
// problem next to the field that has it.
type form struct {
	Values url.Values
	Errors map[string]string
	// Error is a problem with the submission as a whole.
	Error string
}

func newForm(values url.Values) *form {
	if values == nil {
		values = url.Values{}
	}
	return &form{Values: values, Errors: map[string]string{}}
}

func (f *form) Get(name string) string { return strings.TrimSpace(f.Values.Get(name)) }

func (f *form) Fail(name, message string) {
	if _, exists := f.Errors[name]; !exists {
		f.Errors[name] = message
	}
}

func (f *form) Valid() bool { return len(f.Errors) == 0 && f.Error == "" }

// Has reports whether the form holds name — a ticked checkbox.
func (f *form) Has(name string) bool { return f.Values.Has(name) }

// Picked reports whether value is among the values of name: one of a
// group of checkboxes that share a name.
func (f *form) Picked(name, value string) bool {
	for _, v := range f.Values[name] {
		if v == value {
			return true
		}
	}
	return false
}

// formField is one labelled input, rendered by the "field" partial.
type formField struct {
	Name, ID, Label, Type, Value, Error, Hint, Autocomplete string
	Required, Autofocus                                     bool
}

// newField builds a field from a form. Options are "required",
// "autofocus", "autocomplete:<value>", "hint:<text>" and "id:<value>". A
// password field never echoes its value back into the page. ID defaults
// to Name; set it explicitly when two fields on the same page share a
// POST field name — two forms posting to the same handler, say — so
// their generated element ids do not collide.
func newField(f *form, name, label, typ string, options ...string) formField {
	field := formField{Name: name, ID: name, Label: label, Type: typ}
	if f != nil {
		field.Error = f.Errors[name]
		if typ != "password" {
			field.Value = f.Values.Get(name)
		}
	}
	for _, opt := range options {
		switch {
		case opt == "required":
			field.Required = true
		case opt == "autofocus":
			field.Autofocus = true
		case strings.HasPrefix(opt, "autocomplete:"):
			field.Autocomplete = strings.TrimPrefix(opt, "autocomplete:")
		case strings.HasPrefix(opt, "hint:"):
			field.Hint = strings.TrimPrefix(opt, "hint:")
		case strings.HasPrefix(opt, "id:"):
			field.ID = strings.TrimPrefix(opt, "id:")
		}
	}
	return field
}

// ago formats how long ago t was — or, for a time still to come such as
// a token's expiry, how long until it — in the same words the page's
// script uses when it keeps the text current.
func ago(t time.Time) string {
	d := time.Since(t)
	span := d.Abs()
	var amount string
	switch {
	case span < time.Minute:
		return "just now"
	case span < time.Hour:
		amount = fmt.Sprintf("%d min", int(span.Minutes()))
	case span < 24*time.Hour:
		amount = fmt.Sprintf("%d h", int(span.Hours()))
	case span < 30*24*time.Hour:
		amount = fmt.Sprintf("%d d", int(span.Hours()/24))
	default:
		return t.UTC().Format("2006-01-02")
	}
	if d < 0 {
		return "in " + amount
	}
	return amount + " ago"
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// dict builds a map from alternating string keys and values, so a
// template can pass more than one named value into a shared partial —
// text/template only lets {{template}} pass a single value otherwise.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %v is not a string", pairs[i])
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}

// eventDescriptions maps a settings-change event's action to the phrase
// the timeline shows for it. It is a package variable, not a switch,
// so a new action added in internal/activity is a one-line addition here
// too, in the same shape.
var eventDescriptions = map[string]string{
	activity.RepoCreated:    "created the repository",
	activity.RepoDeleted:    "deleted the repository",
	activity.RepoDescribed:  "updated the description",
	activity.RuleSaved:      "saved a ref rule",
	activity.RuleDeleted:    "removed a ref rule",
	activity.SecretSet:      "set a secret",
	activity.SecretDeleted:  "removed a secret",
	activity.PersonAdded:    "added a person",
	activity.PersonDisabled: "disabled a person",
	activity.PersonEnabled:  "enabled a person",
	activity.PersonRole:     "changed a role",
	activity.PasswordReset:  "reset a password",

	activity.RepoVisibilityChanged:    "changed who may read the repository",
	activity.RepoReaderAdded:          "added a reader",
	activity.RepoReaderRemoved:        "removed a reader",
	activity.RepoDefaultPushChanged:   "changed the default push policy",
	activity.RepoDefaultBranchChanged: "changed the default branch",
}

// eventText renders a settings-change event's action and detail as one
// line of prose for the activity.
func eventText(action, detail string) string {
	phrase, ok := eventDescriptions[action]
	if !ok {
		phrase = action
	}
	if detail == "" {
		return phrase
	}
	return phrase + ": " + detail
}

// lineKind names a diff line's kind for use as a CSS class suffix.
func lineKind(k git.LineKind) string {
	switch k {
	case git.LineAdded:
		return "add"
	case git.LineDeleted:
		return "del"
	case git.LineNoNewline:
		return "nonewline"
	default:
		return "context"
	}
}

// dirOf returns the parent of a repository-relative path ("" at the
// root), unlike path.Dir, which returns "." there — a value that would
// render as a literal "." in a link instead of the root.
func dirOf(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

// diffStat is the total size of a diff's shown changes.
type diffStat struct {
	Files, Additions, Deletions int
}

func diffTotals(d *git.Diff) diffStat {
	s := diffStat{Files: len(d.Files) + d.MoreFiles}
	for _, f := range d.Files {
		s.Additions += f.Additions
		s.Deletions += f.Deletions
	}
	return s
}

// splitLines splits file content into lines for line-numbered display,
// dropping a single trailing newline (its absence, not an empty final
// line, is what a "no newline at end of file" marker would show, and
// this view doesn't need that marker).
func splitLines(content string) []string {
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

// A flash is the one-line outcome of an action, shown as a notice on the
// page the action redirected to. It lives in a short-lived cookie read on the very
// next page and cleared at once, so refreshing that page shows nothing
// new and never repeats the action.
type flash struct {
	Kind    flashKind `json:"k"`
	Message string    `json:"m"`
}

type flashKind string

const (
	flashSuccess flashKind = "success"
	flashInfo    flashKind = "info"
	flashError   flashKind = "error"
)

// Icon is the icon a flash is shown with.
func (f flash) Icon() string {
	switch f.Kind {
	case flashSuccess:
		return "check"
	case flashError:
		return "warning"
	}
	return "info"
}

const maxFlashBytes = 1024

func (a *App) flashCookie() string { return a.cookieName("gitman_flash") }

// redirect finishes an action: it records the flash and sends the browser
// to target with 303 See Other, so a refresh reloads the destination
// instead of resubmitting the form.
func (a *App) redirect(w http.ResponseWriter, r *http.Request, target string, kind flashKind, message string) {
	if message != "" {
		data, err := json.Marshal(flash{Kind: kind, Message: message})
		if err == nil && len(data) <= maxFlashBytes {
			http.SetCookie(w, &http.Cookie{
				Name:     a.flashCookie(),
				Value:    base64.RawURLEncoding.EncodeToString(data),
				Path:     "/",
				MaxAge:   60,
				HttpOnly: true,
				Secure:   a.secure,
				SameSite: http.SameSiteLaxMode,
			})
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

type flashKey struct{}

// withFlash takes the pending flash, if any, and clears it.
func (a *App) withFlash(w http.ResponseWriter, r *http.Request) *http.Request {
	cookie, err := r.Cookie(a.flashCookie())
	if err != nil {
		return r
	}
	http.SetCookie(w, &http.Cookie{
		Name: a.flashCookie(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	data, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(data) > maxFlashBytes {
		return r
	}
	var f flash
	if json.Unmarshal(data, &f) != nil || f.Message == "" {
		return r
	}
	switch f.Kind {
	case flashSuccess, flashInfo, flashError:
	default:
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), flashKey{}, &f))
}

func flashFrom(r *http.Request) *flash {
	f, _ := r.Context().Value(flashKey{}).(*flash)
	return f
}

// formatDuration shows a duration the way a person reads one: "<1s",
// "45s", "3m 05s", "1h 02m".
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Second:
		return "<1s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}
