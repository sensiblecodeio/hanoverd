package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/dockerignore"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/fileutils"
	"github.com/docker/docker/pkg/jsonmessage"
	git "github.com/sensiblecodeio/git-prep-directory"
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

	buildPath := "."
	err = DockerBuildDirectory(c, imageName, buildPath)
	if err != nil {
		return "", err
	}

	// Test for the presence of a 'runtime/Dockerfile' in the buildpath.
	// If it's there, then we run the image we just built, and use its
	// stdout as a build context
	if exists(filepath.Join(buildPath, "runtime", "Dockerfile")) {
		log.Printf("Generate runtime image")
		imageName, err = constructRuntime(c, imageName)
		if err != nil {
			return "", err
		}
	}

	return imageName, nil
}

type DockerPullSource struct {
	Repository, Tag string
}

var imageTag = regexp.MustCompile("^([^:]+):?(.*)$")

// DockerPullSourceFromImage creates a *DockerPullSource from an image name
// (with an optional tag)
func DockerPullSourceFromImage(image string) *DockerPullSource {
	parts := imageTag.FindStringSubmatch(image)
	if len(parts) != 2 {
		log.Panicf("imageTag regexp failed to match %q", image)
	}
	image, tag := parts[0], parts[1]
	return &DockerPullSource{image, tag}
}

// Obtain an image by pulling a docker image from somewhere.
func (s *DockerPullSource) Obtain(c *docker.Client, payload []byte) (string, error) {
	imageName := fmt.Sprintf("%s:%s", s.Repository, s.Tag)

	rc, err := c.ImagePull(context.TODO(), imageName, types.ImagePullOptions{})
	if err != nil {
		return "", err
	}
	defer rc.Close()

	err = jsonmessage.DisplayJSONMessagesStream(rc, os.Stderr, 0, false, nil)
	// _, err = io.Copy(os.Stderr, rc)
	if err != nil {
		return "", err
	}

	return imageName, nil
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

	build, err := git.PrepBuildDirectory(gitDir, s.CloneURL(), ref, 10*time.Minute, os.Stderr)
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

	// Test for the presence of a 'runtime/Dockerfile' in the buildpath.
	// If it's there, then we run the image we just built, and use its
	// stdout as a build context
	if exists(filepath.Join(buildPath, "runtime", "Dockerfile")) {
		dockerImage, err = constructRuntime(c, dockerImage)
		if err != nil {
			return "", err
		}
	}

	return dockerImage, nil
}

// constructRuntime builds an image from the standard output of another container.
func constructRuntime(c *docker.Client, dockerImage string) (string, error) {
	stdout, err := DockerRun(c, dockerImage)
	if err != nil {
		return "", fmt.Errorf("run buildtime image: %v", err)
	}

	imageName := dockerImage + "-runtime"

	resp, err := c.ImageBuild(context.TODO(), stdout, types.ImageBuildOptions{
		Tags: []string{imageName},
	})
	if err != nil {
		return "", err
	}

	_, err = io.Copy(os.Stderr, resp.Body)
	if err != nil {
		return "", err
	}

	return imageName, nil
}

