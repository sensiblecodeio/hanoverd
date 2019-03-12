package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/pwaller/barrier"

	"github.com/sensiblecodeio/hanoverd/pkg/source"
)

type Container struct {
	Name      string
	ImageName string
	Args, Env []string
	Volumes   []string
	Mounts    []mount.Mount
	StatusURI string

	client        *docker.Client
	containerID   string
	containerInfo types.ContainerJSON

	Failed, Superceded, Obtained, Ready, Closing barrier.Barrier

	wg *sync.WaitGroup

	Errors  <-chan error
	errorsW chan<- error
}

// Construct a *Container. When the `wg` WaitGroup is zero, there is nothing
// outstanding (such as firewall rules which need garbage collecting).
func NewContainer(client *docker.Client, name string, wg *sync.WaitGroup) *Container {

	errors := make(chan error)

	c := &Container{
		Name:    name,
		client:  client,
		wg:      wg,
		Errors:  errors,
		errorsW: errors,
	}

	// If the container fails we should assume it should be torn down.
	c.Failed.Forward(&c.Closing)

	return c
}

func makeVolumeSet(in []string) map[string]struct{} {
	volumes := map[string]struct{}{}
	for _, v := range in {
		if strings.Contains(v, ":") {
			continue
		}
		volumes[v] = struct{}{}
	}
	return volumes
}

func makeBinds(in []string) []string {
	binds := []string{}
	for _, v := range in {
		if !strings.Contains(v, ":") {
			continue
		}
		binds = append(binds, v)
	}
	return binds
}

// `docker create` the container.
func (c *Container) Create(imageName string) error {
	// Inject internal environment variables
	imageRepo, imageTagDigest := imageRef(imageName)
	internalEnv := []string{
		"HANOVERD_IMAGE=" + imageName,
		"HANOVERD_IMAGE_REPO=" + imageRepo,
		"HANOVERD_IMAGE_TAGDIGEST=" + imageTagDigest,
	}

	resp, err := c.client.ContainerCreate(
		context.TODO(),
		&container.Config{
			Hostname:     c.Name,
			AttachStdout: true,
			AttachStderr: true,
			Env:          append(internalEnv, c.Env...),
			Cmd:          c.Args,
			Image:        imageName,
			Volumes:      makeVolumeSet(c.Volumes),
			Labels: map[string]string{
				"orchestrator":  "hanoverd",
				"hanoverd-name": c.Name,
			},
		},
		&container.HostConfig{
			PublishAllPorts: true,
			Binds:           makeBinds(c.Volumes),
			AutoRemove:      true,
			Mounts:          c.Mounts,
		},
		&network.NetworkingConfig{},
		"",
	)

	c.containerID = resp.ID

	return err
}

// CopyOutput copies the output of the container to `w` and blocks until
// completion
func (c *Container) CopyOutput() error {

	body, err := c.client.ContainerAttach(
		context.TODO(),
		c.containerID,
		types.ContainerAttachOptions{
			Stdout: true,
			Stderr: true,
			Logs:   true, // Capture messages from process start.
			Stream: true, // Attach to receive messages thereafter.
		},
	)
	if err != nil {
		return err
	}
	defer body.Close()

	w := os.Stderr
	// Note: buffered reads, but buffered reads are not as block-y as buffered
	//       writes so it's OK, it just makes it more efficient.
	_, err = stdcopy.StdCopy(w, w, body.Reader)
	return err
}

// AwaitListening polls for the program inside the container being ready to accept
// connections.
// Returns `true` for success and `false` for failure.
func (c *Container) AwaitListening() error {

	if len(c.containerInfo.NetworkSettings.Ports) == 0 {
		return fmt.Errorf("no ports are exposed (specify EXPOSE in Dockerfile)")
	}

	const (
		DefaultTimeout = 5 * time.Minute
		PollFrequency  = 5 // approx. times per second.
	)

	success := make(chan chan struct{})
	finished := make(chan struct{})
	defer close(finished)

	// Poll the statusURL once.
	// Returns true if polling should continue and false otherwise.
	poll := func(statusURL string) bool {
		req, err := http.NewRequest("GET", statusURL, nil)
		if err != nil {
			log.Printf("Warning, malformed URL: %q: %v", statusURL, err)
			return false
		}
		req.Cancel = finished

		resp, err := http.DefaultClient.Do(req)
		if resp != nil && resp.Body != nil {
			// Don't care about the body, make sure we close it.
			resp.Body.Close()
		}

		if urlErr, ok := err.(*url.Error); ok {
			errStr := urlErr.Err.Error()
			if strings.Contains(errStr, "malformed HTTP response") {
				// Seen in case endpoint doesn't speak HTTP. Give up.
				return false
			}
		}

		if resp == nil {
			// Keep going, connection probably failed.
			return true
		}
		switch resp.StatusCode {
		case http.StatusOK:
			// Protocol: poll() must not return before success
			// has been acknowledged, otherwise we may hit
			// noPollersRemain.
			response := make(chan struct{})
			select {
			case success <- response:
				<-response
			case <-finished:
				// Something else caused success/failure,
				// we'll never be able to communicate success.
			}
			return false

		default:
			log.Printf("Status poller got non-200 status: %q returned %v",
				statusURL, resp.Status)
			return false
		}

		return true
	}

	var pollers sync.WaitGroup

	// Start one poller per exposed port.
	for _, portMaps := range c.containerInfo.NetworkSettings.Ports {
		port := portMaps[0] // take the first public port
		statusURL := fmt.Sprint("http://", port.HostIP, ":", port.HostPort, c.StatusURI)

		c.wg.Add(1)
		pollers.Add(1)
		go func() {
			defer c.wg.Done()
			defer pollers.Done()

			// Poll until:
			// * 200 status code
			// * malformed response
			// * teardown
			for poll(statusURL) {
				select {
				case <-finished:
					return
				case <-time.After(time.Second / PollFrequency):
				}
			}
		}()
	}

	noPollersRemain := make(chan struct{})
	go func() {
		defer close(noPollersRemain)
		pollers.Wait()
	}()

	select {
	case ack := <-success:
		ack <- struct{}{}
		return nil

	case <-noPollersRemain:
		return fmt.Errorf("no status checks succeeded")

	case <-c.Closing.Barrier():
		return fmt.Errorf("shutting down")

	case <-time.After(DefaultTimeout):
		return fmt.Errorf("took longer than %v to start, giving up", DefaultTimeout)
	}
}

