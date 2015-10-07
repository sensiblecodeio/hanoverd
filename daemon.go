package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/fsouza/go-dockerclient"
	"github.com/scraperwiki/hanoverd/builder/git"
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

		err = git.Checkout(path, checkoutPath, rev)
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
