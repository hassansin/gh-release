package github

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/go-github/github"
	"github.com/shurcooL/githubv4"
	"github.com/shurcooL/graphql"
	"golang.org/x/oauth2"
)

type Commit struct {
	Message string
	ID      string
	ShortID string
	Author  string
}

type Tag struct {
	Name        string
	Target      *Commit
	CommitCount int
}

type Release struct {
	Name        string
	Description string
	Tag         Tag
	HTMLURL     string
}

type Branch struct {
	Name        string
	Head        *Commit
	CommitCount int
}

type Repository struct {
	Owner         string
	Name          string
	LatestRelease *Release
	Branches      []*Branch
}

type GithubClient interface {
	GetRepository() (*Repository, error)
	CompareCommits(base, head *Commit) ([]*Commit, error)
	CreateRelease(*Release) (*Release, error)
}

func New(owner, name, token string) GithubClient {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	client := oauth2.NewClient(ctx, ts)
	return &Github{
		client: client,
		ctx:    ctx,
		owner:  owner,
		name:   name,
	}

}

type Github struct {
	ctx         context.Context
	client      *http.Client
	owner, name string
}

func (c *Github) GetRepository() (*Repository, error) {
	client := githubv4.NewClient(c.client)
	type RefNode struct {
		Name   string
		Target struct {
			Commit struct {
				History struct {
					TotalCount int
					Edges      []struct {
						Node struct {
							Message        string
							Oid            string
							AbbreviatedOid string
							Author         struct {
								Name string
							}
						}
					}
				} `graphql:"history(first:1)"`
			} `graphql:"... on Commit"`
		}
	}

	var query struct {
		Repository struct {
			Refs struct {
				Edges []struct {
					Node RefNode
				}
			} `graphql:"refs(first: 100, refPrefix: $refPrefix)"`
			Releases struct {
				Edges []struct {
					Node struct {
						Name string
						Tag  RefNode
					}
				}
			} `graphql:"releases(last:1)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	vars := map[string]interface{}{
		"owner":     graphql.String(c.owner),
		"name":      graphql.String(c.name),
		"refPrefix": graphql.String("refs/heads/"),
	}

	err := client.Query(c.ctx, &query, vars)
	if err != nil {
		return nil, err
	}
	releases := query.Repository.Releases.Edges
	var latestRelease *Release
	if len(releases) > 0 && len(releases[0].Node.Tag.Target.Commit.History.Edges) > 0 {
		n := releases[0].Node
		t := n.Tag
		c := t.Target.Commit.History.Edges[0].Node
		latestRelease = &Release{
			Name: n.Name,
			Tag: Tag{
				Name:        t.Name,
				CommitCount: t.Target.Commit.History.TotalCount,
				Target: &Commit{
					Message: c.Message,
					ID:      c.Oid,
					ShortID: c.AbbreviatedOid,
				},
			},
		}
	}
	var branches []*Branch
	refs := query.Repository.Refs.Edges

	if len(refs) > 0 {
		for _, ref := range refs {
			br := &Branch{
				Name:        ref.Node.Name,
				CommitCount: ref.Node.Target.Commit.History.TotalCount,
			}
			if br.CommitCount > 0 {
				c := ref.Node.Target.Commit.History.Edges[0].Node
				br.Head = &Commit{
					Message: c.Message,
					ID:      c.Oid,
					ShortID: c.AbbreviatedOid,
					Author:  c.Author.Name,
				}
			}
			branches = append(branches, br)
		}
	}

	return &Repository{
		Owner:         c.owner,
		Name:          c.name,
		LatestRelease: latestRelease,
		Branches:      branches,
	}, nil
}

func (c *Github) CompareCommits(base, head *Commit) ([]*Commit, error) {
	client := github.NewClient(c.client)

	compare, _, err := client.Repositories.CompareCommits(c.ctx, c.owner, c.name, base.ID, head.ID)
	if *compare.Status != "ahead" {
		return nil, nil
	}
	var commits []*Commit
	for _, c := range compare.Commits {
		commits = append(commits, &Commit{
			Message: *c.Commit.Message,
			ID:      *c.SHA,
			ShortID: (*c.SHA)[:7],
			Author:  *c.Commit.Author.Name,
		})
	}
	return commits, err
}

func (c *Github) CreateRelease(r *Release) (*Release, error) {
	if r.Name == "" || r.Description == "" {
		return nil, errors.New("empty release title and message")
	}
	client := github.NewClient(c.client)
	rel, _, err := client.Repositories.CreateRelease(c.ctx, c.owner, c.name, &github.RepositoryRelease{
		Name:            &r.Name,
		TagName:         &r.Tag.Name,
		TargetCommitish: &r.Tag.Target.ID,
		Body:            &r.Description,
	})
	r.HTMLURL = *rel.HTMLURL
	return r, err
}