// Given an internal port, return the port mapped by docker, if there is one.
func (c *Container) MappedPort(internal int) (int, bool) {
	for privatePort, mappedPorts := range c.containerInfo.NetworkSettings.Ports {
		if privatePort.Int() == internal {
			for _, port := range mappedPorts {
				var portInt int
				_, err := fmt.Sscan(port.HostPort, &portInt)
				if err != nil {
					log.Printf("Failed to parse port %q", port.HostPort)
				} else {
					return portInt, true
				}
			}
		}
	}
	return -1, false
}

// Start the container (and notify it if c.Closing falls)
func (c *Container) Start() error {

	ctx := context.TODO()

	err := c.client.ContainerStart(ctx, c.containerID, types.ContainerStartOptions{})
	if err != nil {
		return err
	}

	// Load container.NetworkSettings
	c.containerInfo, err = c.client.ContainerInspect(ctx, c.containerID)
	if err != nil {
		return err
	}

	// Listen on the Closing barrier and send a kill to the container if it
	// falls.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()

		<-c.Closing.Barrier()
		// If the container is signaled to close, send a kill signal
		err := c.client.ContainerKill(ctx, c.containerID, "")
		if err == nil {
			return
		}
		switch err := err.(type) {
		default:
			t := fmt.Sprintf("%T", err)
			log.Println("Killing container failed:", c.containerID, t, err)
		}
	}()
	return nil
}

// Wait until container exits
func (c *Container) Wait() (int64, error) {
	waitBodyC, errC := c.client.ContainerWait(context.TODO(), c.containerID, container.WaitConditionNextExit)
	select {
	case err := <-errC:
		return -1, err

	case waitBody := <-waitBodyC:
		if waitBody.Error != nil && waitBody.Error.Message != "" {
			return -1, fmt.Errorf("containerWait: %v", waitBody.Error.Message)
		}
		return waitBody.StatusCode, nil
	}
}

// Internal function for raising an error.
func (c *Container) err(err error) {
	c.errorsW <- err
	c.Closing.Fall()
}

// Manage the whole lifecycle of the container in response to a request to
// start it.
func (c *Container) Run(imageSource source.ImageSource, payload []byte) (int64, error) {

	defer c.Closing.Fall()
	defer close(c.errorsW)

	go func() {
		for err := range c.Errors {
			log.Println("BUG: Async container error:", err)
			// TODO(pwaller): If this case is hit we might not want to
			// tear the container down really.
			c.Failed.Fall()
		}
	}()

	imageName, err := imageSource.Obtain(c.client, payload)
	c.Obtained.Fall()
	if err != nil {
		c.Failed.Fall()
		return -2, err
	}

	err = c.Create(imageName)
	if err != nil {
		c.Failed.Fall()
		return -1, err
	}

	err = c.Start()
	if err != nil {
		c.Delete() // Only attempt to delete if start fails, otherwise handled by AutoRemove.
		c.Failed.Fall()
		return -1, err
	}

	// Must come after container start has succeeded, otherwise we end up
	// perpetually attached if it fails to succeed, which blocks program exit.
	// Program exit must be blocked ordinarily until this completes so that
	// if we are quitting we see all of the messages sent by the container
	// until it quit.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()

		err := c.CopyOutput()
		if err != nil {
			c.err(err)
		}
	}()

	go func() {
		if err := c.AwaitListening(); err != nil {
			log.Printf("AwaitListening failed: %v", err)
			c.Failed.Fall()
			return
		}
		c.Ready.Fall()
	}()

	return c.Wait()
}

func (c *Container) Delete() {
	err := c.client.ContainerRemove(context.TODO(), c.containerID, types.ContainerRemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	})
	if err != nil {
		log.Println("Warn: failed to delete container:", err)
	}
}

var imageRefRepoPattern = regexp.MustCompile(`^(.*/.*)[:@](.*)$`)
var imageRefNamePattern = regexp.MustCompile(`^(.*)[:@](.*)$`)

func imageRef(imageName string) (name string, tagDigest string) {
	if strings.Count(imageName, "/") >= 1 {
		parts := imageRefRepoPattern.FindAllStringSubmatch(imageName, -1)
		if len(parts) == 0 {
			return imageName, "latest"
		}
		return parts[0][1], parts[0][2]
	}

	parts := imageRefNamePattern.FindAllStringSubmatch(imageName, -1)
	if len(parts) == 0 {
		return imageName, "latest"
	}

	return parts[0][1], parts[0][2]
}
