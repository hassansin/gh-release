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

func TestParseReleaseMsg(t *testing.T) {
	testCases := []struct {
		name        string
		inp         []byte
		title, body string
	}{
		{
			"empty input",
			[]byte{},
			"",
			"",
		},
		{
			"one line",
			[]byte(`v1.0.0\n`),
			"v1.0.0",
			"",
		},
		{
			"multi lines",
			[]byte(`v1.0.0
some text
some more text`),
			"v1.0.0",
			"some text\nsome more text",
		},
		{
			"commented lines",
			[]byte(`#v1.0.0
#some text
#some more text`),
			"",
			"",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			title, body := parseReleaseMsg(tc.inp)
			if title != tc.title {
				fmt.Printf("exp: %v\ngot: %v\n\n", tc.title, title)
				t.FailNow()
			}
			if body != tc.body {
				fmt.Printf("exp: %v\ngot: %v\n\n", tc.body, body)
				t.FailNow()
			}
		})
	}
}
