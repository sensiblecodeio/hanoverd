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

// Creates or updates a mirror of `url` at `git_dir` using `git clone --mirror`
func gitLocalMirror(url, git_dir string, messages io.Writer) (err error) {

	err = os.MkdirAll(git_dir, 0777)
	if err != nil {
		return
	}

	cmd := Command(".", "git", "clone", "-q", "--mirror", url, git_dir)
	cmd.Stdout = messages
	cmd.Stderr = messages
	err = cmd.Run()

	if err == nil {
		log.Println("Cloned", url)

	} else if _, ok := err.(*exec.ExitError); ok {

		done := make(chan struct{})
		// Try "git remote update"

		cmd := Command(git_dir, "git", "fetch")
		cmd.Stdout = messages
		cmd.Stderr = messages

		go func() {
			err = cmd.Run()
			log.Printf("Normal completion %v %v", cmd.Args, cmd.Dir)
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
				return
			}
		}

		log.Println("Remote updated", url)

	} else {
		return
	}

	return
}

func gitHaveFile(git_dir, ref, path string) (ok bool, err error) {
	cmd := Command(git_dir, "git", "show", fmt.Sprintf("%s:%s", ref, path))
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

func gitRevParse(git_dir, ref string) (sha string, err error) {
	cmd := Command(git_dir, "git", "rev-parse", ref)
	cmd.Stdout = nil // for cmd.Output

	var stdout []byte
	stdout, err = cmd.Output()
	if err != nil {
		return
	}

	sha = strings.TrimSpace(string(stdout))
	return
}

func gitDescribe(git_dir, ref string) (desc string, err error) {
	cmd := Command(git_dir, "git", "describe", "--all", "--tags", "--long", ref)
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

func gitCheckout(git_dir, checkout_dir, ref string) error {

	err := os.MkdirAll(checkout_dir, 0777)
	if err != nil {
		return err
	}

	log.Println("Populating", checkout_dir)

	args := []string{"--work-tree", checkout_dir, "checkout", ref, "--", "."}
	err = Command(git_dir, "git", args...).Run()
	if err != nil {
		return err
	}

	// Set mtimes to time file is most recently affected by a commit.
	// This is annoying but unfortunately git sets the timestamps to now,
	// and docker depends on the mtime for cache invalidation.
	err = gitSetMTimes(git_dir, checkout_dir, ref)
	if err != nil {
		return err
	}

	return nil
}

func gitSetMTimes(git_dir, checkout_dir, ref string) error {
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

	lsFiles := Command(git_dir, "git", "ls-tree", "-r", "--name-only", "-z", ref)
	lsFiles.Stdout = nil
	out, err := lsFiles.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %v", err)
	}

	dirMTimes := map[string]time.Time{}

	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	for _, file := range files {
		gitLog := Command(git_dir, "git", "log", "-1", "--date=rfc2822",
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

		err = os.Chtimes(checkout_dir+"/"+file, mTime, mTime)
		if err != nil {
			return fmt.Errorf("chtimes: %v", err)
		}

		// fmt.Printf("%s: %s\n", file, mTime)
	}

	for dir, mTime := range dirMTimes {
		err = os.Chtimes(checkout_dir+"/"+dir, mTime, mTime)
		if err != nil {
			return fmt.Errorf("chtimes: %v", err)
		}
		// fmt.Printf("%s: %s\n", dir, mTime)
	}
	return nil
}

func PrepBuildDirectory(
	remote, ref string,
) (buildDir, buildName string, cleanup func()) {

	if strings.HasPrefix(remote, "github.com/") {
		remote = "https://" + remote
	}

	wd, err := os.Getwd()
	if err != nil {
		log.Println("Failed to obtain working directory")
		return
	}

	gitDir := path.Join(wd, "src")

	gitLocalMirror(remote, gitDir, os.Stderr)

	rev, err := gitRevParse(gitDir, ref)
	if err != nil {
		log.Printf("Unable to parse rev: %v", err)
		return
	}

	shortRev := rev[:10]
	checkoutPath := path.Join(gitDir, "c/"+shortRev)

	err = gitCheckout(gitDir, checkoutPath, rev)
	if err != nil {
		log.Printf("Failed to checkout: %v", err)
		return
	}

	tagName, err := gitDescribe(gitDir, rev)
	if err != nil {
		log.Printf("Unable to describe %v: %v", rev, err)
		return
	}

	cleanup = func() {
		err := SafeCleanup(checkoutPath)
		if err != nil {
			log.Println("Error cleaning up path: ", checkoutPath)
		}
	}

	return checkoutPath, tagName, cleanup
}

func SafeCleanup(path string) error {
	if path == "/" || path == "" || path == "." || strings.Contains(path, "..") {
		return fmt.Errorf("invalid path specified for deletion %q", path)
	}
	return os.RemoveAll(path)
}
