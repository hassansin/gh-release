package main

import (
	"context"
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
	"sync"

	"github.com/blang/semver"
	"github.com/google/go-github/github"
	"github.com/hassansin/promptui"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
)

const (
	defaultEditor      = "vim"
	tagPrefix          = "v"
	releaseMsgFilename = "RELEASE_MSG"
)

var (
	releaseMsgFile = path.Join(".git", releaseMsgFilename)
	reRepo         = regexp.MustCompile(`([a-z-]+)/([a-z-]+)`)
	reSection      = regexp.MustCompile(`^\[(\w+)\]`)
	reVal          = regexp.MustCompile(`^\s+(\w+)\s*=\s*(.*)$`)

	cyan          = promptui.Styler(promptui.FGCyan, promptui.FGBold)
	green         = promptui.Styler(promptui.FGGreen, promptui.FGBold)
	faint         = promptui.Styler(promptui.FGFaint, promptui.FGBold)
	startBoldCyan = strings.Replace(cyan(""), promptui.ResetCode, "", -1)
)

func main() {
	mustBeGitRepo()
	editorCmd := mustFindEditor()
	if err := do(editorCmd); err != nil {
		panic(err)
	}
}

func newClient(token, owner, repo string) *Client {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)
	return &Client{
		client: client,
		owner:  owner,
		repo:   repo,
		ctx:    ctx,
	}
}

//Client - Wrapper around github.Client
type Client struct {
	ctx         context.Context
	owner, repo string
	client      *github.Client
}

func (c *Client) compare(base, head string) (*github.CommitsComparison, error) {
	compare, _, err := c.client.Repositories.CompareCommits(c.ctx, c.owner, c.repo, base, head)
	return compare, wrap(err, "unable to compare commits")
}

func (c *Client) createRelease(title, tag, target, body string) (*github.RepositoryRelease, error) {
	release, _, err := c.client.Repositories.CreateRelease(c.ctx, c.owner, c.repo, &github.RepositoryRelease{
		Name:            &title,
		TagName:         &tag,
		TargetCommitish: &target,
		Body:            &body,
	})
	return release, wrap(err, "unable to create new release")
}
func (c *Client) getLatestRelease() (*github.RepositoryRelease, error) {
	latest, _, err := c.client.Repositories.GetLatestRelease(c.ctx, c.owner, c.repo)
	return latest, wrap(err, "unable to get latest release")
}

func (c *Client) listBranches() ([]*github.Branch, error) {
	branches, _, err := c.client.Repositories.ListBranches(c.ctx, c.owner, c.repo, nil)
	return branches, wrap(err, "unable to list branches")
}

//wrap wraps an error with context
func wrap(err error, msg string) error {
	if err != nil {
		err = errors.Wrap(err, msg)
	}
	return err
}

func do(editorCmd []string) error {
	token, err := getToken()
	if err != nil {
		return err
	}
	owner, repo, head, err := getCurrentRepo()
	if err != nil {
		return err
	}
	client := newClient(token, owner, repo)

	latest, branches, err := getBranchesAndReleases(client)
	if err != nil {
		return err
	}
	sortBranches(branches, head)

	if latest == nil {
		return errors.New("no previous release") //@TODO
	}
	target, err := selectTarget(branches)
	if err != nil || target == nil {
		return err
	}
	lastRel := *latest.TagName
	v, err := semver.Make(strings.TrimPrefix(lastRel, tagPrefix))
	if err != nil {
		panic(err)
	}
	v.Patch++
	version := v.String()
	if strings.HasPrefix(lastRel, tagPrefix) {
		version = tagPrefix + version
	}
	compare, err := client.compare(lastRel, *target.Name)
	if err != nil {
		return err
	}
	if *compare.Status == "behind" {
		fmt.Printf("%v is already released", *target.Name)
		return nil
	}
	templates := &promptui.PromptTemplates{
		Success: fmt.Sprintf(`{{ "%s" | green | bold }} {{"%s" | bold}} %v`, promptui.IconGood, "Tag:", startBoldCyan),
	}
	prompt := promptui.Prompt{
		Label:     fmt.Sprintf("Please enter release tag (last release: %v)", cyan(lastRel)),
		AllowEdit: true,
		Default:   version,
		Templates: templates,
	}
	tagName, err := prompt.Run()
	fmt.Print(promptui.ResetCode)
	if err == promptui.ErrInterrupt || err == promptui.ErrEOF {
		return nil
	} else if err != nil {
		return err
	}

	ed, err := newEditor(editorCmd)
	if err != nil {
		return err
	}

	title, body, err := ed.edit(releaseNotes(tagName, compare.Commits))
	if err != nil {
		return err
	}
	if title == "" || len(body) == 0 {
		fmt.Println("aborting due to empty release title and message")
		return nil
	}
	release, err := client.createRelease(title, tagName, *compare.Commits[len(compare.Commits)-1].SHA, body)
	if err != nil {
		return err
	}
	fmt.Printf("%v New release(%v) created\n", green(promptui.IconGood), cyan(*release.TagName))
	return nil
}

