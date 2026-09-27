package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
	"github.com/mmrzaf/gitman/internal/worker"
)

// adminEnv is what every admin action works with.
type adminEnv struct {
	cfg    *config.Config
	people *auth.Service
	repos  *reposvc.Service
	ci     *ci.Service
	out    io.Writer
	// dockerBinary is the docker client "worker cleanup" runs.
	dockerBinary string
}

type adminAction struct {
	usage string
	run   func(ctx context.Context, env *adminEnv, args []string) error
}

const ruleSetUsage = "[--push everyone|admins|people] [--people a,b] [--force] [--delete] [--run] [--docker] [--secrets] [--ship] <repo> branch|tag <pattern>"

const defaultPushUsage = "[--push everyone|admins|people] [--people a,b] <name>"

// adminGroups maps "admin <group> <action>" to its implementation.
var adminGroups = map[string]map[string]adminAction{
	"person": {
		"add":            {"[--admin] <username>", adminPersonAdd},
		"list":           {"", adminPersonList},
		"disable":        {"<username>", adminPersonDisable},
		"enable":         {"<username>", adminPersonEnable},
		"role":           {"<username> admin|member", adminPersonRole},
		"reset-password": {"<username>", adminPersonResetPassword},
	},
	"token": {
		"create": {"[--write] [--days N] <username> <name>", adminTokenCreate},
	},
	"run": {
		"cancel": {"<repo> <number>", adminRunCancel},
	},
	"repo": {
		"create":         {"[--description TEXT] [--default-branch NAME] <name>", adminRepoCreate},
		"list":           {"", adminRepoList},
		"delete":         {"<name>", adminRepoDelete},
		"sync":           {"<name>", adminRepoSync},
		"visibility":     {"<name> everyone|restricted", adminRepoVisibility},
		"default-branch": {"<name> <branch>", adminRepoDefaultBranch},
		"default-push":   {defaultPushUsage, adminRepoDefaultPush},
	},
	"rule": {
		"list":   {"<repo>", adminRuleList},
		"set":    {ruleSetUsage, adminRuleSet},
		"delete": {"<repo> branch|tag <pattern>", adminRuleDelete},
	},
	"reader": {
		"add":    {"<repo> <username>", adminReaderAdd},
		"remove": {"<repo> <username>", adminReaderRemove},
		"list":   {"<repo>", adminReaderList},
	},
	"worker": {
		"cleanup": {"", adminWorkerCleanup},
	},
}

func adminUsage() error {
	var b strings.Builder
	b.WriteString("usage: gitman admin <group> <action> [arguments]\n\n")
	b.WriteString("  gitman admin migrate\n")
	groups := make([]string, 0, len(adminGroups))
	for g := range adminGroups {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		actions := make([]string, 0, len(adminGroups[g]))
		for a := range adminGroups[g] {
			actions = append(actions, a)
		}
		sort.Strings(actions)
		for _, a := range actions {
			fmt.Fprintf(&b, "  %s\n", strings.TrimSpace(fmt.Sprintf("gitman admin %s %s %s", g, a, adminGroups[g][a].usage)))
		}
	}
	b.WriteString("\nFlags go before the positional arguments.")
	return errors.New(b.String())
}

func runAdmin(args []string) error {
	if len(args) == 0 {
		return adminUsage()
	}
	var action adminAction
	var rest []string
	if args[0] == "migrate" {
		if len(args) != 1 {
			return adminUsage()
		}
		// Every admin action runs after the connect-then-Migrate sequence
		// below, so "migrate" needs no action of its own: reaching this
		// point has already applied any pending migration.
		action = adminAction{run: func(context.Context, *adminEnv, []string) error { return nil }}
	} else {
		group, ok := adminGroups[args[0]]
		if !ok || len(args) < 2 {
			return adminUsage()
		}
		if action, ok = group[args[1]]; !ok {
			return adminUsage()
		}
		rest = args[2:]
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()

	database, err := postgres.Connect(ctx, cfg.DatabaseURL, postgres.Options{MaxConns: 2})
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return err
	}
	store := git.NewStore(cfg.ReposPath())
	defer store.Close()

	return action.run(ctx, &adminEnv{
		cfg:    cfg,
		people: auth.NewService(database),
		repos:  reposvc.NewService(database, store, cfg.SecretKey),
		ci:     ci.NewService(database),
		out:    os.Stdout,

		dockerBinary: "docker",
	}, rest)
}

