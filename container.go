package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/fsouza/go-dockerclient"
	"github.com/pwaller/barrier"
)

type Container struct {
	Name string

	client    *docker.Client
	container *docker.Container

	Closing, Ready barrier.Barrier
	wg             *sync.WaitGroup

	Errors  <-chan error
	errorsW chan<- error
}

func NewContainer(client *docker.Client, name string, wg *sync.WaitGroup, dying *barrier.Barrier) *Container {

	errors := make(chan error)

	c := &Container{
		Name:    name,
		client:  client,
		wg:      wg,
		Errors:  errors,
		errorsW: errors,
	}

	// :TODO(drj,pwaller): refactor to put this forwarding idiom on the barrier.
	go func() {
		// Listen for close, and then kill the container
		<-dying.Barrier()
		c.Closing.Fall()
	}()

	return c
}

func (c *Container) Build() error {
	var err error
	bo := docker.BuildImageOptions{}
	bo.ContextDir, err = os.Getwd()
	if err != nil {
		return err
	}
	bo.OutputStream = os.Stderr
	bo.Name = c.Name
	return c.client.BuildImage(bo)
}

func (c *Container) Create() error {
	opts := docker.CreateContainerOptions{
		Name: c.Name,
		Config: &docker.Config{
			Hostname:     c.Name,
			Image:        c.Name,
			AttachStdout: true,
			AttachStderr: true,
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
func (c *Container) AwaitListening() {

	for _, port := range c.container.NetworkSettings.PortMappingAPI() {
		url := fmt.Sprint("http://", port.IP, ":", port.PublicPort, "/")
		for {
			response, err := http.Get(url)
			if err == nil && response.StatusCode == http.StatusOK {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	c.Ready.Fall()
}

func (c *Container) Start() error {
	hc := &docker.HostConfig{
		PublishAllPorts: true,
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

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		<-c.Closing.Barrier()
		// If the container is signaled to close, send a kill signal
		err := c.client.KillContainer(docker.KillContainerOptions{
			ID: c.container.ID,
		})
		if err != nil {
			log.Println("killing container", c.container.ID, err)
		}
	}()
	return nil
}

func (c *Container) Wait() (int, error) {
	return c.client.WaitContainer(c.container.ID)
}

func (c *Container) err(err error) {
	c.errorsW <- err
	c.Closing.Fall()
}

func (c *Container) Run() (int, error) {

	defer close(c.errorsW)

	err := c.Build()
	if err != nil {
		return -2, err
	}

	err = c.Create()
	if err != nil {
		return -1, err
	}
	defer c.Delete()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		err := c.CopyOutput()
		if err != nil {
			c.err(err)
		}
	}()

	err = c.Start()
	if err != nil {
		return -1, err
	}

	go func() {
		c.AwaitListening()
		c.Ready.Fall()
		log.Println("Listening on", c.container.NetworkSettings.PortMappingAPI())
	}()

	return c.Wait()
}

func (c *Container) Delete() {
	c.client.RemoveContainer(docker.RemoveContainerOptions{
		ID:            c.container.ID,
		RemoveVolumes: true,
		Force:         true,
	})
}
