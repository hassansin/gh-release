package main

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/davecgh/go-spew/spew"
)

func TestParseConfig(t *testing.T) {
	got := parseConfig([]byte(`
[core]
	editor = vim
	whitespace = fix,-indent-with-non-tab,trailing-space,cr-at-eol
[github]
	user = testuser
	token = token
[gitflow "prefix"]
	feature = feature-
[alias]
	a = add --all
	#############
	ap = apply
`))
	exp := map[string]map[string]string{
		"core": map[string]string{
			"editor":     "vim",
			"whitespace": "fix,-indent-with-non-tab,trailing-space,cr-at-eol",
		},
		"github": map[string]string{
			"token": "token",
			"user":  "testuser",
		},
		"gitflow \"prefix\"": map[string]string{
			"feature": "feature-",
		},
		"alias": map[string]string{
			"a":  "add --all",
			"ap": "apply",
		},
	}
	if !reflect.DeepEqual(exp, got) {
		fmt.Printf("exp: %v\ngot: %v\n\n", spew.Sdump(exp), spew.Sdump(got))
		t.FailNow()
	}
}
