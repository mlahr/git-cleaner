package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
)

var defaultProtected = []string{"main", "master", "develop", "dev", "development", "staging", "release/*"}
var defaultOldDays = []int{30, 60, 90}

type Config struct {
	Protected []string `yaml:"protected"`
	OldDays   []int    `yaml:"old_days"`
}

type Options struct {
	ConfigPath string
	Protected  []string
	OldDays    []int
	DryRun     bool
	NoFetch    bool
	ListOnly   bool
	InitConfig bool
}

type BranchKind string

const (
	LocalBranch  BranchKind = "local"
	RemoteBranch BranchKind = "remote"
)

type Branch struct {
	Kind          BranchKind
	Name          string
	ShortName     string
	Remote        string
	Hash          string
	LastCommit    time.Time
	AuthorName    string
	AuthorEmail   string
	Upstream      string
	UpstreamTrack string
	Protected     bool
	MergedInto    []string
	OldBucket     int
	Abandoned     string
}

func (b Branch) Key() string {
	return string(b.Kind) + ":" + b.Name
}

func (b Branch) DeleteCommand(force bool) []string {
	if b.Kind == LocalBranch {
		mode := "-d"
		if force {
			mode = "-D"
		}
		return []string{"git", "branch", mode, b.Name}
	}
	return []string{"git", "push", b.Remote, "--delete", b.ShortName}
}

type Category struct {
	Title    string
	Details  string
	Branches []Branch
}

type ScanResult struct {
	Categories []Category
	Branches   []Branch
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		exitErr(err)
	}
	if opts.InitConfig {
		if err := writeDefaultConfig(opts.ConfigPath); err != nil {
			exitErr(err)
		}
		fmt.Printf("Created %s\n", opts.ConfigPath)
		return
	}
	ctx := context.Background()
	if err := ensureGitRepo(ctx); err != nil {
		exitErr(err)
	}
	if !opts.NoFetch {
		_ = runGit(ctx, "fetch", "--all", "--prune")
	}
	result, err := scan(ctx, opts)
	if err != nil {
		exitErr(err)
	}
	if len(result.Branches) == 0 {
		fmt.Println("No branches found.")
		return
	}
	if opts.ListOnly {
		printScan(result)
		return
	}
	m := newModel(result, opts.DryRun)
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		exitErr(err)
	}
	if model, ok := final.(model); ok && model.deleteRequested {
		if err := confirmAndDelete(ctx, model.selectedBranches(), opts.DryRun); err != nil {
			exitErr(err)
		}
	}
}

func parseOptions(args []string) (Options, error) {
	var protectedCSV, oldDaysCSV string
	opts := Options{}
	fs := flag.NewFlagSet("git-cleaner", flag.ContinueOnError)
	fs.StringVar(&opts.ConfigPath, "config", ".git-cleaner.yml", "local config path")
	fs.StringVar(&protectedCSV, "protected", "", "comma-separated protected branch patterns")
	fs.StringVar(&oldDaysCSV, "old-days", "", "comma-separated old branch thresholds in days")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "print deletion commands without running them")
	fs.BoolVar(&opts.NoFetch, "no-fetch", false, "skip git fetch --all --prune before scanning")
	fs.BoolVar(&opts.ListOnly, "list", false, "print grouped branch candidates without opening the TUI")
	fs.BoolVar(&opts.InitConfig, "init-config", false, "create a local config file with all defaults")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if opts.InitConfig {
		return opts, nil
	}
	cfg := Config{Protected: append([]string{}, defaultProtected...), OldDays: append([]int{}, defaultOldDays...)}
	if loaded, err := loadConfig(opts.ConfigPath); err == nil {
		if len(loaded.Protected) > 0 {
			cfg.Protected = loaded.Protected
		}
		if len(loaded.OldDays) > 0 {
			cfg.OldDays = loaded.OldDays
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return opts, err
	}
	if protectedCSV != "" {
		cfg.Protected = splitCSV(protectedCSV)
	}
	if oldDaysCSV != "" {
		days, err := parseInts(oldDaysCSV)
		if err != nil {
			return opts, err
		}
		cfg.OldDays = days
	}
	opts.Protected = uniqueStrings(cfg.Protected)
	opts.OldDays = uniqueInts(cfg.OldDays)
	sort.Ints(opts.OldDays)
	return opts, nil
}

func loadConfig(file string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(file)
	if err != nil {
		return cfg, err
	}
	return cfg, yaml.Unmarshal(data, &cfg)
}

