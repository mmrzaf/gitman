package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	cipipeline "github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

const maxArtifactPreviewBytes int64 = 512 * 1024

type CIRunView struct {
	Run           models.CIRun
	Commit        *git.Commit
	RefLabel      string
	RefKind       string
	IsCurrent     bool
	AttemptNumber int
}

type CILogSection struct {
	Name        string
	Kind        string
	Status      string
	Output      string
	Duration    string
	ExitCode    int
	StartedAt   *time.Time
	CompletedAt *time.Time
	Open        bool
}

type CILogView struct {
	Setup    CILogSection
	Steps    []CILogSection
	Finalize CILogSection
}

type CIConfigView struct {
	Found       bool
	Valid       bool
	Error       string
	Image       string
	Docker      bool
	EnvCount    int
	SecretCount int
	Steps       []cipipeline.Step
}

type ArtifactNode struct {
	Name        string
	Path        string
	IsDir       bool
	Size        int64
	Previewable bool
	DownloadURL string
	PreviewURL  string
	Children    []*ArtifactNode
}

type artifactFile struct {
	Path        string
	Size        int64
	Previewable bool
}

func newCIRunView(run models.CIRun, commit *git.Commit) CIRunView {
	view := CIRunView{Run: run, Commit: commit}
	switch {
	case run.Branch != "":
		view.RefLabel, view.RefKind = run.Branch, "branch"
	case run.Tag != "":
		view.RefLabel, view.RefKind = run.Tag, "tag"
	default:
		view.RefLabel, view.RefKind = shortString(run.CommitHash, 12), "commit"
	}
	return view
}

func loadCIRunViews(ctx context.Context, repoPath string, runs []models.CIRun) []CIRunView {
	hashes := make([]string, 0, len(runs))
	for _, run := range runs {
		if run.CommitHash != "" {
			hashes = append(hashes, run.CommitHash)
		}
	}
	commits, err := git.GetCommitsByHashes(ctx, repoPath, hashes)
	if err != nil {
		// A stale/unreachable run revision should not erase metadata for every
		// other run on the page. Fall back per hash only when the fast batch
		// lookup fails.
		commits = map[string]git.Commit{}
		seen := make(map[string]struct{}, len(hashes))
		for _, hash := range hashes {
			if _, ok := seen[hash]; ok {
				continue
			}
			seen[hash] = struct{}{}
			items, itemErr := git.GetCommits(ctx, repoPath, hash, 0, 1)
			if itemErr == nil && len(items) == 1 {
				commits[items[0].Hash] = items[0]
			}
		}
	}
	views := make([]CIRunView, 0, len(runs))
	for _, run := range runs {
		var commit *git.Commit
		if found, ok := commits[run.CommitHash]; ok {
			copy := found
			commit = &copy
		}
		views = append(views, newCIRunView(run, commit))
	}
	return views
}

func loadCIConfigView(ctx context.Context, repoPath, commitHash string) (*cipipeline.Config, CIConfigView) {
	view := CIConfigView{}
	if commitHash == "" {
		return nil, view
	}
	exists, err := git.BlobExists(ctx, repoPath, commitHash, cipipeline.ConfigFile)
	if err != nil || !exists {
		return nil, view
	}
	data, err := git.GetBlob(ctx, repoPath, commitHash, cipipeline.ConfigFile)
	if err != nil {
		return nil, view
	}
	view.Found = true
	cfg, err := cipipeline.ParseConfigBytes(data)
	if err != nil {
		view.Error = err.Error()
		return nil, view
	}
	view.Valid = true
	view.Image = cfg.Image
	view.Docker = cfg.Docker
	view.EnvCount = len(cfg.Env)
	for _, entry := range cfg.Env {
		if entry.Secret != "" {
			view.SecretCount++
		}
	}
	view.Steps = append([]cipipeline.Step(nil), cfg.Steps...)
	return cfg, view
}

var logTimestampRe = regexp.MustCompile(`^\[?(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)\]?\s+(.*)$`)
var fallbackStepStartRe = regexp.MustCompile(`^--- Step: (.*) ---$`)
var fallbackStepSuccessRe = regexp.MustCompile(`^--- Step: (.*): SUCCESS ---$`)
var fallbackStepFailedRe = regexp.MustCompile(`^--- Step: (.*): FAILED \(exit ([0-9]+)\) ---$`)

func parseLogLine(line string) (time.Time, string, bool) {
	match := logTimestampRe.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
	if match == nil {
		return time.Time{}, line, false
	}
	timestamp, err := time.Parse(time.RFC3339, match[1])
	if err != nil {
		return time.Time{}, match[2], false
	}
	return timestamp, match[2], true
}

func appendLogOutput(dst *strings.Builder, line string) {
	if dst.Len() > 0 {
		dst.WriteByte('\n')
	}
	dst.WriteString(line)
}

func durationLabel(start, end *time.Time) string {
	if start == nil || end == nil || end.Before(*start) {
		return ""
	}
	return compactDuration(end.Sub(*start))
}

func compactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		if d%time.Second == 0 {
			return fmt.Sprintf("%ds", int(d/time.Second))
		}
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	minutes := int(d / time.Minute)
	seconds := int((d % time.Minute) / time.Second)
	if seconds == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dm %ds", minutes, seconds)
}

func parseCILog(content string, run *models.CIRun, cfg *cipipeline.Config) CILogView {
	view := CILogView{
		Setup:    CILogSection{Name: "Setup", Kind: "setup", Status: "pending", Open: false},
		Finalize: CILogSection{Name: "Finalize", Kind: "finalize", Status: "pending", Open: false},
	}
	if cfg != nil {
		view.Steps = make([]CILogSection, len(cfg.Steps))
		for i, step := range cfg.Steps {
			view.Steps[i] = CILogSection{Name: step.Name, Kind: "step", Status: "pending"}
		}
	}

	var setupOut, finalizeOut strings.Builder
	stepOut := make([]strings.Builder, len(view.Steps))
	active := -1
	next := 0
	startedAny := false
	completedAny := false
	pipelineStopped := false

	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}
	for _, rawLine := range lines {
		ts, message, hasTS := parseLogLine(rawLine)

		if active >= 0 && active < len(view.Steps) {
			name := view.Steps[active].Name
			if message == "--- Step: "+name+": SUCCESS ---" {
				view.Steps[active].Status = "success"
				if hasTS {
					t := ts
					view.Steps[active].CompletedAt = &t
				}
				view.Steps[active].Duration = durationLabel(view.Steps[active].StartedAt, view.Steps[active].CompletedAt)
				active = -1
				completedAny = true
				continue
			}
			failedPrefix := "--- Step: " + name + ": FAILED (exit "
			if strings.HasPrefix(message, failedPrefix) && strings.HasSuffix(message, ") ---") {
				exitText := strings.TrimSuffix(strings.TrimPrefix(message, failedPrefix), ") ---")
				exitCode, _ := strconv.Atoi(exitText)
				view.Steps[active].Status = "failed"
				view.Steps[active].ExitCode = exitCode
				if hasTS {
					t := ts
					view.Steps[active].CompletedAt = &t
				}
				view.Steps[active].Duration = durationLabel(view.Steps[active].StartedAt, view.Steps[active].CompletedAt)
				active = -1
				completedAny = true
				pipelineStopped = true
				continue
			}
		}

		if active == -1 && next < len(view.Steps) {
			name := view.Steps[next].Name
			if message == "--- Step: "+name+" ---" {
				active = next
				next++
				startedAny = true
				view.Steps[active].Status = "running"
				if hasTS {
					t := ts
					view.Steps[active].StartedAt = &t
				}
				continue
			}
		}

		// Fallback for historical logs when the exact config cannot be loaded.
		if cfg == nil {
			if active >= 0 {
				if match := fallbackStepSuccessRe.FindStringSubmatch(message); match != nil && match[1] == view.Steps[active].Name {
					view.Steps[active].Status = "success"
					if hasTS {
						t := ts
						view.Steps[active].CompletedAt = &t
					}
					view.Steps[active].Duration = durationLabel(view.Steps[active].StartedAt, view.Steps[active].CompletedAt)
					active = -1
					completedAny = true
					continue
				}
				if match := fallbackStepFailedRe.FindStringSubmatch(message); match != nil && match[1] == view.Steps[active].Name {
					view.Steps[active].Status = "failed"
					view.Steps[active].ExitCode, _ = strconv.Atoi(match[2])
					if hasTS {
						t := ts
						view.Steps[active].CompletedAt = &t
					}
					view.Steps[active].Duration = durationLabel(view.Steps[active].StartedAt, view.Steps[active].CompletedAt)
					active = -1
					completedAny = true
					pipelineStopped = true
					continue
				}
			}
			if active == -1 {
				if match := fallbackStepStartRe.FindStringSubmatch(message); match != nil {
					view.Steps = append(view.Steps, CILogSection{Name: match[1], Kind: "step", Status: "running"})
					stepOut = append(stepOut, strings.Builder{})
					active = len(view.Steps) - 1
					startedAny = true
					if hasTS {
						t := ts
						view.Steps[active].StartedAt = &t
					}
					continue
				}
			}
		}

		switch {
		case active >= 0:
			appendLogOutput(&stepOut[active], rawLine)
		case !startedAny:
			appendLogOutput(&setupOut, rawLine)
		case pipelineStopped || (next >= len(view.Steps) && (completedAny || len(view.Steps) == 0)):
			appendLogOutput(&finalizeOut, rawLine)
		default:
			appendLogOutput(&setupOut, rawLine)
		}
	}

	view.Setup.Output = setupOut.String()
	view.Finalize.Output = finalizeOut.String()
	for i := range view.Steps {
		if i < len(stepOut) {
			view.Steps[i].Output = stepOut[i].String()
		}
	}

	terminal := run != nil && isTerminalCIStatus(run.Status)
	if startedAny {
		view.Setup.Status = "success"
	} else if run != nil {
		switch run.Status {
		case "running":
			view.Setup.Status = "running"
		case "pending":
			view.Setup.Status = "pending"
		default:
			view.Setup.Status = run.Status
		}
	}
	if active >= 0 && terminal {
		switch run.Status {
		case "cancelled":
			view.Steps[active].Status = "cancelled"
		case "failed":
			view.Steps[active].Status = "failed"
		default:
			view.Steps[active].Status = run.Status
		}
	}
	if run != nil {
		finalizeStarted := pipelineStopped || strings.TrimSpace(view.Finalize.Output) != "" || (startedAny && next >= len(view.Steps) && active == -1)
		if terminal && finalizeStarted {
			view.Finalize.Status = run.Status
		} else if !terminal && finalizeStarted {
			view.Finalize.Status = "running"
		}
	}

	for i := range view.Steps {
		view.Steps[i].Open = view.Steps[i].Status == "failed" || view.Steps[i].Status == "running" || view.Steps[i].Status == "cancelled"
	}
	view.Setup.Open = view.Setup.Status == "failed" || view.Setup.Status == "running" || view.Setup.Status == "cancelled" || (len(view.Steps) == 0 && strings.TrimSpace(view.Setup.Output) != "")
	view.Finalize.Open = view.Finalize.Status == "failed" || view.Finalize.Status == "running" || view.Finalize.Status == "cancelled"
	return view
}