// parseArgs parses flags followed by exactly n positional arguments.
func parseArgs(fs *flag.FlagSet, args []string, n int, usage string) ([]string, error) {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%v\nusage: %s", err, usage)
	}
	if fs.NArg() != n {
		return nil, fmt.Errorf("usage: %s", usage)
	}
	return fs.Args(), nil
}

func personByName(ctx context.Context, env *adminEnv, username string) (*auth.Person, error) {
	p, err := env.people.GetByUsername(ctx, username)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, fmt.Errorf("no person named %q", username)
	}
	return p, err
}

func repoByName(ctx context.Context, env *adminEnv, name string) (*reposvc.Repo, error) {
	r, err := env.repos.GetByName(ctx, name)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, fmt.Errorf("no repository named %q", name)
	}
	return r, err
}

func adminPersonAdd(ctx context.Context, env *adminEnv, args []string) error {
	fs := flag.NewFlagSet("person add", flag.ContinueOnError)
	admin := fs.Bool("admin", false, "")
	pos, err := parseArgs(fs, args, 1, "gitman admin person add [--admin] <username>")
	if err != nil {
		return err
	}
	password, err := auth.GeneratePassword()
	if err != nil {
		return err
	}
	p, err := env.people.Create(ctx, pos[0], password, *admin, "")
	if err != nil {
		if errors.Is(err, postgres.ErrAlreadyExists) {
			return fmt.Errorf("a person named %q already exists", pos[0])
		}
		return err
	}
	role := "member"
	if p.IsAdmin {
		role = "admin"
	}
	fmt.Fprintf(env.out, "Added %s (%s).\nPassword: %s\n", p.Username, role, password)
	return nil
}

func adminPersonList(ctx context.Context, env *adminEnv, args []string) error {
	if _, err := parseArgs(flag.NewFlagSet("person list", flag.ContinueOnError), args, 0, "gitman admin person list"); err != nil {
		return err
	}
	list, err := env.people.List(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(env.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "USERNAME\tROLE\tSTATE\tSINCE")
	for _, p := range list {
		role, state := "member", "active"
		if p.IsAdmin {
			role = "admin"
		}
		if p.Disabled() {
			state = "disabled"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Username, role, state, p.CreatedAt.Format("2006-01-02"))
	}
	return tw.Flush()
}

func adminPersonDisable(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("person disable", flag.ContinueOnError), args, 1, "gitman admin person disable <username>")
	if err != nil {
		return err
	}
	p, err := personByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	if err := env.people.Disable(ctx, p.ID, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Disabled %s. Their sessions and tokens no longer work.\n", p.Username)
	return nil
}

func adminPersonEnable(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("person enable", flag.ContinueOnError), args, 1, "gitman admin person enable <username>")
	if err != nil {
		return err
	}
	p, err := personByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	if err := env.people.Enable(ctx, p.ID, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Enabled %s.\n", p.Username)
	return nil
}

func adminPersonRole(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("person role", flag.ContinueOnError), args, 2, "gitman admin person role <username> admin|member")
	if err != nil {
		return err
	}
	var isAdmin bool
	switch pos[1] {
	case "admin":
		isAdmin = true
	case "member":
	default:
		return fmt.Errorf("role must be admin or member")
	}
	p, err := personByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	if err := env.people.SetAdmin(ctx, p.ID, isAdmin, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "%s is now %s.\n", p.Username, map[bool]string{true: "an admin", false: "a member"}[isAdmin])
	return nil
}

func adminPersonResetPassword(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("person reset-password", flag.ContinueOnError), args, 1, "gitman admin person reset-password <username>")
	if err != nil {
		return err
	}
	p, err := personByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	password, err := auth.GeneratePassword()
	if err != nil {
		return err
	}
	if err := env.people.ResetPassword(ctx, p.ID, password, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Reset the password for %s. Their existing sessions were signed out.\nPassword: %s\n", p.Username, password)
	return nil
}

