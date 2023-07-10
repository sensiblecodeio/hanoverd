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
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/moby/buildkit/frontend/dockerfile/dockerignore"
	"github.com/moby/patternmatcher"
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
		Remove: true,
		Tags:   []string{imageName},
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
		nil,
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

			waitBodyC, err2C := c.ContainerWait(context.TODO(), containerID, container.WaitConditionNotRunning)
			select {
			case err2 := <-err2C:
				return err2

			case waitBody := <-waitBodyC:
				if waitBody.Error != nil && waitBody.Error.Message != "" {
					return fmt.Errorf("containerWait: %v", waitBody.Error.Message)
				}
				if waitBody.StatusCode != 0 {
					return fmt.Errorf("non-zero exit status: %v", waitBody.StatusCode)
				}
				return nil
			}
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
			Remove: true,
			Tags:   []string{name},
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
	relDockerfile = archive.CanonicalTarNameForPath(relDockerfile)

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

	if err := validateContextDirectory(contextDir, excludes); err != nil {
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
	keepThem1, _ := patternmatcher.MatchesOrParentMatches(".dockerignore", excludes)
	keepThem2, _ := patternmatcher.MatchesOrParentMatches(relDockerfile, excludes)
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

// Copied verbatim from docker/cli/command/image/build.
// Licensed under apache 2 license available from.
// https://github.com/docker/cli/blob/master/LICENSE
//
// validateContextDirectory checks if all the contents of the directory
// can be read and returns an error if some files can't be read
// symlinks which point to non-existing files don't trigger an error
func validateContextDirectory(srcPath string, excludes []string) error {
	contextRoot, err := getContextRoot(srcPath)
	if err != nil {
		return err
	}
	return filepath.Walk(contextRoot, func(filePath string, f os.FileInfo, err error) error {
		if err != nil {
			if os.IsPermission(err) {
				return fmt.Errorf("can't stat '%s'", filePath)
			}
			if os.IsNotExist(err) {
				return fmt.Errorf("file ('%s') not found or excluded by .dockerignore", filePath)
			}
			return err
		}

		// skip this directory/file if it's not in the path, it won't get added to the context
		if relFilePath, err := filepath.Rel(contextRoot, filePath); err != nil {
			return err
		} else if skip, err := patternmatcher.MatchesOrParentMatches(relFilePath, excludes); err != nil {
			return err
		} else if skip {
			if f.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// skip checking if symlinks point to non-existing files, such symlinks can be useful
		// also skip named pipes, because they hanging on open
		if f.Mode()&(os.ModeSymlink|os.ModeNamedPipe) != 0 {
			return nil
		}

		if !f.IsDir() {
			currentFile, err := os.Open(filePath)
			if err != nil && os.IsPermission(err) {
				return fmt.Errorf("no permission to read from '%s'", filePath)
			}
			currentFile.Close()
		}
		return nil
	})
}

func getContextRoot(srcPath string) (string, error) {
	return filepath.Join(srcPath, "."), nil
}
