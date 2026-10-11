/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import (
	"runtime"
	"testing"
)

func TestIsReadOnlyCommand_Allows(t *testing.T) {
	for _, c := range []string{
		"ls -la",
		"ls -la && pwd",
		"cat README.md | grep -n foo | head -20",
		"grep -rn 'func main' --include='*.go' .",
		"grep foo *.go",
		"wc -l main.go; ls",
		"git status",
		"git diff --stat",
		"git log --oneline -n 20",
		"git show HEAD --stat",
		"git branch",
		"git branch -a",
		"git branch --list 'feat/*'",
		"git tag",
		"git tag -l 'v1.*'",
		"git remote -v",
		"git remote get-url origin",
		"git stash list",
		"git reflog",
		"git config --get user.name",
		"git config --list",
		"git config get user.name",
		"go version",
		"go env GOPATH",
		"go list -m all",
		"go vet ./...",
		"go doc fmt.Println",
		"sed -n '1,20p' main.go",
		"sed -n 5p main.go",
		"sed 's/foo/bar/g' main.go",
		"sed -E -e 's|a|b|' -e '/^#/d' file.txt",
		"awk -F: '{print $1}' data.txt",
		"awk '{ n++ } END { print n }' data.txt",
		"sort -u names.txt",
		"sort -k2 -n data.txt | uniq -c",
		"find . -name '*.go' -type f",
		"jq '.items[] | .name' data.json",
		"kubectl get pods",
		"kubectl config current-context",
		"kubectl config view",
		"npm ls",
		"npm config get registry",
		"docker ps",
		"docker network ls",
		"docker volume inspect data",
		"node --version",
		"python3 --version",
		"tail -n 50 app.log",
		"ls 2>/dev/null",
		"ls > /dev/null 2>&1",
		"grep x < input.txt",
		"date +%Y-%m-%d",
		"hostname",
		"echo hello",
		"/usr/bin/git status",
		"rg -n TODO",
	} {
		if !IsReadOnlyCommand(c) {
			t.Errorf("IsReadOnlyCommand(%q) = false, want true", c)
		}
	}
}

