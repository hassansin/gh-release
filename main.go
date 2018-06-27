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
	mode               = 0644
)

var releaseMsgFile = path.Join(".git", releaseMsgFilename)

type opt struct {
	Label   string
	Commits []*gitlog.Commit
}

func (o opt) commitNotes() string {
	notes := ""
	for _, c := range o.Commits {
		notes += fmt.Sprintf("* [%v] - %v\n", c.Hash.Short, c.Subject)
	}
	return notes
}
func (o opt) releaseNotes(title string) string {
	return fmt.Sprintf(`%v
# Please enter the realease title as the first line. Lines starting
# with '#' will be ignored, and an empty message aborts the operation.
**Commits**

%v`, title, o.commitNotes())
}
func (o opt) targetCommitish() string {
	return o.Commits[0].Hash.Short
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

func getOption(oldRev, newRev string) (*opt, error) {
	commits, err := getCommitsRange(oldRev, newRev)
	if err != nil {
		return nil, err
	}
	if len(commits) > 0 {
		return &opt{
			Label:   fmt.Sprintf("from %v to %v", oldRev, newRev),
			Commits: commits,
		}, nil
	}
	return nil, nil
}

func mustAppendOption(oldRev, newRev string, opts []*opt) []*opt {
	opt, err := getOption(oldRev, newRev)
	if err != nil {
		panic(err)
	}
	if opt != nil {
		return append(opts, opt)
	}
	return opts
}

func selectOption(opts []*opt) (*opt, error) {
	tplCounts := `{{ print "(" (len .Commits)  " commits)" | faint }}`
	templates := &promptui.SelectTemplates{
		Label:    fmt.Sprintf("%s {{.Label}}: ", promptui.IconInitial),
		Active:   fmt.Sprintf("%s {{.Label | cyan }} %v ", "▣", tplCounts),
		Inactive: fmt.Sprintf("%s {{.Label }} %v", "▢", tplCounts),
		Selected: fmt.Sprintf(`{{ "%s" | green }} {{.Label | faint }} %v`, promptui.IconGood, tplCounts),
		Details: `
------ Commits ({{.Label | faint}}) ----- {{range .Commits}}
{{.Hash.Short | magenta}} {{.Subject | faint}} {{end}}`,
	}

	prompt := promptui.Select{
		Label:     "Please choose an option below",
		Items:     opts,
		Templates: templates,
		Size:      4,
	}

	i, _, err := prompt.Run()
	if err != nil {
		return nil, err
	}
	return opts[i], nil
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

	var selected *opt
	version := ""

	if len(releases) < 1 {
		commits, err := getCommits(br.ShortName())
		if err != nil {
			panic(err)
		}
		selected = &opt{Commits: commits}
		version = "1.0.0"
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

		upsBr, err := br.Upstream()
		if err != nil {
			panic(err)
		}

		var opts []*opt

		opts = mustAppendOption(r.TagName, upsBr.LongName(), opts)
		opts = mustAppendOption(r.TagName, br.ShortName(), opts)
		opts = mustAppendOption(br.ShortName(), upsBr.LongName(), opts)
		//draft
		opts = mustAppendOption(upsBr.LongName(), br.ShortName(), opts)
		if len(opts) == 0 {
			cyan := color.New(color.FgCyan).SprintFunc()
			fmt.Printf("your latest release(%v) already pointing to the HEAD of %v", cyan(r.TagName), cyan(upsBr.LongName()))
			return
		}

		selected, err = selectOption(opts)
		if err == promptui.ErrInterrupt || err == promptui.ErrEOF {
			return
		} else if err != nil {
			panic(err)
		}
	}

	/*
	 */
	ed, err := newEditor()
	if err != nil {
		panic(err)
	}

	msg, err := ed.edit(selected.releaseNotes(version))
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

	prompt := promptui.Prompt{
		Label:     "Please enter Tag name",
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
		TargetCommitish: selected.targetCommitish(),
		Body:            strings.Join(newLines[1:], "\n"),
	}
	r, err := c.CreateRelease(pr, release)
	if err != nil {
		panic(err)
	}
	fmt.Printf("new release(%v) created\n", r.TagName)
}
