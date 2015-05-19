package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/fsouza/go-dockerclient"

	"github.com/scraperwiki/hanoverd/builder/git"
)

type ImageSource interface {
	// Build/pull/fetch a docker image and return its name as a string
	Obtain(client *docker.Client, payload []byte) (string, error)
}

type CwdSource struct {
}

func (s *CwdSource) Obtain(c *docker.Client, payload []byte) (string, error) {
	// `docker build pwd`
	return "", fmt.Errorf("not implemented: CwdSource.Obtain")
}

type RegistrySource struct {
	ImageName string // `localhost.localdomain:5000/image:tag
}

func (s *RegistrySource) Obtain(c *docker.Client, payload []byte) (string, error) {
	// docker pull s.ImageName
	return "", fmt.Errorf("not implemented: RegistrySource.Obtain")
	return s.ImageName, nil
}

type GithubSource struct {
	User, Repository, InitialBranch string
}

func (s *GithubSource) CloneURL() string {

	format := "https://github.com/%s/%s"
	if HaveDotSSH() {
		format = "ssh://git@github.com:%s/%s"
	}

	return fmt.Sprintf(format, s.User, s.Repository)
}

// Return the git SHA from the given hook payload, if we have a hook payload,
// otherwise return the InitialBranch.
func (s *GithubSource) Ref(payload []byte) (string, error) {
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

func (s *GithubSource) Obtain(c *docker.Client, payload []byte) (string, error) {
	// Obtain/update local mirrorformat

	ref, err := s.Ref(payload)

	build, err := git.PrepBuildDirectory(s.CloneURL(), ref)
	if err != nil {
		return "", nil
	}
	defer build.Cleanup()

	dockerImage := fmt.Sprintf("%s:%s", s.Repository, build.Name)

	err = DockerBuildDirectory(c, dockerImage, build.Dir)
	if err != nil {
		return "", err
	}

	return dockerImage, nil
}

// Returns true if $HOME/.ssh exists, false otherwise
func HaveDotSSH() bool {
	_, err := os.Stat(os.ExpandEnv("${HOME}/.ssh"))
	return err == nil
}

func DockerBuildDirectory(c *docker.Client, name, path string) error {
	return c.BuildImage(docker.BuildImageOptions{
		Name:         name,
		ContextDir:   path,
		OutputStream: os.Stderr,
	})
}

// Generate a docker image. This can be done through various mechanisms in
// response to an UpdateEvent (see SourceType constant declarations).
func (c *Container) Build(config UpdateEvent) error {
	// if config.BuildComplete != nil {
	// 	defer close(config.BuildComplete)
	// }

	// var err error
	// bo := docker.BuildImageOptions{}
	// bo.Name = c.Name
	// bo.OutputStream = config.OutputStream
	// if bo.OutputStream == nil {
	// 	bo.OutputStream = os.Stderr
	// }

	// switch config.Source.Type {
	// case GithubRepository:
	// 	bo.ContextDir = config.Source.buildDirectory
	// case BuildCwd:
	// 	bo.ContextDir, err = os.Getwd()
	// 	if err != nil {
	// 		return err
	// 	}
	// case BuildTarballContent:
	// 	bo.InputStream = config.Source.buildTarballContent
	// default:
	// 	return fmt.Errorf("Unimplemented ContainerSource: %v", config.Source.Type)
	// }

	// return c.client.BuildImage(bo)
	return nil
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
				log.Print("decode failure in  ", err)
				return
			}
		}
	}()
	return wrappedWriter, errorC
}

// Pull an image from a docker repository.
func (c *Container) Pull(config UpdateEvent) error {
	// if config.BuildComplete != nil {
	// 	defer close(config.BuildComplete)
	// }

	// parts := strings.SplitN(config.Source.dockerImageName, ":", 2)

	// pio := docker.PullImageOptions{}

	// pio.Repository = parts[0]
	// pio.Tag = "latest"
	// if len(parts) == 2 {
	// 	pio.Tag = parts[1]
	// }
	// pio.Registry = ""
	// pio.RawJSONStream = true

	// log.Println("Pulling", pio.Repository)

	// target := config.OutputStream
	// if target == nil {
	// 	target = os.Stderr
	// }

	// outputStream, errorC := PullProgressCopier(target)
	// pio.OutputStream = outputStream

	// pullImageErr := c.client.PullImage(pio, docker.AuthConfiguration{})

	// outputStream.Close()

	// if pullImageErr != nil {
	// 	return pullImageErr
	// }

	// return <-errorC
	return nil
}
