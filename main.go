package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/hassansin/gh-release/internal/github"
	"github.com/manifoldco/promptui"
	"github.com/pkg/errors"
)

const (
	defaultEditor      = "vim"
	tagPrefix          = "v"
	releaseMsgFilename = "RELEASE_EDITMSG"
	lineReset          = "\033[2K\r"
)

var (
	reRepo    = regexp.MustCompile(`[/:]([a-z-]+)/([a-z-]+)`)
	reSection = regexp.MustCompile(`^\[(.*)\]`)
	reVal     = regexp.MustCompile(`^\s+(\w+)\s*=\s*(.*)$`)
	reComment = regexp.MustCompile(`^\s*#`)

	bold          = promptui.Styler(promptui.FGBold)
	cyan          = promptui.Styler(promptui.FGCyan, promptui.FGBold)
	white         = promptui.Styler(promptui.FGWhite, promptui.FGBold)
	green         = promptui.Styler(promptui.FGGreen, promptui.FGBold)
	faint         = promptui.Styler(promptui.FGFaint)
	startBoldCyan = strings.Replace(cyan(""), promptui.ResetCode, "", -1)
)

func main() {
	owner, name, head := mustGetCurrentRepo()
	editorCmd := mustFindEditor()
	token := mustGetToken()
	client := github.New(owner, name, token)
	if err := do(editorCmd, client, head); err != nil {
		abort(err)
	}
}

//abort exits with non-zero status, prints pretty error message instead of panic
func abort(err error) {
	fmt.Printf("%v %v\n", promptui.IconBad, err)
	os.Exit(1)
}

func do(editorCmd []string, client github.GithubClient, head string) error {
	done := make(chan struct{})
	go showProgress("getting current release", done)

	repo, err := client.GetRepository()
	if err != nil {
		return err
	}
	if len(repo.Branches) == 0 {
		return errors.New("Couldn't find any remote branch")
	}
	if repo.LatestRelease == nil {
		return errors.New("no previous release") //@TODO
	}

	sortBranches(repo.Branches, head)

	done <- struct{}{}
	/*
		fmt.Printf("%v %v %v\n\t%v, \n\t%v\n",
			green(promptui.IconGood),
			white("Current release:"),
			cyan(repo.LatestRelease.Name),
			faint("Tag: "+repo.LatestRelease.Tag.Name),
			faint("Commit: "+repo.LatestRelease.Tag.Target.ShortID+" "+repo.LatestRelease.Tag.Target.Message))
	*/

	target, err := selectTarget(repo.Branches, repo.LatestRelease)
	if err != nil || target == nil {
		return err
	}
	lastRel := repo.LatestRelease.Tag.Name
	version, err := nextVersion(lastRel)
	if err != nil {
		return err
	}
	var commits []*github.Commit
	errCh := make(chan error)

	go func() {
		commits, err = client.CompareCommits(repo.LatestRelease.Tag.Target, target.Head)
		errCh <- err
	}()

	tagName, err := promptTag(version, lastRel)
	if err != nil || tagName == "" {
		return err
	}

	if err := <-errCh; err != nil {
		return err
	}

	if len(commits) == 0 {
		return errors.Errorf("%v is already released", cyan(target.Name))
	}

	ed := newEditor(editorCmd)

	title, body, err := ed.edit(releaseNotes(tagName, commits))
	if err != nil {
		return err
	}
	go showProgress("creating release", done)
	release, err := client.CreateRelease(&github.Release{
		Name: title,
		Tag: github.Tag{
			Name:   tagName,
			Target: target.Head,
		},
		Description: body,
	})
	done <- struct{}{}
	if err != nil {
		return err
	}

	fmt.Printf("%v New release(%v) created:\n  %v\n", green(promptui.IconGood), cyan(release.Tag.Name), release.HTMLURL)
	return nil
}