func writeDefaultConfig(file string) error {
	if _, err := os.Stat(file); err == nil {
		return fmt.Errorf("%s already exists", file)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cfg := Config{
		Protected: append([]string{}, defaultProtected...),
		OldDays:   append([]int{}, defaultOldDays...),
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0644)
}

func scan(ctx context.Context, opts Options) (ScanResult, error) {
	locals, err := listBranches(ctx, LocalBranch)
	if err != nil {
		return ScanResult{}, err
	}
	remotes, err := listBranches(ctx, RemoteBranch)
	if err != nil {
		return ScanResult{}, err
	}
	all := append(locals, remotes...)
	localNames := map[string]bool{}
	remoteNames := map[string]bool{}
	for i := range all {
		all[i].Protected = isProtected(all[i], opts.Protected)
		if all[i].Kind == LocalBranch {
			localNames[all[i].ShortName] = true
		} else {
			remoteNames[all[i].ShortName] = true
		}
	}
	merged, err := mergedByProtected(ctx, all)
	if err != nil {
		return ScanResult{}, err
	}
	now := time.Now()
	for i := range all {
		all[i].MergedInto = merged[all[i].Key()]
		all[i].OldBucket = oldBucket(now, all[i].LastCommit, opts.OldDays)
		if all[i].Kind == LocalBranch && strings.Contains(all[i].UpstreamTrack, "gone") {
			all[i].Abandoned = "upstream gone"
		}
		if all[i].Kind == RemoteBranch && !localNames[all[i].ShortName] {
			all[i].Abandoned = "remote-only"
		}
		_ = remoteNames
	}
	return categorize(all), nil
}

func listBranches(ctx context.Context, kind BranchKind) ([]Branch, error) {
	ref := "refs/heads"
	if kind == RemoteBranch {
		ref = "refs/remotes"
	}
	format := "%(refname)%00%(refname:short)%00%(objectname)%00%(committerdate:iso8601)%00%(authorname)%00%(authoremail)%00%(upstream:short)%00%(upstream:track)"
	out, err := gitOutput(ctx, "for-each-ref", ref, "--format", format)
	if err != nil {
		return nil, err
	}
	var branches []Branch
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		for len(parts) < 8 {
			parts = append(parts, "")
		}
		fullRef := parts[0]
		name := parts[1]
		if kind == RemoteBranch && (strings.HasSuffix(fullRef, "/HEAD") || !strings.Contains(name, "/")) {
			continue
		}
		t, _ := time.Parse("2006-01-02 15:04:05 -0700", parts[3])
		b := Branch{
			Kind:          kind,
			Name:          name,
			ShortName:     name,
			Hash:          parts[2],
			LastCommit:    t,
			AuthorName:    parts[4],
			AuthorEmail:   strings.Trim(parts[5], "<>"),
			Upstream:      parts[6],
			UpstreamTrack: parts[7],
		}
		if kind == RemoteBranch {
			remote, short, ok := strings.Cut(name, "/")
			if ok && short != "" {
				b.Remote = remote
				b.ShortName = short
			} else {
				continue
			}
		}
		branches = append(branches, b)
	}
	return branches, nil
}

func mergedByProtected(ctx context.Context, branches []Branch) (map[string][]string, error) {
	keysByName := map[string]string{}
	var protected []Branch
	for _, b := range branches {
		keysByName[string(b.Kind)+":"+b.Name] = b.Key()
		if b.Protected {
			protected = append(protected, b)
		}
	}
	result := map[string][]string{}
	for _, base := range protected {
		flag := "--merged"
		out, err := gitOutput(ctx, "branch", branchListFlag(base.Kind), flag, base.Name, "--format", "%(refname:short)")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "* "))
			if name == "" || name == base.Name {
				continue
			}
			key := keysByName[string(base.Kind)+":"+name]
			if key != "" {
				result[key] = append(result[key], base.Name)
			}
		}
	}
	for k := range result {
		result[k] = uniqueStrings(result[k])
		sort.Strings(result[k])
	}
	return result, nil
}

func branchListFlag(kind BranchKind) string {
	if kind == RemoteBranch {
		return "-r"
	}
	return "--list"
}

