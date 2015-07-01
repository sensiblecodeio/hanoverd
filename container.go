package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fsouza/go-dockerclient"
	"github.com/pwaller/barrier"
)

type Container struct {
	Name      string
	ImageName string
	Args, Env []string
	Volumes   []string
	StatusURI string

	client    *docker.Client
	container *docker.Container

	Failed, Superceded, Obtained, Ready, Closing barrier.Barrier

	wg *sync.WaitGroup

	Errors  <-chan error
	errorsW chan<- error
}

type SourceType int

const (
	// Run build with current directory with context
	BuildCwd            SourceType = iota
	BuildTarballContent            // Build with specified io.Reader as context
	BuildTarballURL                // Build with specified remote URL as context
	DockerPull                     // Run a docker pull to obtain the image
	GithubRepository               // build a github repository by making a local mirror
)

type ContainerSource struct {
	Type                SourceType
	buildTarballContent io.Reader
	buildDirectory      string
	buildTarballURL     string
	dockerImageName     string
	githubURL           string
	githubRef           string
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
	opts := docker.CreateContainerOptions{
		Config: &docker.Config{
			Hostname:     c.Name,
			AttachStdout: true,
			AttachStderr: true,
			Env:          c.Env,
			Cmd:          c.Args,
			Image:        imageName,
			Volumes:      makeVolumeSet(c.Volumes),
			Labels: map[string]string{
				"orchestrator":  "hanoverd",
				"hanoverd-name": c.Name,
			},
		},
	}

	var err error
	c.container, err = c.client.CreateContainer(opts)
	return err
}

// CopyOutput copies the output of the container to `w` and blocks until
// completion
func (c *Container) CopyOutput() error {

	// TODO(pwaller): at some point move this on to 'c' for configurability?
	w := os.Stderr
	// Blocks until stream closed
	return c.client.AttachToContainer(docker.AttachToContainerOptions{
		Container:    c.container.ID,
		OutputStream: w,
		ErrorStream:  w,
		Logs:         true,
		Stdout:       true,
		Stderr:       true,
		Stream:       true,
	})
}

// :todo(drj): May want to return errors for truly broken containers (timeout).
// Poll for the program inside the container being ready to accept connections
// Returns `true` for success and `false` for failure.
func (c *Container) AwaitListening() bool {

	const (
		DefaultTimeout = 5 * time.Minute
		PollFrequency  = 10 // times per second (via integer division of ns)
	)

	startDeadline := time.Now().Add(DefaultTimeout)

	for _, port := range c.container.NetworkSettings.PortMappingAPI() {
		url := fmt.Sprint("http://", port.IP, ":", port.PublicPort, c.StatusURI)
		for {
			response, err := http.Get(url)
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			if err == nil {
				switch response.StatusCode {
				case http.StatusOK:
					return true
				case http.StatusNotFound:
				default:
					log.Printf("Got non-200 status code: %v, giving up",
						response.StatusCode)
					c.Failed.Fall()
					return false
				}
			}

			if time.Now().After(startDeadline) {
				log.Printf("Took longer than %v to start, giving up", DefaultTimeout)
				return false
			}

			time.Sleep(time.Second / PollFrequency)

			select {
			case <-c.Closing.Barrier():
				// If the container has closed, cease waiting
				return false
			default:
			}
		}
	}

	return false
}

// Given an internal port, return the port mapped by docker, if there is one.
func (c *Container) MappedPort(internal int) (int, bool) {
	for _, m := range c.container.NetworkSettings.PortMappingAPI() {
		if int(m.PrivatePort) == internal {
			return int(m.PublicPort), true
		}
	}
	return -1, false
}

// Start the container (and notify it if c.Closing falls)
func (c *Container) Start() error {
	hc := &docker.HostConfig{
		PublishAllPorts: true,
		Binds:           makeBinds(c.Volumes),
	}
	err := c.client.StartContainer(c.container.ID, hc)
	if err != nil {
		return err
	}

	// Load container.NetworkSettings
	c.container, err = c.client.InspectContainer(c.container.ID)
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
		err := c.client.KillContainer(docker.KillContainerOptions{
			ID: c.container.ID,
		})
		if err == nil {
			return
		}
		switch err := err.(type) {
		case *docker.NoSuchContainer:
			// The container already went away, who cares.
			return
		default:
			log.Println("Killing container failed:", c.container.ID, err)
		}
	}()
	return nil
}

// Wait until container exits
func (c *Container) Wait() (int, error) {
	return c.client.WaitContainer(c.container.ID)
}

// Internal function for raising an error.
func (c *Container) err(err error) {
	c.errorsW <- err
	c.Closing.Fall()
}

// Manage the whole lifecycle of the container in response to a request to
// start it.
func (c *Container) Run(imageSource ImageSource, payload []byte) (int, error) {

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
	defer c.Delete()

	err = c.Start()
	if err != nil {
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
		if !c.AwaitListening() {
			c.Failed.Fall()
			return
		}
		c.Ready.Fall()
	}()

	return c.Wait()
}

func (c *Container) Delete() {
	err := c.client.RemoveContainer(docker.RemoveContainerOptions{
		ID:            c.container.ID,
		RemoveVolumes: true,
		Force:         true,
	})
	if err != nil {
		log.Println("Warn: failed to delete container:", err)
	}
}