func getBranchesAndReleases(client *Client) (*github.RepositoryRelease, []*github.Branch, error) {
	var latest *github.RepositoryRelease
	var branches []*github.Branch
	var err error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		var e error
		latest, e = client.getLatestRelease()
		if err == nil {
			err = e
		}
	}()
	go func() {
		defer wg.Done()
		var e error
		branches, e = client.listBranches()
		if err == nil {
			err = e
		}
	}()
	wg.Wait()
	return latest, branches, err
}

//sort branches by branch name length, keeping head at the top
func sortBranches(branches []*github.Branch, head string) {
	sort.Slice(branches, func(i, j int) bool {
		li := len(*branches[i].Name)
		lj := len(*branches[j].Name)
		if *branches[i].Name == head {
			li = 0
		}
		if *branches[j].Name == head {
			lj = 0
		}
		return li < lj
	})
}

func selectTarget(branches []*github.Branch) (*github.Branch, error) {
	options := make([]string, len(branches))
	for i, b := range branches {
		options[i] = *b.Name
	}

	templates := &promptui.SelectTemplates{
		Label:    fmt.Sprintf("%s {{.}} ", promptui.IconInitial),
		Active:   fmt.Sprintf(`%s {{.| cyan }}`, "▣"),
		Inactive: fmt.Sprintf("%s {{.}}", "▢"),
		Selected: fmt.Sprintf(`{{ "%s" | green | bold }} {{"%s" | bold}} {{.| cyan | bold }}`, promptui.IconGood, "Target:"),
	}

	prompt := promptui.Select{
		Label:     "Please choose a target branch",
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
	_, token := parseConfig(data)
	return token, nil
}

func getCurrentRepo() (owner string, repo string, head string, err error) {
	cmd := exec.Command("git", "ls-remote", "--get-url", "origin")
	var out []byte
	out, err = cmd.Output()
	if err != nil {
		return
	}
	if m := reRepo.FindStringSubmatch(string(out)); m != nil {
		owner, repo = m[1], m[2]
	}
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	out, err = cmd.Output()
	head = strings.TrimSpace(string(out))
	return
}

func mustBeGitRepo() {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		panic(err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		panic(errors.New("not a git repo"))
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
func parseConfig(data []byte) (string, string) {
	config := make(map[string]map[string]string)
	lines := strings.Split(string(data), "\n")
	section := "" //current section tracking while parsing
	for _, line := range lines {
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
	if config["github"] == nil {
		return "", ""
	}
	return config["github"]["user"], config["github"]["token"]
}

func releaseNotes(title string, commits []github.RepositoryCommit) string {
	notes := ""
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		lines := strings.Split(*c.Commit.Message, "\n")
		notes += fmt.Sprintf("#* [%v] - %v (%v)\n", *c.SHA, lines[0], *c.Commit.Author.Name)
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
		panic(fmt.Errorf("unable to find editor(%v): %v", parts[0], err))
	}
	return append([]string{path}, parts[1:]...)
}

func newEditor(cmd []string) (*editor, error) {
	return &editor{
		cmd:  cmd,
		file: releaseMsgFile,
		mode: 0644,
	}, nil

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
	//validate:
	//1. remove comments
	//2. remove trailing blank lines
	//3. at least one non-empty line
	lines := strings.Split(string(data), "\n")
	newLines := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimPrefix(line, " "), "#") {
			continue
		}
		newLines = append(newLines, line)
	}
	return newLines[0], strings.Join(newLines[1:], "\n"), nil
}
