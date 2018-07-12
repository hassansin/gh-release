# gh-release

Interactive command line tool that let's you to create Github releases with auto-generated release message from commits


## Install

1. Install the binary

```
go install -i github.com/hassansin/gh-release
```

Make sure `$GOPATH/bin` is added your `$PATH`:

```
export PATH=$PATH:$GOPATH/bin
```

2. Create a [`Github Personal Access Token`](https://github.com/settings/tokens) and add it to your `~/.gitconfig` file:

```
[github]
	user = github-username
	token = access-token
```

3. (optional) When editing release message, it will open the editor found in `$EDITOR` environment variable, will fallback to `vim` if not found. You can set the environment variable to the path of your editor executable.

```
export EDITOR=/usr/bin/code
```

## Usage

Just run `gh-release` and follow the prompts.

The auto-generated release message will be commented out, you need to uncomment the lines that you want to be in your release body.

