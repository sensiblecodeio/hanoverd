package git

// everything git and github related

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const GIT_BASE_DIR = "repo"

// Invoke a `command` in `workdir` with `args`, connecting up its Stdout and Stderr
func Command(workdir, command string, args ...string) *exec.Cmd {
	// log.Printf("wd = %s cmd = %s, args = %q", workdir, command, append([]string{}, args...))

	cmd := exec.Command(command, args...)
	cmd.Dir = workdir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

var (
	ErrEmptyRepoName         = errors.New("Empty repository name")
	ErrEmptyRepoOrganization = errors.New("Empty repository organization")
	ErrUserNotAllowed        = errors.New("User not in the allowed set")
)

type Repository struct {
	Name         string `json:"name"`
	Url          string `json:"url"`
	Organization string `json:"organization"`
}

type Pusher struct {
	Name string `json:"name"`
}

type NonGithub struct {
	NoBuild bool `json:"nobuild"`
	Wait    bool `json:"wait"`
}

type JustNongithub struct {
	NonGithub NonGithub `json:"nongithub"`
}

func ParseJustNongithub(in []byte) (j JustNongithub, err error) {
	err = json.Unmarshal(in, &j)
	return
}

type PushEvent struct {
	Ref        string     `json:"ref"`
	Deleted    bool       `json:"deleted"`
	Repository Repository `json:"repository"`
	After      string     `json:"after"`
	Pusher     Pusher     `json:"pusher"`
	NonGithub  NonGithub  `json:"nongithub"`
	HtmlUrl    string     `json:"html_url"`
}

type GithubStatus struct {
	State       string `json:"state"`
	TargetUrl   string `json:"target_url"`
	Description string `json:"description"`
}

var ErrSkipGithubEndpoint = errors.New("Github endpoint skipped")

// Creates or updates a mirror of `url` at `gitDir` using `git clone --mirror`
func gitLocalMirror(url, gitDir, ref string, messages io.Writer) (err error) {

	if _, err := os.Stat(gitDir); err == nil {
		// Repo already exists, don't need to clone it.

		if gitAlreadyHaveRef(gitDir, ref) {
			// Sha already exists, don't need to fetch.
			// log.Printf("Already have ref: %v %v", gitDir, ref)
			return nil
		}

		return gitFetch(gitDir, url, messages)
	}

	err = os.MkdirAll(gitDir, 0777)
	if err != nil {
		return fmt.Errorf("gitLocalMirror: %v", err)
	}

	return gitClone(url, gitDir, messages)
}

func gitClone(url, gitDir string, messages io.Writer) error {
	cmd := Command(".", "git", "clone", "-q", "--mirror", url, gitDir)
	cmd.Stdout = messages
	cmd.Stderr = messages
	return cmd.Run()
}

func gitFetch(gitDir, url string, messages io.Writer) (err error) {

	done := make(chan struct{})
	// Try "git remote update"

	cmd := Command(gitDir, "git", "fetch", "-f", url, "*:*")
	cmd.Stdout = messages
	cmd.Stderr = messages

	go func() {
		start := time.Now()
		err = cmd.Run()
		log.Printf("Normal completion %v in %v", cmd.Args, time.Since(start))
		close(done)
	}()

	const timeout = 1 * time.Minute

	select {
	case <-done:
	case <-time.After(timeout):
		err = cmd.Process.Kill()
		log.Printf("Killing cmd %+v after %v, error returned: %v", cmd, timeout, err)
		err = fmt.Errorf("cmd %+v timed out", cmd)
	}

	if err != nil {
		// git fetch where there is no update is exit status 1.
		if err.Error() != "exit status 1" {
			return err
		}
	}

	return nil
}

var ShaLike = regexp.MustCompile("[0-9a-zA-Z]{40}")

// Returns true if ref is sha-like and is in the object database.
// The "sha-like" condition ensures that refs like `master` are always
// freshened.
func gitAlreadyHaveRef(gitDir, sha string) bool {
	if !ShaLike.MatchString(sha) {
		return false
	}
	cmd := Command(gitDir, "git", "cat-file", "-t", sha)
	cmd.Stdout = nil
	cmd.Stderr = nil

	err := cmd.Run()
	return err == nil
}

func gitHaveFile(gitDir, ref, path string) (ok bool, err error) {
	cmd := Command(gitDir, "git", "show", fmt.Sprintf("%s:%s", ref, path))
	cmd.Stdout = nil // don't want to see the contents
	err = cmd.Run()
	ok = true
	if err != nil {
		ok = false
		if err.Error() == "exit status 128" {
			// This happens if the file doesn't exist.
			err = nil
		}
	}
	return ok, err
}

func gitRevParse(gitDir, ref string) (sha string, err error) {
	cmd := Command(gitDir, "git", "rev-parse", ref)
	cmd.Stdout = nil // for cmd.Output

	var stdout []byte
	stdout, err = cmd.Output()
	if err != nil {
		return
	}

	sha = strings.TrimSpace(string(stdout))
	return
}

func gitDescribe(gitDir, ref string) (desc string, err error) {
	cmd := Command(gitDir, "git", "describe", "--all", "--tags", "--long", ref)
	cmd.Stdout = nil // for cmd.Output

	var stdout []byte
	stdout, err = cmd.Output()
	if err != nil {
		return
	}

	desc = strings.TrimSpace(string(stdout))
	desc = strings.TrimPrefix(desc, "heads/")
	return
}

func gitCheckout(gitDir, checkoutDir, ref string) error {

	err := os.MkdirAll(checkoutDir, 0777)
	if err != nil {
		return err
	}

	args := []string{"--work-tree", checkoutDir, "checkout", ref, "--", "."}
	err = Command(gitDir, "git", args...).Run()
	if err != nil {
		return err
	}

	// Set mtimes to time file is most recently affected by a commit.
	// This is annoying but unfortunately git sets the timestamps to now,
	// and docker depends on the mtime for cache invalidation.
	err = gitSetMTimes(gitDir, checkoutDir, ref)
	if err != nil {
		return err
	}

	return nil
}

func gitSetMTimes(gitDir, checkoutDir, ref string) error {
	// From https://github.com/rosylilly/git-set-mtime with tweaks
	// Copyright (c) 2014 Sho Kusano

	// MIT License

	// Permission is hereby granted, free of charge, to any person obtaining
	// a copy of this software and associated documentation files (the
	// "Software"), to deal in the Software without restriction, including
	// without limitation the rights to use, copy, modify, merge, publish,
	// distribute, sublicense, and/or sell copies of the Software, and to
	// permit persons to whom the Software is furnished to do so, subject to
	// the following conditions:

	// The above copyright notice and this permission notice shall be
	// included in all copies or substantial portions of the Software.

	// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
	// EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
	// MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
	// NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
	// LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
	// OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
	// WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

	lsFiles := Command(gitDir, "git", "ls-tree", "-r", "--name-only", "-z", ref)
	lsFiles.Stdout = nil
	out, err := lsFiles.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %v", err)
	}

	dirMTimes := map[string]time.Time{}

	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	for _, file := range files {
		gitLog := Command(gitDir, "git", "log", "-1", "--date=rfc2822",
			"--format=%cd", ref, "--", file)
		gitLog.Stdout = nil
		gitLog.Stderr = os.Stderr

		out, err := gitLog.Output()
		if err != nil {
			return fmt.Errorf("git log: %v", err)
		}

		mStr := strings.TrimSpace(strings.TrimLeft(string(out), "Date:"))
		mTime, err := time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", mStr)
		if err != nil {
			return fmt.Errorf("parse time: %v", err)
		}

		// Loop over each directory in the path to `file`, updating `dirMTimes`
		// to take the most recent time seen.
		dir := filepath.Dir(file)
		for {
			if other, ok := dirMTimes[dir]; ok {
				if mTime.After(other) {
					// file mTime is more recent than previous seen for 'dir'
					dirMTimes[dir] = mTime
				}
			} else {
				// first occurrence of dir
				dirMTimes[dir] = mTime
			}

			// Remove one directory from the path until it isn't changed anymore
			if dir == filepath.Dir(dir) {
				break
			}
			dir = filepath.Dir(dir)
		}

		err = os.Chtimes(checkoutDir+"/"+file, mTime, mTime)
		if err != nil {
			return fmt.Errorf("chtimes: %v", err)
		}

		// fmt.Printf("%s: %s\n", file, mTime)
	}

	for dir, mTime := range dirMTimes {
		err = os.Chtimes(checkoutDir+"/"+dir, mTime, mTime)
		if err != nil {
			return fmt.Errorf("chtimes: %v", err)
		}
		// fmt.Printf("%s: %s\n", dir, mTime)
	}
	return nil
}