func categorize(branches []Branch) ScanResult {
	var merged, old, abandoned, stale, risky []Branch
	for _, b := range branches {
		if b.Protected {
			continue
		}
		switch {
		case len(b.MergedInto) > 0:
			merged = append(merged, b)
		case b.Kind == LocalBranch && strings.Contains(b.UpstreamTrack, "gone"):
			stale = append(stale, b)
		case b.Abandoned != "":
			abandoned = append(abandoned, b)
		case b.OldBucket > 0:
			old = append(old, b)
		default:
			risky = append(risky, b)
		}
	}
	sortBranches := func(bs []Branch) {
		sort.Slice(bs, func(i, j int) bool {
			if bs[i].Kind != bs[j].Kind {
				return bs[i].Kind < bs[j].Kind
			}
			return bs[i].Name < bs[j].Name
		})
	}
	for _, group := range []*[]Branch{&merged, &old, &abandoned, &stale, &risky} {
		sortBranches(*group)
	}
	cats := []Category{
		{Title: "Merged into protected", Details: "safe candidates found through git branch --merged", Branches: merged},
		{Title: "Old", Details: "last commit is older than configured thresholds", Branches: old},
		{Title: "Abandoned", Details: "upstream gone or remote-only", Branches: abandoned},
		{Title: "Stale local tracking branches", Details: "local branches tracking a deleted upstream", Branches: stale},
		{Title: "Unmerged / risky", Details: "not merged into any protected branch", Branches: risky},
	}
	return ScanResult{Categories: cats, Branches: branches}
}

func isProtected(b Branch, patterns []string) bool {
	candidates := []string{b.ShortName, b.Name}
	for _, pattern := range patterns {
		for _, candidate := range candidates {
			if pattern == candidate {
				return true
			}
			if ok, _ := path.Match(pattern, candidate); ok {
				return true
			}
		}
	}
	return false
}

func oldBucket(now, last time.Time, thresholds []int) int {
	if last.IsZero() || len(thresholds) == 0 {
		return 0
	}
	age := int(now.Sub(last).Hours() / 24)
	bucket := 0
	for _, days := range thresholds {
		if age >= days {
			bucket = days
		}
	}
	return bucket
}

func ensureGitRepo(ctx context.Context) error {
	if _, err := gitOutput(ctx, "rev-parse", "--git-dir"); err != nil {
		return errors.New("current directory is not a Git repository")
	}
	return nil
}

func gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}

