package source

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/fsouza/go-dockerclient"
	git "github.com/scraperwiki/git-prep-directory"
)

type ImageSource interface {
	// Build/pull/fetch a docker image and return its name as a string
	Obtain(client *docker.Client, payload []byte) (string, error)
}

type CwdSource struct{}

func (CwdSource) Name() (string, error) {
	name, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Base(name), nil
}

func (s *CwdSource) Obtain(c *docker.Client, payload []byte) (string, error) {
	imageName, err := s.Name()
	if err != nil {
		return "", err
	}
	err = DockerBuildDirectory(c, imageName, ".")
	return imageName, nil
}

type DockerPullSource struct {
	Repository, Tag string
}

func (s *DockerPullSource) Obtain(c *docker.Client, payload []byte) (string, error) {

	opts := docker.PullImageOptions{
		Repository:    s.Repository,
		Tag:           s.Tag,
		RawJSONStream: true,
	}

	// TODO(pwaller): Send the output somewhere better
	target := os.Stderr

	outputStream, errorC := PullProgressCopier(target)
	opts.OutputStream = outputStream

	// TODO(pwaller):
	// I don't use auth, just a daemon listening only on localhost,
	// so this remains unimplemented.
	var auth docker.AuthConfiguration
	err := c.PullImage(opts, auth)

	outputStream.Close()

	if err != nil {
		return "", err
	}

	imageName := fmt.Sprintf("%s:%s", s.Repository, s.Tag)
	return imageName, <-errorC

	// c.PullImage(opts)
	// return , nil
	// return "", fmt.Errorf("not implemented: DockerPullSource.Obtain(%v, %v)", s.Repository, s.Tag)
}

type GitHostSource struct {
	Host          string
	User          string
	Repository    string
	InitialBranch string
	// Directory in which to do `docker build`.
	// Uses repository root if blank.
	ImageRoot string
}

func (s *GitHostSource) CloneURL() string {
	format := "https://%s/%s/%s"
	if HaveSSHKey() {
		format = "ssh://git@%s/%s/%s"
	}

	return fmt.Sprintf(format, s.Host, s.User, s.Repository)
}

// Return the git SHA from the given hook payload, if we have a hook payload,
// otherwise return the InitialBranch.
func (s *GitHostSource) Ref(payload []byte) (string, error) {
	if len(payload) == 0 {
		return s.InitialBranch, nil
	}

	var v struct {
		SHA string
	}

	err := json.Unmarshal(payload, &v)
	if err != nil {
		return "", err
	}

	return v.SHA, nil
}

func (s *GitHostSource) Obtain(c *docker.Client, payload []byte) (string, error) {
	// Obtain/update local mirrorformat

	ref, err := s.Ref(payload)
	if err != nil {
		return "", err
	}

	gitDir, err := filepath.Abs(filepath.Join(".", "src", s.Host, s.User, s.Repository))
	if err != nil {
		return "", err
	}

	build, err := git.PrepBuildDirectory(gitDir, s.CloneURL(), ref)
	if err != nil {
		return "", err
	}
	defer build.Cleanup()

	dockerImage := fmt.Sprintf("%s:%s", s.Repository, build.Name)
	buildPath := filepath.Join(build.Dir, s.ImageRoot)

	err = DockerBuildDirectory(c, dockerImage, buildPath)
	if err != nil {
		return "", err
	}

	return dockerImage, nil
}

// Returns true if $HOME/.ssh exists, false otherwise
func HaveSSHKey() bool {
	keys := []string{"id_dsa", "id_ecdsa", "id_rsa", "id_ed25519"}
	for _, filename := range keys {
		path := os.ExpandEnv(filepath.Join("$HOME/.ssh", filename))
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func DockerBuildDirectory(c *docker.Client, name, path string) error {
	return c.BuildImage(docker.BuildImageOptions{
		Name:         name,
		ContextDir:   path,
		OutputStream: os.Stderr,
	})
}

func PullProgressCopier(target io.Writer) (io.WriteCloser, <-chan error) {
	reader, wrappedWriter := io.Pipe()
	errorC := make(chan error)
	go func() {
		finish := make(chan struct{})
		defer close(finish)
		defer close(errorC)

		mu := sync.Mutex{}
		lastMessage := jsonmessage.JSONMessage{}
		newMessage := false

		printMessage := func(m *jsonmessage.JSONMessage) {
			if m.ProgressMessage != "" {
				fmt.Fprintln(target, m.ID[:8], m.Status, m.ProgressMessage)
			} else if m.Progress != nil {
				fmt.Fprintln(target, m.ID[:8], m.Status, m.Progress.String())
			} else {
				m.Display(target, false)
			}
		}

		go func() {
			tick := time.Tick(1 * time.Second)
			for {
				select {
				case <-tick:
					mu.Lock()
					if newMessage {
						printMessage(&lastMessage)
						newMessage = false
					}
					mu.Unlock()

				case <-finish:
					return
				}
			}
		}()

		dec := json.NewDecoder(reader)
		for {
			tmp := jsonmessage.JSONMessage{}
			err := dec.Decode(&tmp)

			mu.Lock()
			if tmp.Error != nil || tmp.ErrorMessage != "" {
				tmp.Display(target, false)
				if tmp.Error != nil {
					errorC <- tmp.Error
				} else {
					errorC <- fmt.Errorf("%s", tmp.ErrorMessage)
				}
				return
			} else if tmp.Status != "Downloading" && tmp.Status != "Extracting" {
				printMessage(&tmp)
			} else {
				newMessage = true
				lastMessage = tmp
			}
			mu.Unlock()

			if err == io.EOF {
				return
			}
			if err != nil {
				log.Print("decode failure in", err)
				return
			}
		}
	}()
	return wrappedWriter, errorC
}