func adminTokenCreate(ctx context.Context, env *adminEnv, args []string) error {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	write := fs.Bool("write", false, "")
	days := fs.Int("days", 0, "")
	pos, err := parseArgs(fs, args, 2, "gitman admin token create [--write] [--days N] <username> <name>")
	if err != nil {
		return err
	}
	if *days < 0 {
		return fmt.Errorf("--days must not be negative")
	}
	p, err := personByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	scope := auth.ScopeRead
	if *write {
		scope = auth.ScopeWrite
	}
	var ttl *time.Duration
	if *days > 0 {
		d := time.Duration(*days) * 24 * time.Hour
		ttl = &d
	}
	plain, token, err := env.people.CreateToken(ctx, p.ID, pos[1], scope, ttl)
	if err != nil {
		return err
	}
	expiry := "never expires"
	if token.ExpiresAt != nil {
		expiry = "expires " + token.ExpiresAt.Format("2006-01-02")
	}
	fmt.Fprintf(env.out, "Created %s token %q for %s (%s).\nToken: %s\n", scope, token.Name, p.Username, expiry, plain)
	fmt.Fprintf(env.out, "Use it as the password for Git over HTTP, with %s as the username.\n", p.Username)
	return nil
}

func adminRepoCreate(ctx context.Context, env *adminEnv, args []string) error {
	fs := flag.NewFlagSet("repo create", flag.ContinueOnError)
	description := fs.String("description", "", "")
	branch := fs.String("default-branch", "main", "")
	pos, err := parseArgs(fs, args, 1, "gitman admin repo create [--description TEXT] [--default-branch NAME] <name>")
	if err != nil {
		return err
	}
	repo, err := env.repos.Create(ctx, pos[0], *description, *branch, "")
	if err != nil {
		if errors.Is(err, postgres.ErrAlreadyExists) {
			return fmt.Errorf("a repository named %q already exists", pos[0])
		}
		return err
	}
	fmt.Fprintf(env.out, "Created %s.\nClone: %s/%s.git\n", repo.Name, env.cfg.PublicURL, repo.Name)
	return nil
}

func adminRepoList(ctx context.Context, env *adminEnv, args []string) error {
	if _, err := parseArgs(flag.NewFlagSet("repo list", flag.ContinueOnError), args, 0, "gitman admin repo list"); err != nil {
		return err
	}
	list, err := env.repos.List(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(env.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDEFAULT BRANCH\tCREATED\tDESCRIPTION")
	for _, r := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, r.DefaultBranch, r.CreatedAt.Format("2006-01-02"), r.Description)
	}
	return tw.Flush()
}

func adminRepoDelete(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("repo delete", flag.ContinueOnError), args, 1, "gitman admin repo delete <name>")
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	if err := env.repos.Delete(ctx, repo.ID, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Deleted %s, with its history, runs and deployments.\n", repo.Name)
	return nil
}

// adminRepoSync rebuilds a repository's ref index from Git, for a
// repository whose files were restored or changed outside Gitman.
func adminRepoSync(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("repo sync", flag.ContinueOnError), args, 1, "gitman admin repo sync <name>")
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	n, err := env.repos.SyncRefs(ctx, repo)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Synced %d branches and tags of %s.\n", n, repo.Name)
	return nil
}

func adminRepoVisibility(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("repo visibility", flag.ContinueOnError), args, 2, "gitman admin repo visibility <name> everyone|restricted")
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	visibility := reposvc.Visibility(pos[1])
	if err := env.repos.SetVisibility(ctx, repo.ID, visibility, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "%s is now %s.\n", repo.Name, visibility)
	return nil
}

func adminRepoDefaultBranch(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("repo default-branch", flag.ContinueOnError), args, 2, "gitman admin repo default-branch <name> <branch>")
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	if err := env.repos.SetDefaultBranch(ctx, repo, pos[1], ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "%s's default branch is now %s.\n", repo.Name, pos[1])
	return nil
}

