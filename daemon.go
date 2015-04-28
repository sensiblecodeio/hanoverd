package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsouza/go-dockerclient"
)

// Invoke a `command` in `workdir` with `args`, connecting up its Stdout and Stderr
func Command(workdir, command string, args ...string) *exec.Cmd {
	log.Printf("wd = %s cmd = %s, args = %q", workdir, command, append([]string{}, args...))
	cmd := exec.Command(command, args...)
	cmd.Dir = workdir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

type DockerClient struct {
	underlying *docker.Client
}

func (c *DockerClient) Build(checkoutPath, name, tag string) error {
	return c.underlying.BuildImage(docker.BuildImageOptions{
		Name:         fmt.Sprintf("%s:%s", name, tag),
		ContextDir:   checkoutPath,
		OutputStream: os.Stderr,
	})
}

func (c *DockerClient) Tag(name, what, tag string) error {
	cmd := Command(".", "docker", "tag", what, tag)
	return cmd.Run()
}

func (c *DockerClient) Push(repo, name, tag string) error {
	opts := docker.PushImageOptions{
		Name:         repo + "/" + name,
		Tag:          tag,
		Registry:     repo,
		OutputStream: os.Stderr,
	}
	auth := docker.AuthConfiguration{}
	return c.underlying.PushImage(opts, auth)
}

func RunDaemon() {
	DaemonRoutes()

	log.Println("Listening on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal(err)
	}
}

func DaemonRoutes() {

	underlying, err := dockerConnect()
	if err != nil {
		log.Fatal("Unable to connect to client")
	}

	client := &DockerClient{underlying}

	http.HandleFunc("/build/", func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Path[len("/build/"):]

		name := path.Base(target)

		remote := fmt.Sprintf("https://%v.git", target)

		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = "HEAD"
		}

		if _, useSSH := r.URL.Query()["ssh"]; useSSH {
			split := strings.SplitN(target, "/", 2)
			domain, repo := split[0], split[1]
			remote = "git@" + domain + ":" + repo
		}

		path := "./src/" + target

		gitLocalMirror(remote, path, os.Stderr)

		rev, err := gitRevParse(path, ref)
		if err != nil {
			log.Printf("Unable to parse rev: %v", err)
			return
		}

		shortRev := rev[:10]

		checkoutPath := "c/" + shortRev

		err = gitCheckout(path, checkoutPath, rev)
		if err != nil {
			log.Printf("Failed to checkout: %v", err)
			return
		}

		tagName, err := gitDescribe(path, rev)
		if err != nil {
			log.Printf("Unable to describe %v: %v", rev, err)
			return
		}

		log.Println("Checked out")

		// dockerImage := name + "-" + shortRev

		repo := "localhost.localdomain:5000"

		fullName := repo + "/" + name

		err = client.Build(path+"/"+checkoutPath, fullName, tagName)
		if err != nil {
			log.Printf("Failed to build: %v", err)
			return
		}

		start := time.Now()

		err = client.Push(repo, name, tagName)
		log.Printf("Took %v to push", time.Since(start))

		if err != nil {
			log.Printf("Failed to push: %v", err)
			return
		}

		// var buf bytes.Buffer
		// start := time.Now()
		// dockerSave(dockerImage, &buf)
		// log.Printf("Took %v to save %v bytes", time.Since(start), buf.Len())

		fmt.Fprintln(w, "Success:", rev)
	})
}

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

		const timeout = 20 * time.Second
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

	// TODO(pwaller): this needs to be protected by a lock per git_dir,
	// since git-ls-files operates on the contents of the index :(

	err := os.MkdirAll(path.Join(git_dir, checkout_dir), 0777)
	if err != nil {
		return err
	}

	log.Println("Populating", checkout_dir)

	// Checkout without specifying a path to update the index
	// (required for ls-files to work correctly)
	args := []string{"--work-tree", checkout_dir, "checkout", ref}
	err = Command(git_dir, "git", args...).Run()
	if err != nil {
		return err
	}

	// Put the files physically in checkout_dir
	args = []string{"--work-tree", checkout_dir, "checkout", ref, "--", "."}
	err = Command(git_dir, "git", args...).Run()
	if err != nil {
		return err
	}

	wd := path.Join(git_dir, checkout_dir)

	args = []string{"--work-tree", ".", "submodule", "sync"}
	err = Command(wd, "git", args...).Run()
	if err != nil {
		return err
	}

	args = []string{"--work-tree", ".", "submodule", "update", "--init"}
	err = Command(wd, "git", args...).Run()
	if err != nil {
		return err
	}

	// Set mtimes to time file is most recently affected by a commit.
	// This is annoying but unfortunately git sets the timestamps to now,
	// and docker depends on the mtime for cache invalidation.
	err = gitSetMTimes(git_dir, git_dir+"/"+checkout_dir, ref)
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

	lsFiles := Command(git_dir, "git", "ls-tree", "-z", "-r", "--name-only", ref)
	lsFiles.Stdout = nil
	out, err := lsFiles.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %v", err)
	}

	dirMTimes := map[string]time.Time{}

	chtime := func(path string, atime, mtime time.Time) error {
		// https://github.com/torvalds/linux/blob/2decb2682f80759f631c8332f9a2a34a02150a03/include/uapi/linux/fcntl.h#L56
		const AT_FDCWD = -100

		var utimes [2]syscall.Timeval
		utimes[0] = syscall.NsecToTimeval(atime.UnixNano())
		utimes[1] = syscall.NsecToTimeval(mtime.UnixNano())

		if e := syscall.Futimesat(AT_FDCWD, path, utimes[0:]); e != nil {
			wd, err := os.Getwd()
			log.Println("WD:", wd, err)
			return &os.PathError{"futimesat", path, e}
		}
		return nil
	}

	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	for _, file := range files {
		gitLog := Command(git_dir, "git", "log", "-1", "--date=rfc2822",
			"--format=%cd", "--", file)
		gitLog.Stdout = nil

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

		err = chtime(checkout_dir+"/"+file, mTime, mTime)
		if err != nil {
			return fmt.Errorf("chtimes: %v", err)
		}

		fmt.Printf("%s: %s\n", file, mTime)
	}

	for dir, mTime := range dirMTimes {
		err = chtime(checkout_dir+"/"+dir, mTime, mTime)
		if err != nil {
			return fmt.Errorf("chtimes: %v", err)
		}
		fmt.Printf("%s: %s\n", dir, mTime)
	}
	return nil
}