func DockerRun(c *docker.Client, imageName string) (io.ReadCloser, error) {
	resp, err := c.ContainerCreate(
		context.TODO(),
		&container.Config{
			Hostname:     "generateruntimecontext",
			AttachStdout: true,
			AttachStderr: true,
			Image:        imageName,
			Labels: map[string]string{
				"orchestrator": "hanoverd",
				"purpose":      "Generate build context for runtime container",
			},
		},
		&container.HostConfig{},
		&network.NetworkingConfig{},
		"",
	)
	if err != nil {
		log.Printf("Create container... failed: %v", err)
		return nil, err
	}

	containerID := resp.ID
	if len(resp.Warnings) > 0 {
		for _, w := range resp.Warnings {
			log.Printf("ContainerCreate warning: %v", w)
		}
	}

	r, w := io.Pipe()
	attached := make(chan struct{})
	detached := make(chan struct{})

	go func() {
		defer close(detached)

		cr, err2 := c.ContainerAttach(
			context.TODO(),
			containerID,
			types.ContainerAttachOptions{
				Logs:   true,
				Stdout: true,
				Stderr: true,
				Stream: true,
			},
		)
		if err2 != nil {
			_ = w.CloseWithError(err2)
		}

		_, err2 = io.Copy(w, cr.Reader)
		_ = w.CloseWithError(err2)
	}()

	select {
	case <-detached:
		// attachment failed
		log.Printf("Attachment failed")
		return nil, fmt.Errorf("Attachment failed")
	case <-attached:
		attached <- struct{}{}
	}

	err = c.ContainerStart(context.TODO(), containerID, types.ContainerStartOptions{})
	if err != nil {
		log.Printf("Start container... failed: %v", err)
		return nil, err
	}

	removeContainer := func() {
		err2 := c.ContainerRemove(context.TODO(), containerID, types.ContainerRemoveOptions{
			RemoveVolumes: true,
			Force:         true,
		})
		if err2 != nil {
			log.Printf("Error removing intermediate container: %v", err2)
		}
	}

	return struct {
		io.Reader
		io.Closer
	}{
		Reader: r,
		Closer: CloseFunc(func() error {
			defer removeContainer()

			statusCode, err2 := c.ContainerWait(context.TODO(), containerID)
			if err2 != nil {
				return err2
			}
			if statusCode != 0 {
				return fmt.Errorf("non-zero exit status: %v", err)
			}
			return nil
		}),
	}, err
}

type CloseFunc func() error

func (fn CloseFunc) Close() error { return fn() }

func exists(filename string) bool {
	_, err := os.Stat(filename)
	switch {
	case err == nil:
		return true
	default:
		log.Printf("Error checking for the existence of %q: %v", filename, err)
	case os.IsNotExist(err):
	}
	return false
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
	buildCtx, err := contextFromDir(path)
	if err != nil {
		return err
	}
	resp, err := c.ImageBuild(
		context.TODO(),
		buildCtx,
		types.ImageBuildOptions{
			Tags: []string{name},
		},
	)
	if err != nil {
		return err
	}

	err = jsonmessage.DisplayJSONMessagesStream(resp.Body, os.Stderr, 0, false, nil)
	return err
}

// contextFromDir taken verbatim from docker/build to match logic.
func contextFromDir(contextDir string) (io.ReadCloser, error) {
	relDockerfile := "Dockerfile"
	// And canonicalize dockerfile name to a platform-independent one
	relDockerfile, err := archive.CanonicalTarNameForPath(relDockerfile)
	if err != nil {
		return nil, fmt.Errorf("cannot canonicalize dockerfile path %s: %v", relDockerfile, err)
	}

	f, err := os.Open(filepath.Join(contextDir, ".dockerignore"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	defer f.Close()

	var excludes []string
	if err == nil {
		excludes, err = dockerignore.ReadAll(f)
		if err != nil {
			return nil, err
		}
	}

	if err := builder.ValidateContextDirectory(contextDir, excludes); err != nil {
		return nil, fmt.Errorf("Error checking context: '%s'.", err)
	}

	// If .dockerignore mentions .dockerignore or the Dockerfile
	// then make sure we send both files over to the daemon
	// because Dockerfile is, obviously, needed no matter what, and
	// .dockerignore is needed to know if either one needs to be
	// removed. The daemon will remove them for us, if needed, after it
	// parses the Dockerfile. Ignore errors here, as they will have been
	// caught by validateContextDirectory above.
	var includes = []string{"."}
	keepThem1, _ := fileutils.Matches(".dockerignore", excludes)
	keepThem2, _ := fileutils.Matches(relDockerfile, excludes)
	if keepThem1 || keepThem2 {
		includes = append(includes, ".dockerignore", relDockerfile)
	}

	compression := archive.Uncompressed // likely on localhost.
	buildCtx, err := archive.TarWithOptions(contextDir, &archive.TarOptions{
		Compression:     compression,
		ExcludePatterns: excludes,
		IncludeFiles:    includes,
	})
	if err != nil {
		return nil, err
	}
	return buildCtx, nil
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