func TestIsReadOnlyCommand_Refuses(t *testing.T) {
	cases := []string{
		// chaining and later pipe stages
		"ls; rm -rf build",
		"ls && rm -rf build",
		"ls || touch pwned",
		"ls\nrm -rf build",
		"cat a | grep b | sh",
		"cat a | tee out.txt",
		"cat a | xargs rm",
		"ls & touch pwned",
		// substitution and expansion
		"echo $(touch pwned)",
		"echo `touch pwned`",
		"ls $(id)",
		"echo \"$(touch pwned)\"",
		"echo $OPENAI_API_KEY",
		"echo \"${AWS_SECRET_ACCESS_KEY}\"",
		"cat <(touch pwned)",
		"FOO=bar ls",
		"(rm -rf build)",
		"{ ls; rm -rf build; }",
		"if true; then rm x; fi",
		// redirection
		"ls > out.txt",
		"ls >> out.txt",
		"ls 2> err.txt",
		"ls &> out.txt",
		"ls >| out.txt",
		"ls <> f",
		"cat <<EOF\nhi\nEOF",
		// env, interpreters
		"env",
		"env touch pwned",
		"printenv",
		"printenv AWS_SECRET_ACCESS_KEY",
		"python -c 'import os; os.system(\"id\")'",
		"python3 -c 'print(1)'",
		"node -e 'require(\"child_process\").execSync(\"id\")'",
		// awk
		"awk 'BEGIN { system(\"id\") }'",
		"awk '{ print > \"out.txt\" }' data.txt",
		"awk 'BEGIN { \"id\" | getline x; print x }'",
		"awk 'BEGIN { print ENVIRON[\"HOME\"] }'",
		"awk -f prog.awk data.txt",
		// sed
		"sed -i 's/a/b/' f",
		"sed -i.bak 's/a/b/' f",
		"sed --in-place=.bak 's/a/b/' f",
		"sed --in-pl 's/a/b/' f",
		"sed -ni 's/a/b/p' f",
		"sed -I '' 's/a/b/' f",
		"sed 's/a/b/w out.txt' f",
		"sed 's/a/id/e' f",
		"sed 'e id' f",
		"sed '1w out.txt' f",
		"sed -n '/x/r /etc/hosts' f",
		"sed -f script.sed f",
		"sed -\\i 's/a/b/' f",
		"sed -\"i\" 's/a/b/' f",
		// sort
		"sort --output=out.txt in.txt",
		"sort --out=out.txt in.txt",
		"sort -o out.txt in.txt",
		"sort -ro out.txt in.txt",
		"sort --compress-program=./evil in.txt",
		"sort *",
		// find
		"find . -ok rm {} ;",
		"find . -fprint out.txt",
		"find . -fprintf out.txt %p",
		"find . -delete",
		"find . -exec rm {} +",
		"find . -execdir id ;",
		// git writes
		"git config user.name x",
		"git config --global user.email x@example.invalid",
		"git config --add core.hooksPath x",
		"git config --unset user.name",
		"git config set user.name x",
		"git stash",
		"git stash drop",
		"git stash pop",
		"git tag v9.9.9",
		"git tag -d v1",
		"git branch new-branch",
		"git branch -D main",
		"git branch -m old new",
		"git remote add evil https://example.invalid/x.git",
		"git remote set-url origin https://example.invalid/x.git",
		"git reflog expire --all",
		"git reflog delete HEAD@{1}",
		"git log --output=/tmp/x",
		"git diff --ext-diff",
		"git -C /tmp status",
		"git -c core.pager=id log",
		"git grep -O x",
		"git commit -m x",
		// go
		"go vet -vettool=./evil ./...",
		"go vet --vettool=./evil ./...",
		"go env -w GOFLAGS=-x",
		"go env -u GOFLAGS",
		"go list -toolexec ./evil ./...",
		"go run main.go",
		// kubectl, npm, yarn, docker
		"kubectl config set-context x",
		"kubectl config use-context prod",
		"kubectl config view --raw",
		"kubectl delete pod x",
		"npm config set registry https://example.invalid",
		"npm install",
		"yarn config set x y",
		"yarn info react",
		"docker network create x",
		"docker network rm x",
		"docker volume rm data",
		"docker volume prune",
		"docker run alpine",
		// misc
		"rg --pre ./evil TODO",
		"rg --pre=./evil TODO",
		"rg TODO *",
		"tail -f app.log",
		"tree -o out.txt",
		"xmllint --output out.xml in.xml",
		"yq -i '.a = 1' f.yaml",
		"jq -n env",
		"jq -n '$ENV.HOME'",
		"less '+!id' f",
		"uniq in.txt out.txt",
		"date -s 2020-01-01",
		"date 0101000020",
		"hostname evil",
		"./ls",
		"bin/cat x",
		"/tmp/git status",
		"cargo metadata",
		"rustc --version",
		"echo hi > out.txt",
		"",
	}
	for _, c := range cases {
		if IsReadOnlyCommand(c) {
			t.Errorf("IsReadOnlyCommand(%q) = true, want false", c)
		}
	}
}

func TestIsReadOnlyCommand_WindowsExpansion(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cmd.exe expansion only applies on Windows")
	}
	for _, c := range []string{"echo %OPENAI_API_KEY%", "echo !PATH!", "dir ^& del x"} {
		if IsReadOnlyCommand(c) {
			t.Errorf("IsReadOnlyCommand(%q) = true, want false", c)
		}
	}
}

func TestSedScriptReadOnly(t *testing.T) {
	ok := []string{"p", "1,20p", "$p", "/foo/d", "s/a/b/g", "s|a/b|c|2p", "y/abc/xyz/", "1~2d", "/start/,/end/p", "s/a\\/b/c/"}
	bad := []string{"w out", "1w out", "s/a/b/w out", "s/a/b/e", "e id", "r /etc/hosts", "R f", "W f", "s/a/b", "a text", "F", "z"}
	for _, s := range ok {
		if !sedScriptReadOnly(s) {
			t.Errorf("sedScriptReadOnly(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if sedScriptReadOnly(s) {
			t.Errorf("sedScriptReadOnly(%q) = true, want false", s)
		}
	}
}