func adminRepoDefaultPush(ctx context.Context, env *adminEnv, args []string) error {
	fs := flag.NewFlagSet("repo default-push", flag.ContinueOnError)
	push := fs.String("push", string(reposvc.PushEveryone), "")
	pushPeople := fs.String("people", "", "")
	pos, err := parseArgs(fs, args, 1, "gitman admin repo default-push "+defaultPushUsage)
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	var people []string
	if *pushPeople != "" {
		for _, username := range strings.Split(*pushPeople, ",") {
			p, err := personByName(ctx, env, strings.TrimSpace(username))
			if err != nil {
				return err
			}
			people = append(people, p.ID)
		}
	}
	policy := reposvc.PushPolicy(*push)
	if err := env.repos.SetDefaultPush(ctx, repo.ID, policy, people, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Set %s's default push policy to %s.\n", repo.Name, policy)
	return nil
}

func adminReaderAdd(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("reader add", flag.ContinueOnError), args, 2, "gitman admin reader add <repo> <username>")
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	person, err := personByName(ctx, env, pos[1])
	if err != nil {
		return err
	}
	if err := env.repos.AddReader(ctx, repo.ID, person.ID, person.Username, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "%s can now read %s.\n", person.Username, repo.Name)
	return nil
}

func adminReaderRemove(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("reader remove", flag.ContinueOnError), args, 2, "gitman admin reader remove <repo> <username>")
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	person, err := personByName(ctx, env, pos[1])
	if err != nil {
		return err
	}
	if err := env.repos.RemoveReader(ctx, repo.ID, person.ID, person.Username, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Removed %s's read access to %s.\n", person.Username, repo.Name)
	return nil
}

func adminReaderList(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("reader list", flag.ContinueOnError), args, 1, "gitman admin reader list <repo>")
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	ids, err := env.repos.ListReaders(ctx, repo.ID)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Fprintln(env.out, "No explicit readers.")
		return nil
	}
	for _, personID := range ids {
		person, err := env.people.GetByID(ctx, personID)
		if err != nil {
			return err
		}
		fmt.Fprintln(env.out, person.Username)
	}
	return nil
}

// adminWorkerCleanup fails the runs of any worker that has stopped
// heartbeating, then removes the step containers and workspaces this
// host's workers left behind. A worker does both periodically on its
// own, but a worker killed outright (SIGKILL, OOM) leaves nothing behind
// to run its own cleanup, and nothing else on that host runs it either
// unless another worker process happens to share it. This lets an
// operator, or a cron job, reclaim the disk and containers on a host
// like that without needing a second worker running there: a run is
// only "no longer running" once something has failed it, so failing lost
// runs first is what lets the cleanup that follows find anything to do.
func adminWorkerCleanup(ctx context.Context, env *adminEnv, args []string) error {
	if _, err := parseArgs(flag.NewFlagSet("worker cleanup", flag.ContinueOnError), args, 0, "gitman admin worker cleanup"); err != nil {
		return err
	}
	failed, err := env.ci.FailLostRuns(ctx, ci.WorkerLostAfter)
	if err != nil {
		return err
	}
	docker, err := newDocker(ctx, env.ci, env.dockerBinary)
	if err != nil {
		return err
	}
	containers, workspaces, err := worker.RemoveLeftovers(ctx, docker, env.cfg.WorkspacesPath(), env.ci.RunningRunIDs)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Failed %d runs, removed %d containers and %d workspaces.\n", failed, containers, workspaces)
	return nil
}