func isTerminalCIStatus(status string) bool {
	switch status {
	case "success", "failed", "skipped", "cancelled":
		return true
	default:
		return false
	}
}

func readCILog(path string) (string, int64) {
	if path == "" {
		return "", 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	consume := completeCILogPrefixLen(data)
	data = data[:consume]
	return stripANSI(data), int64(consume)
}

func buildArtifactTree(files []artifactFile) []*ArtifactNode {
	root := &ArtifactNode{IsDir: true}
	for _, file := range files {
		parts := strings.Split(file.Path, "/")
		node := root
		for i, part := range parts {
			last := i == len(parts)-1
			var child *ArtifactNode
			for _, existing := range node.Children {
				if existing.Name == part && existing.IsDir == !last {
					child = existing
					break
				}
			}
			if child == nil {
				child = &ArtifactNode{Name: part, IsDir: !last}
				if node.Path == "" {
					child.Path = part
				} else {
					child.Path = node.Path + "/" + part
				}
				node.Children = append(node.Children, child)
			}
			if last {
				child.Size = file.Size
				child.Previewable = file.Previewable
			}
			node = child
		}
	}
	var sortNodes func([]*ArtifactNode)
	sortNodes = func(nodes []*ArtifactNode) {
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].IsDir != nodes[j].IsDir {
				return nodes[i].IsDir
			}
			return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
		})
		for _, node := range nodes {
			sortNodes(node.Children)
		}
	}
	sortNodes(root.Children)
	return root.Children
}

func listArtifactFiles(root string) []artifactFile {
	var files []artifactFile
	_ = filepath.WalkDir(root, func(current string, entry os.DirEntry, err error) error {
		if err != nil || current == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return nil
		}
		files = append(files, artifactFile{
			Path:        filepath.ToSlash(rel),
			Size:        info.Size(),
			Previewable: artifactLooksPreviewable(current, info.Size()),
		})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func artifactLooksPreviewable(path string, size int64) bool {
	if size < 0 || size > maxArtifactPreviewBytes {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	buf := make([]byte, 4096)
	n, _ := file.Read(buf)
	buf = buf[:n]
	if bytes.IndexByte(buf, 0) >= 0 || !validUTF8Sample(buf, size > int64(n)) {
		return false
	}
	contentType := http.DetectContentType(buf)
	return strings.HasPrefix(contentType, "text/") || contentType == "application/json" || contentType == "application/xml"
}

func validUTF8Sample(sample []byte, truncated bool) bool {
	if utf8.Valid(sample) {
		return true
	}
	if !truncated {
		return false
	}
	for trim := 1; trim < utf8.UTFMax && trim <= len(sample); trim++ {
		prefix := sample[:len(sample)-trim]
		suffix := sample[len(sample)-trim:]
		if utf8.Valid(prefix) && len(suffix) > 0 && utf8.RuneStart(suffix[0]) && !utf8.FullRune(suffix) {
			return true
		}
	}
	return false
}

func decorateArtifactTreeURLs(nodes []*ArtifactNode, owner, repo, runID string) {
	for _, node := range nodes {
		if node.IsDir {
			decorateArtifactTreeURLs(node.Children, owner, repo, runID)
			continue
		}
		node.DownloadURL = fmt.Sprintf("/api/repos/%s/%s/artifacts/run/%s/%s", owner, repo, runID, escapePath(node.Path))
		if node.Previewable {
			node.PreviewURL = fmt.Sprintf("/%s/%s/ci/%s/artifacts/preview/%s", owner, repo, runID, escapePath(node.Path))
		}
	}
}

func artifactTreeSize(nodes []*ArtifactNode) int64 {
	var size int64
	for _, node := range nodes {
		if node.IsDir {
			size += artifactTreeSize(node.Children)
		} else {
			size += node.Size
		}
	}
	return size
}