type BuildDirectory struct {
	Name, Dir string
	Cleanup   func()
}

func PrepBuildDirectory(
	gitDir, remote, ref string,
) (*BuildDirectory, error) {

	if strings.HasPrefix(remote, "github.com/") {
		remote = "https://" + remote
	}

	gitDir, err := filepath.Abs(gitDir)
	if err != nil {
		return nil, fmt.Errorf("unable to determine abspath: %v", err)
	}

	err = gitLocalMirror(remote, gitDir, ref, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("unable to gitLocalMirror: %v", err)
	}

	rev, err := gitRevParse(gitDir, ref)
	if err != nil {
		return nil, fmt.Errorf("unable to parse rev: %v", err)
	}

	tagName, err := gitDescribe(gitDir, rev)
	if err != nil {
		return nil, fmt.Errorf("unable to describe %v: %v", rev, err)
	}

	shortRev := rev[:10]
	checkoutPath := path.Join(gitDir, "c/"+shortRev)

	err = checkout(gitDir, checkoutPath, rev)
	if err != nil {
		return nil, err
	}

	cleanup := func() {
		err := SafeCleanup(checkoutPath)
		if err != nil {
			log.Printf("Error cleaning up path: ", checkoutPath)
		}
	}

	return &BuildDirectory{tagName, checkoutPath, cleanup}, nil
}

func checkout(gitDir, checkoutPath, rev string) error {
	err := gitCheckout(gitDir, checkoutPath, rev)
	if err != nil {
		return fmt.Errorf("failed to checkout: %v", err)
	}

	err = PrepSubmodules(gitDir, checkoutPath, rev)
	if err != nil {
		return fmt.Errorf("failed to prep submodules: %v", err)
	}
	return nil
}

func SafeCleanup(path string) error {
	if path == "/" || path == "" || path == "." || strings.Contains(path, "..") {
		return fmt.Errorf("invalid path specified for deletion %q", path)
	}
	return os.RemoveAll(path)
}
