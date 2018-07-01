package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/blang/semver"
	"github.com/fatih/color"
	"github.com/github/hub/github"
	"github.com/manifoldco/promptui"
	"github.com/pkg/errors"
	gitlog "github.com/tsuyoshiwada/go-gitlog"
)

const (
	//DefaultEditor - editor to use when $EDITOR env is empty
	DefaultEditor      = "vim"
	tagPrefix          = "v"
	releaseMsgFilename = "RELEASE_MSG"
)

var releaseMsgFile = path.Join(".git", releaseMsgFilename)

func releaseNotes(title string, commits []*gitlog.Commit) string {
	notes := ""
	for _, c := range commits {
		notes += fmt.Sprintf("* [%v] - %v\n", c.Hash.Short, c.Subject)
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

func (ed editor) edit(msg string) (string, error) {
	if err := ioutil.WriteFile(ed.file, []byte(msg), ed.mode); err != nil {
		return "", fmt.Errorf("unable to write release message: %v", err)
	}
	cmd := exec.Command(ed.path, ed.file)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("edit error: %v", err)
	}
	data, err := ioutil.ReadFile(ed.file)
	return string(data), err
}

func remoteBranches() ([]string, error) {
	cmd := exec.Command("git", "ls-remote", "--heads", "-q")
	data, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.Wrap(err, string(data))
	}
	lines := strings.Split(string(data), "\n")
	var branches []string
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) == 2 {
			branches = append(branches, parts[1])
		}
	}
	return branches, nil

}

func getCommitsRange(oldRev, newRev string) ([]*gitlog.Commit, error) {
	git := gitlog.New(&gitlog.Config{Path: "."})
	commits, err := git.Log(&gitlog.RevRange{newRev, oldRev}, nil)
	if err != nil {
		return nil, errors.Wrap(err, "getCommitsRange")
	}
	return commits, nil
}
func getCommits(rev string) ([]*gitlog.Commit, error) {
	git := gitlog.New(&gitlog.Config{Path: "."})
	commits, err := git.Log(&gitlog.Rev{rev}, nil)
	if err != nil {
		return nil, errors.Wrap(err, "getCommits")
	}
	return commits, nil
}

func main() {
	c := github.NewClient("github.com")
	repo, err := github.LocalRepo()
	if err != nil {
		panic(err)
	}
	pr, err := repo.CurrentProject()
	if err != nil {
		panic(err)
	}
	releases, err := c.FetchReleases(pr, 1, nil)
	if err != nil {
		panic(err)
	}
	br, err := repo.CurrentBranch()
	if err != nil {
		panic(err)
	}
	upsBr, err := br.Upstream()
	if err != nil {
		panic(err)
	}
	branches, err := remoteBranches()
	if err != nil {
		panic(err)
	}

	target := upsBr.LongName()
	cyan := color.New(color.FgCyan, color.Bold).SprintFunc()

	prompt := promptui.Prompt{
		Label:     fmt.Sprintf("Create a new release targetted to %v", cyan(upsBr.LongName())),
		IsConfirm: true,
		AllowEdit: false,
		Default:   "y",
		Validate: func(r string) error {
			r = strings.ToLower(r)
			if r == "" {
				return nil
			}
			if r != "y" && r != "n" {
				return errors.New("invalid input")
			}
			return nil
		},
	}
	response, err := prompt.Run()
	if err == promptui.ErrInterrupt || err == promptui.ErrEOF {
		return
	} else if err != nil && err.Error() != "" {
		panic(err)
	}
	if strings.ToLower(response) == "n" {
		templates := &promptui.SelectTemplates{
			Label:    fmt.Sprintf("%s {{.}}: ", promptui.IconInitial),
			Active:   fmt.Sprintf("%s {{.| cyan }}", "▣"),
			Inactive: fmt.Sprintf("%s {{.}}", "▢"),
			Selected: fmt.Sprintf(`{{ "%s" | green }} {{"%s" | bold}} {{.| cyan | bold }}`, promptui.IconGood, "Target:"),
		}

		prompt := promptui.Select{
			Label:     "Please choose a target branch",
			Items:     branches,
			Templates: templates,
			Size:      4,
		}

		i, _, err := prompt.Run()
		if err == promptui.ErrInterrupt || err == promptui.ErrEOF {
			return
		} else if err != nil {
			panic(err)
		}
		target = branches[i]
	}

	var commits []*gitlog.Commit
	version := ""

	if len(releases) < 1 {
		var err error
		commits, err = getCommits(upsBr.LongName())
		if err != nil {
			panic(err)
		}
		version = tagPrefix + "1.0.0"
	} else {

		r := releases[0]
		v, err := semver.Make(strings.TrimPrefix(r.TagName, tagPrefix))
		if err != nil {
			panic(err)
		}
		v.Patch++
		version = v.String()
		if strings.HasPrefix(r.TagName, tagPrefix) {
			version = tagPrefix + version
		}

		commits, err = getCommitsRange(r.TagName, target)
		if err != nil {
			panic(err)
		}
		if len(commits) == 0 {
			fmt.Printf("your latest release(%v) is already pointing to %v", cyan(r.TagName), cyan(target))
			return
		}
	}

	ed, err := newEditor()
	if err != nil {
		panic(err)
	}

	msg, err := ed.edit(releaseNotes(version, commits))
	if err != nil {
		panic(err)
	}
	//validate:
	//1. remove comments
	//2. remove trailing blank lines
	//3. at least one non-empty line
	lines := strings.Split(msg, "\n")
	newLines := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimPrefix(line, " "), "#") {
			continue
		}
		newLines = append(newLines, line)
	}

	prompt = promptui.Prompt{
		Label:     "Please enter release tag",
		AllowEdit: true,
		Default:   version,
	}
	tagName, err := prompt.Run()
	if err != nil {
		panic(err)
	}
	release := &github.Release{
		Name:            newLines[0],
		TagName:         tagName,
		TargetCommitish: commits[0].Hash.Long,
		Body:            strings.Join(newLines[1:], "\n"),
	}
	release, err = c.CreateRelease(pr, release)
	if err != nil {
		panic(err)
	}
	success := color.New(color.Bold).PrintlnFunc()
	success("%v New release(%v) created\n", promptui.IconGood, release.TagName)
}