func adminRuleList(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("rule list", flag.ContinueOnError), args, 1, "gitman admin rule list <repo>")
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	rules, err := env.repos.ListRules(ctx, repo.ID)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		fmt.Fprintln(env.out, "No rules: every branch and tag is unprotected, and no push runs a pipeline.")
		return nil
	}
	everyone, err := env.people.List(ctx)
	if err != nil {
		return err
	}
	usernames := make(map[string]string, len(everyone))
	for _, p := range everyone {
		usernames[p.ID] = p.Username
	}
	tw := tabwriter.NewWriter(env.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tPATTERN\tPUSH\tALLOWS")
	for _, r := range rules {
		push := string(r.PushPolicy)
		if r.PushPolicy == reposvc.PushPeople {
			listed := make([]string, len(r.PushPeople))
			for i, personID := range r.PushPeople {
				listed[i] = usernames[personID]
			}
			push = "people: " + strings.Join(listed, ", ")
		}
		var allows []string
		for _, flag := range []struct {
			on   bool
			name string
		}{{r.AllowForce, "force"}, {r.AllowDelete, "delete"}, {r.RunOnPush, "run"}, {r.AllowDocker, "docker"}, {r.AllowSecrets, "secrets"}, {r.AllowShip, "ship"}} {
			if flag.on {
				allows = append(allows, flag.name)
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Kind, r.Pattern, push, strings.Join(allows, " "))
	}
	return tw.Flush()
}

func parseKind(s string) (git.Kind, error) {
	switch git.Kind(s) {
	case git.KindBranch, git.KindTag:
		return git.Kind(s), nil
	}
	return "", fmt.Errorf("kind must be branch or tag, got %q", s)
}

func adminRuleSet(ctx context.Context, env *adminEnv, args []string) error {
	fs := flag.NewFlagSet("rule set", flag.ContinueOnError)
	push := fs.String("push", string(reposvc.PushEveryone), "")
	pushPeople := fs.String("people", "", "")
	force := fs.Bool("force", false, "")
	del := fs.Bool("delete", false, "")
	runOnPush := fs.Bool("run", false, "")
	docker := fs.Bool("docker", false, "")
	secrets := fs.Bool("secrets", false, "")
	ship := fs.Bool("ship", false, "")
	pos, err := parseArgs(fs, args, 3, "gitman admin rule set "+ruleSetUsage)
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	kind, err := parseKind(pos[1])
	if err != nil {
		return err
	}
	rule := reposvc.Rule{
		Kind: kind, Pattern: pos[2], PushPolicy: reposvc.PushPolicy(*push),
		AllowForce: *force, AllowDelete: *del, RunOnPush: *runOnPush,
		AllowDocker: *docker, AllowSecrets: *secrets, AllowShip: *ship,
	}
	if *pushPeople != "" {
		for _, username := range strings.Split(*pushPeople, ",") {
			p, err := personByName(ctx, env, strings.TrimSpace(username))
			if err != nil {
				return err
			}
			rule.PushPeople = append(rule.PushPeople, p.ID)
		}
	}
	if err := env.repos.SaveRule(ctx, repo.ID, rule, ""); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Saved the rule for %s %q on %s.\n", kind, rule.Pattern, repo.Name)
	return nil
}

func adminRuleDelete(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("rule delete", flag.ContinueOnError), args, 3, "gitman admin rule delete <repo> branch|tag <pattern>")
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	kind, err := parseKind(pos[1])
	if err != nil {
		return err
	}
	if err := env.repos.DeleteRule(ctx, repo.ID, kind, pos[2], ""); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			return fmt.Errorf("%s has no rule for %s %q", repo.Name, kind, pos[2])
		}
		return err
	}
	fmt.Fprintf(env.out, "Deleted the rule for %s %q on %s.\n", kind, pos[2], repo.Name)
	return nil
}

func adminRunCancel(ctx context.Context, env *adminEnv, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("run cancel", flag.ContinueOnError), args, 2, "gitman admin run cancel <repo> <number>")
	if err != nil {
		return err
	}
	repo, err := repoByName(ctx, env, pos[0])
	if err != nil {
		return err
	}
	number, err := strconv.ParseInt(strings.TrimPrefix(pos[1], "#"), 10, 64)
	if err != nil || number < 1 {
		return fmt.Errorf("run number must be a positive integer, got %q", pos[1])
	}
	switch err := env.ci.Cancel(ctx, repo.ID, number, ""); {
	case errors.Is(err, postgres.ErrNotFound):
		return fmt.Errorf("%s has no run #%d", repo.Name, number)
	case errors.Is(err, ci.ErrRunFinished):
		return fmt.Errorf("run #%d of %s has already finished", number, repo.Name)
	case err != nil:
		return err
	}
	fmt.Fprintf(env.out, "Cancelled run #%d of %s. A running run stops at its worker's next check, within seconds.\n", number, repo.Name)
	return nil
}
