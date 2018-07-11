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
	"github.com/manifoldco/promptui"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
)

const (
	//DefaultEditor - editor to use when $EDITOR env is empty
	DefaultEditor      = "vim"
	tagPrefix          = "v"
	releaseMsgFilename = "RELEASE_MSG"
)

var (
	releaseMsgFile = path.Join(".git", releaseMsgFilename)
	reRepo         = regexp.MustCompile(`([a-z-]+)/([a-z-]+)`)
	reSection      = regexp.MustCompile(`^\[(\w+)\]`)
	reVal          = regexp.MustCompile(`^\s+(\w+)\s*=\s*(.*)$`)

	cyan          = promptui.Styler(promptui.FGCyan, promptui.FGBold)
	faint         = promptui.Styler(promptui.FGFaint, promptui.FGBold)
	startBoldCyan = strings.Replace(cyan(""), promptui.ResetCode, "", -1)
	reset         = promptui.ResetCode
)

func main() {
	if err := do(); err != nil {
		panic(err)
	}
}

func do() error {
	token, err := getToken()
	if err != nil {
		return err
	}
	owner, repo, err := getCurrentRepo()

	ctx := context.Background()
	client := newClient(ctx, token)
	if err != nil {
		return err
	}
	latest, branches, err := getBranchesAndReleases(client, owner, repo)
	if err != nil {
		return err
	}
	if latest == nil {
		panic("no previous release") //@TODO
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
	compare, _, err := client.Repositories.CompareCommits(ctx, owner, repo, lastRel, *target.Name)
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
	fmt.Print(reset)
	if err == promptui.ErrInterrupt || err == promptui.ErrEOF {
		return nil
	} else if err != nil {
		return err
	}

	ed, err := newEditor()
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
	release := &github.RepositoryRelease{
		Name:            &title,
		TagName:         &tagName,
		TargetCommitish: compare.Commits[len(compare.Commits)-1].SHA,
		Body:            &body,
	}
	release, _, err = client.Repositories.CreateRelease(ctx, owner, repo, release)
	if err != nil {
		return err
	}
	fmt.Printf("%v New release(%v) created\n", promptui.IconGood, cyan(*release.TagName))
	return nil
}

func getBranchesAndReleases(client *github.Client, owner, repo string) (*github.RepositoryRelease, []*github.Branch, error) {
	var latest *github.RepositoryRelease
	var branches []*github.Branch
	var err error
	var wg sync.WaitGroup
	ctx := context.Background()
	wg.Add(2)
	go func() {
		defer wg.Done()
		var e error
		latest, _, e = client.Repositories.GetLatestRelease(ctx, owner, repo)
		if err == nil {
			err = e
		}
	}()
	go func() {
		defer wg.Done()
		var e error
		branches, _, e = client.Repositories.ListBranches(ctx, owner, repo, nil)
		if err == nil {
			err = e
		}
	}()
	wg.Wait()

	if err != nil {
		return nil, nil, err
	}

	cur, err := getCurrentBranch()
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(branches, func(i, j int) bool {
		li := len(*branches[i].Name)
		lj := len(*branches[j].Name)
		if *branches[i].Name == cur {
			li = 0
		}
		if *branches[j].Name == cur {
			lj = 0
		}
		return li < lj
	})
	return latest, branches, err
}
func getCurrentBranch() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
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

func newClient(ctx context.Context, token string) *github.Client {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc)
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

func getCurrentRepo() (string, string, error) {

	if ok, err := isGitRepo(); err != nil {
		return "", "", err
	} else if !ok {
		return "", "", errors.New("not inside a git repo")
	}
	cmd := exec.Command("git", "ls-remote", "--get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", "", err
	}
	if m := reRepo.FindStringSubmatch(string(out)); m != nil {
		return m[1], m[2], nil
	}
	return "", "", nil
}

func isGitRepo() (bool, error) {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func parseConfig(data []byte) (string, string) {
	config := string(data)
	lines := strings.Split(config, "\n")
	found := false
	var user, token string
	for _, line := range lines {
		if found {
			if m := reVal.FindStringSubmatch(line); m != nil {
				if m[1] == "user" {
					user = m[2]
				}
				if m[1] == "token" {
					token = m[2]
				}
				continue
			}
		}
		if found && reSection.MatchString(line) {
			found = false
		}
		if reSection.MatchString(line) {
			matches := reSection.FindStringSubmatch(line)
			if matches != nil && len(matches) == 2 && matches[1] == "github" {
				found = true
				continue
			}
		}
	}
	return user, token
}

func releaseNotes(title string, commits []github.RepositoryCommit) string {
	notes := ""
	for _, c := range commits {
		notes += fmt.Sprintf("* [%v] - %v\n", (*c.SHA)[0:6], *c.Commit.Message)
	}
	return fmt.Sprintf(`%v
# Please enter the realease title as the first line. Lines starting
# with '#' will be ignored, and an empty message aborts the operation.
**Commits**

%v`, title, notes)
}

func newEditor() (*editor, error) {
	env := os.Getenv("EDITOR")
	if env == "" {
		env = DefaultEditor
	}
	path, err := exec.LookPath(env)
	if err != nil {
		return nil, fmt.Errorf("unable to find editor(%v): %v", path, err)
	}
	return &editor{
		path: path,
		file: releaseMsgFile,
		mode: 0644,
	}, nil

}

type editor struct {
	path string
	file string
	mode os.FileMode
}

func (ed editor) edit(msg string) (string, string, error) {
	if err := ioutil.WriteFile(ed.file, []byte(msg), ed.mode); err != nil {
		return "", "", fmt.Errorf("unable to write release message: %v", err)
	}
	cmd := exec.Command(ed.path, ed.file)
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
