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

func NewContainer(client *docker.Client, name string, wg *sync.WaitGroup) *Container {
	errors := make(chan error)

	return &Container{
		Name:    name,
		client:  client,
		wg:      wg,
		Errors:  errors,
		errorsW: errors,
	}
}

func (c *Container) Create() error {
	opts := docker.CreateContainerOptions{
		Name: c.Name,
		Config: &docker.Config{
			Image: "base",
			Cmd:   []string{"bash", "-c", onelineweb},

			Hostname:     "container",
			ExposedPorts: map[docker.Port]struct{}{"8000/tcp": struct{}{}},
			AttachStdout: true,
			AttachStderr: true,
		},
	}

	var err error
	c.container, err = c.client.CreateContainer(opts)
	switch DockerErrorStatus(err) {
	default:
		fallthrough
	case 0:
		return err
	case http.StatusConflict:
		log.Fatalln("Container already exists. Aborting.")
	case http.StatusOK:
		break
	}

	return nil
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
		// Bind mounts
		// Binds: []string{""},
		PortBindings: map[docker.Port][]docker.PortBinding{
			"8000/tcp": []docker.PortBinding{
				docker.PortBinding{HostIP: "0.0.0.0"},
			},
		},
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

	go func() {
		<-c.Closing.Barrier()
		// If the container is signaled to close, send a kill signal
		c.client.KillContainer(docker.KillContainerOptions{
			ID: c.container.ID,
		})
	}()
	return nil
}

func (c *Container) Wait() (int, error) {
	return c.client.WaitContainer(c.container.ID)
}

func (c *Container) Run() (int, error) {

	err := c.Create()
	defer c.Delete()

	if err != nil {
		return -1, err
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		err := c.CopyOutput()
		if err != nil {
			c.errorsW <- err
		}
	}()

	err = c.Start()
	if err != nil {
		return -1, err
	}

	go func() {
		c.AwaitListening()
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

const onelineweb = "sleep 0.05; while true; do { printf 'HTTP/1.1 200 OK\r\n\r\n'; printf 'hello world\r\n'; } | nc -l 8000; done"
