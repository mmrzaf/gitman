package web

import (
	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/repo"
)

type refView struct {
	Kind       git.Kind
	Name, Href string
}

func refModel(kind git.Kind, name, href string) refView {
	return refView{Kind: kind, Name: name, Href: href}
}

type commitView struct{ Repo, Hash string }

func commitModel(repository, hash string) commitView { return commitView{Repo: repository, Hash: hash} }

type emptyView struct {
	Icon, Title, Text string
	Compact           bool
}

func emptyModel(icon, title, text string, compact bool) emptyView {
	return emptyView{Icon: icon, Title: title, Text: text, Compact: compact}
}

type firstPushView struct{ Clone, Branch string }

func firstPushModel(clone, branch string) firstPushView {
	return firstPushView{Clone: clone, Branch: branch}
}

type revealView struct{ Label, Value, Note string }

func revealModel(label, value, note string) revealView {
	return revealView{Label: label, Value: value, Note: note}
}

type copyFieldView struct{ ID, Label, Value string }

func copyFieldModel(id, label, value string) copyFieldView {
	return copyFieldView{ID: id, Label: label, Value: value}
}

type feedView struct {
	Entries  []activity.Entry
	ShowRepo bool
}

func feedModel(entries []activity.Entry, showRepo bool) feedView {
	return feedView{Entries: entries, ShowRepo: showRepo}
}

type repoFeedView struct {
	Entries []activity.RepoEntry
	Repo    *repo.Repo
}

func repoFeedModel(entries []activity.RepoEntry, repository *repo.Repo) repoFeedView {
	return repoFeedView{Entries: entries, Repo: repository}
}

type commitTableView struct {
	Rows []commitRow
	Repo *repo.Repo
}

func commitTableModel(rows []commitRow, repository *repo.Repo) commitTableView {
	return commitTableView{Rows: rows, Repo: repository}
}

type diffView struct {
	Diff    *git.Diff
	Compact bool
}

func diffModel(diff *git.Diff, compact bool) diffView { return diffView{Diff: diff, Compact: compact} }

type refsView struct {
	Repo                 *repo.Repo
	Clone                string
	Refs                 []refRow
	Kind                 git.Kind
	DefaultExists, Admin bool
}

func refsModel(repository *repo.Repo, clone string, refs []refRow, kind git.Kind, defaultExists, admin bool) refsView {
	return refsView{Repo: repository, Clone: clone, Refs: refs, Kind: kind, DefaultExists: defaultExists, Admin: admin}
}

type refFieldView struct {
	ID, Name, Label, Value string
	Required               bool
	Clear                  string
}

func refFieldModel(id, name, label, value string, required bool, clear string) refFieldView {
	return refFieldView{ID: id, Name: name, Label: label, Value: value, Required: required, Clear: clear}
}

type ruleFormView struct {
	ID, Title     string
	Form          *form
	People        []auth.Person
	Action, Close string
	Edit          bool
}

func ruleFormModel(id, title string, f *form, people []auth.Person, action, close string, edit bool) ruleFormView {
	return ruleFormView{ID: id, Title: title, Form: f, People: people, Action: action, Close: close, Edit: edit}
}