func nextVersion(tag string) (string, error) {
	v, err := semver.Make(strings.TrimPrefix(tag, tagPrefix))
	if err != nil {
		return "", err
	}
	v.Patch++
	version := v.String()
	if strings.HasPrefix(tag, tagPrefix) {
		version = tagPrefix + version
	}
	return version, nil
}

//sort branches by branch name length, keeping head at the top
func sortBranches(branches []*github.Branch, head string) {
	sort.Slice(branches, func(i, j int) bool {
		li := len(branches[i].Name)
		lj := len(branches[j].Name)
		if branches[i].Name == head {
			li = 0
		}
		if branches[j].Name == head {
			lj = 0
		}
		return li < lj
	})
}

func promptTag(tag, lastRel string) (string, error) {
	templates := &promptui.PromptTemplates{
		Success: fmt.Sprintf(`{{ "%s" | green | bold }} {{"%s" | bold}} %v`, promptui.IconGood, "Tag:", startBoldCyan),
	}
	prompt := promptui.Prompt{
		Label:     fmt.Sprintf("Enter release tag %s", faint("(last release: "+cyan(lastRel)+")")),
		AllowEdit: true,
		Default:   tag,
		Templates: templates,
	}
	tagName, err := prompt.Run()
	fmt.Print(promptui.ResetCode)
	if err == promptui.ErrInterrupt || err == promptui.ErrEOF {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return tagName, nil
}
func selectTarget(branches []*github.Branch, rel *github.Release) (*github.Branch, error) {
	type option struct {
		Name   string
		Count  int
		Status string
	}
	var options []option

	for _, b := range branches {
		count := b.CommitCount - rel.Tag.CommitCount
		status := "ahead"
		if count < 0 {
			count = -1 * count
			status = "behind"
		}
		//options[i] = fmt.Sprintf("%s (%v commits %v)", b.Name, count, status)
		options = append(options, option{
			Name:   b.Name,
			Status: status,
			Count:  count,
		})
	}

	templates := &promptui.SelectTemplates{
		Label:    fmt.Sprintf("%s {{.Name}} {{.Count}} {{.Status}}", promptui.IconInitial),
		Active:   fmt.Sprintf(`%s {{printf "%%v (%%v commits %%v)" (.Name|bold) (.Count | yellow)  (.Status | red) }}`, "▣"),
		Inactive: fmt.Sprintf(`%s {{printf "%%v (%%v commits %%v)" .Name (.Count | yellow) (.Status | red)}}`, "▢"),
		Selected: fmt.Sprintf(`{{ "%s" | green | bold }} {{"%s" | bold}} {{.Name| cyan | bold }}`, promptui.IconGood, "Target:"),
	}

	prompt := promptui.Select{
		Label:     "Choose a target branch",
		Items:     options,
		Templates: templates,
		Size:      4,
	}

	i, _, err := prompt.Run()
	if err == promptui.ErrInterrupt || err == promptui.ErrEOF {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return branches[i], nil
}

func mustGetToken() string {
	token, err := getToken()
	if err != nil {
		abort(err)
	}
	if token == "" {
		abort(errors.New("token not found in your gitconfig file"))
	}
	return token
}

func getToken() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	gitconfigPath := filepath.Join(u.HomeDir, string(filepath.Separator), ".gitconfig")
	data, err := ioutil.ReadFile(gitconfigPath)
	if err != nil {
		return "", err
	}
	config := parseConfig(data)
	if config["github"] == nil {
		return "", nil
	}
	return config["github"]["token"], nil
}

func mustGetCurrentRepo() (owner string, repo string, head string) {
	mustBeGitRepo()
	cmd := exec.Command("git", "ls-remote", "--get-url", "origin")
	var out []byte
	var err error
	out, err = cmd.Output()
	if err != nil {
		panic(err)
	}
	if m := reRepo.FindStringSubmatch(string(out)); m != nil {
		owner, repo = m[1], m[2]
	}
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	out, err = cmd.Output()
	if err != nil {
		panic(err)
	}
	head = strings.TrimSpace(string(out))
	return
}

func mustBeGitRepo() {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		abort(err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		abort(errors.New("not a git repo"))
	}
}

func parseSection(line string) string {
	if reSection.MatchString(line) {
		matches := reSection.FindStringSubmatch(line)
		if matches != nil && len(matches) == 2 {
			return matches[1]
		}
	}
	return ""
}
func parseValue(line string) (string, string) {
	if m := reVal.FindStringSubmatch(line); m != nil && len(m) == 3 {
		return m[1], m[2]
	}
	return "", ""
}
func isComment(line string) bool {
	return reComment.MatchString(line)
}

func parseConfig(data []byte) map[string]map[string]string {
	config := make(map[string]map[string]string)
	lines := strings.Split(string(data), "\n")
	section := "" //current section tracking while parsing
	for _, line := range lines {
		if isComment(line) {
			continue
		}
		if key, val := parseValue(line); section != "" && key != "" {
			config[section][key] = val
			continue
		}
		//start of a section
		section = parseSection(line)
		if section != "" {
			config[section] = make(map[string]string)
		}
	}
	return config
}

func releaseNotes(title string, commits []*github.Commit) string {
	notes := ""
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		lines := strings.Split(c.Message, "\n")
		notes += fmt.Sprintf("#* [%v] - %v (%v)\n", c.ShortID, lines[0], c.Author)
	}
	return fmt.Sprintf(`#%v
#
# Please enter the realease title as the first line. Lines starting
# with '#' will be ignored, and an empty title & message aborts the operation.
# By removing starting '#' of lines below, you can put them in release body.
#
#**Commits**
#
%v`, title, notes)
}