func runGit(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseInts(s string) ([]int, error) {
	var out []int
	for _, part := range splitCSV(s) {
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid day threshold %q", part)
		}
		out = append(out, n)
	}
	return out, nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func uniqueInts(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range in {
		if v > 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func confirmAndDelete(ctx context.Context, branches []Branch, dryRun bool) error {
	if len(branches) == 0 {
		fmt.Println("No branches selected.")
		return nil
	}
	fmt.Println("Commands:")
	for _, b := range branches {
		fmt.Println("  " + shellCommand(b.DeleteCommand(false)))
	}
	if dryRun {
		fmt.Println("Dry run: no commands executed.")
		return nil
	}
	if !promptYesNo("Delete selected branches?") {
		fmt.Println("Cancelled.")
		return nil
	}
	for _, b := range branches {
		cmd := b.DeleteCommand(false)
		fmt.Println(shellCommand(cmd))
		err := runGit(ctx, cmd[1:]...)
		if err == nil || b.Kind != LocalBranch {
			continue
		}
		if promptYesNo(fmt.Sprintf("Soft delete failed for %s. Force delete?", b.Name)) {
			force := b.DeleteCommand(true)
			fmt.Println(shellCommand(force))
			if forceErr := runGit(ctx, force[1:]...); forceErr != nil {
				return forceErr
			}
		}
	}
	return nil
}

func printScan(result ScanResult) {
	for _, cat := range result.Categories {
		fmt.Printf("%s (%d)\n", cat.Title, len(cat.Branches))
		for _, branch := range cat.Branches {
			fmt.Println("  " + formatBranch(branch, nil, 120))
		}
		fmt.Println()
	}
}

func promptYesNo(question string) bool {
	fmt.Printf("%s [y/N] ", question)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func shellCommand(parts []string) string {
	escaped := make([]string, len(parts))
	for i, part := range parts {
		if strings.ContainsAny(part, " \t'\"") {
			escaped[i] = strconv.Quote(part)
		} else {
			escaped[i] = part
		}
	}
	return strings.Join(escaped, " ")
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

type row struct {
	Category int
	Branch   *Branch
	Header   string
}

type model struct {
	categories      []Category
	rows            []row
	cursor          int
	selected        map[string]Branch
	preview         bool
	dryRun          bool
	deleteRequested bool
	width           int
}

func newModel(result ScanResult, dryRun bool) model {
	m := model{
		categories: result.Categories,
		selected:   map[string]Branch{},
		dryRun:     dryRun,
	}
	m.rebuildRows()
	for i, row := range m.rows {
		if row.Branch != nil {
			m.cursor = i
			break
		}
	}
	return m
}

func (m *model) rebuildRows() {
	m.rows = nil
	for i, cat := range m.categories {
		m.rows = append(m.rows, row{Category: i, Header: fmt.Sprintf("%s (%d)", cat.Title, len(cat.Branches))})
		for j := range cat.Branches {
			b := cat.Branches[j]
			m.rows = append(m.rows, row{Category: i, Branch: &b})
		}
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case " ":
			m.toggleCurrent()
		case "a":
			m.setCategorySelection(true)
		case "u":
			m.setCategorySelection(false)
		case "p":
			m.preview = !m.preview
		case "d":
			m.deleteRequested = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *model) move(delta int) {
	if len(m.rows) == 0 {
		return
	}
	for next := m.cursor + delta; next >= 0 && next < len(m.rows); next += delta {
		m.cursor = next
		if m.rows[next].Branch != nil {
			return
		}
	}
}

func (m *model) toggleCurrent() {
	if len(m.rows) == 0 || m.rows[m.cursor].Branch == nil {
		return
	}
	b := *m.rows[m.cursor].Branch
	if _, ok := m.selected[b.Key()]; ok {
		delete(m.selected, b.Key())
	} else {
		m.selected[b.Key()] = b
	}
}

func (m *model) setCategorySelection(selected bool) {
	if len(m.rows) == 0 {
		return
	}
	cat := m.rows[m.cursor].Category
	for _, b := range m.categories[cat].Branches {
		if selected {
			m.selected[b.Key()] = b
		} else {
			delete(m.selected, b.Key())
		}
	}
}

func (m model) selectedBranches() []Branch {
	var out []Branch
	for _, b := range m.selected {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Key() < out[j].Key()
	})
	return out
}

func (m model) View() string {
	title := titleStyle.Render("git-cleaner")
	mode := ""
	if m.dryRun {
		mode = " dry-run"
	}
	help := mutedStyle.Render("space select  a select category  u unselect category  p preview  d delete  q quit" + mode)
	var b strings.Builder
	b.WriteString(title + "\n")
	b.WriteString(help + "\n\n")
	for i, r := range m.rows {
		if r.Header != "" {
			b.WriteString(headerStyle.Render(r.Header))
			if r.Category < len(m.categories) {
				b.WriteString("\n  " + mutedStyle.Render(m.categories[r.Category].Details))
			}
			b.WriteString("\n")
			if len(m.categories[r.Category].Branches) > 0 {
				b.WriteString(mutedStyle.Render("  sel  type    branch                              status                                      updated     author") + "\n")
			}
			continue
		}
		line := formatBranch(*r.Branch, m.selected, m.width)
		if i == m.cursor {
			line = cursorStyle.Render("> " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	if m.preview {
		b.WriteString("\n" + headerStyle.Render("Command preview") + "\n")
		selected := m.selectedBranches()
		if len(selected) == 0 {
			b.WriteString(mutedStyle.Render("No branches selected.") + "\n")
		}
		for _, branch := range selected {
			b.WriteString("  " + shellCommand(branch.DeleteCommand(false)) + "\n")
		}
	}
	return b.String()
}

func formatBranch(branch Branch, selected map[string]Branch, terminalWidth int) string {
	box := "[ ]"
	if _, ok := selected[branch.Key()]; ok {
		box = "[x]"
	}
	statusWidth := 29
	if terminalWidth > 0 {
		statusWidth = terminalWidth - 3 - 3 - 2 - 6 - 2 - 34 - 2 - 10 - 2 - 16
		if statusWidth < 29 {
			statusWidth = 29
		}
	}
	updated := "-"
	if !branch.LastCommit.IsZero() {
		updated = branch.LastCommit.Format("2006-01-02")
	}
	return fmt.Sprintf(
		"%-3s  %-6s  %-34s  %-*s  %-10s  %s",
		box,
		branch.Kind,
		truncate(branch.Name, 34),
		statusWidth,
		truncate(branchStatus(branch), statusWidth),
		updated,
		truncate(branch.AuthorName, 16),
	)
}

func branchStatus(branch Branch) string {
	var parts []string
	if len(branch.MergedInto) > 0 {
		parts = append(parts, "merged -> "+strings.Join(branch.MergedInto, ","))
	}
	if branch.OldBucket > 0 {
		parts = append(parts, fmt.Sprintf("old %dd+", branch.OldBucket))
	}
	if branch.Abandoned != "" {
		parts = append(parts, branch.Abandoned)
	}
	if len(parts) == 0 {
		return "unmerged"
	}
	return strings.Join(parts, ", ")
}

func truncate(s string, width int) string {
	s = strings.TrimSpace(s)
	if len(s) <= width {
		return s
	}
	if width <= 3 {
		return s[:width]
	}
	return s[:width-3] + "..."
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)