func mustFindEditor() []string {
	env := os.Getenv("EDITOR")
	if env == "" {
		env = defaultEditor
	}
	re := regexp.MustCompile("\\s")
	parts := re.Split(env, -1)
	path, err := exec.LookPath(parts[0])
	if err != nil {
		abort(fmt.Errorf("unable to find editor(%v): %v", parts[0], err))
	}
	return append([]string{path}, parts[1:]...)
}

func newEditor(cmd []string) *editor {
	gitDir := ".git"
	if dir, ok := os.LookupEnv("GIT_DIR"); ok {
		gitDir = dir
	}
	file := path.Join(gitDir, releaseMsgFilename)
	return &editor{
		cmd:  cmd,
		file: file,
		mode: 0644,
	}
}

type editor struct {
	cmd  []string
	file string
	mode os.FileMode
}

func (ed editor) edit(msg string) (string, string, error) {
	if err := ioutil.WriteFile(ed.file, []byte(msg), ed.mode); err != nil {
		return "", "", fmt.Errorf("unable to write release message: %v", err)
	}

	cmd := exec.Command(ed.cmd[0], append(ed.cmd[1:], ed.file)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("edit error: %v", err)
	}
	data, err := ioutil.ReadFile(ed.file)
	if err != nil {
		return "", "", err
	}
	title, body := parseReleaseMsg(data)
	return title, body, nil
}
func parseReleaseMsg(data []byte) (string, string) {
	lines := strings.Split(string(data), "\n")
	newLines := lines[:0]
	for _, line := range lines {
		if isComment(line) {
			continue
		}
		newLines = append(newLines, line)
	}
	if len(newLines) == 0 {
		return "", ""
	}
	return newLines[0], strings.TrimSpace(strings.Join(newLines[1:], "\n"))
}
func showProgress(msg string, done chan struct{}) {
	progress := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	defer fmt.Print(faint(lineReset))
	i := 1
	for {
		fmt.Print(faint(lineReset))
		fmt.Printf("%v %v", green(progress[i%len(progress)]), faint(msg))

		select {
		case <-done:
			return
		default:
			time.Sleep(50 * time.Millisecond)
			i++
		}
	}
}
