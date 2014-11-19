package main

import (
	"fmt"
	"io"
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
}

func NewContainer(client *docker.Client, name string, wg *sync.WaitGroup) *Container {
	return &Container{
		Name:   name,
		client: client,
		wg:     wg,
	}
}

func (c *Container) Create() {
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
		panic(err)
	case http.StatusConflict:
		log.Fatalln("Container already exists. Aborting.")
	case http.StatusOK:
		break
	}
}

// CopyOutput copies the output of the container to `w` and blocks until
// completion
func (c *Container) CopyOutput(w io.Writer) {
	// Blocks until stream closed
	err := c.client.AttachToContainer(docker.AttachToContainerOptions{
		Container:    c.container.ID,
		OutputStream: w,
		ErrorStream:  w,
		Logs:         true,
		Stdout:       true,
		Stderr:       true,
		Stream:       true,
	})
	if err != nil {
		log.Fatal(err)
	}
}

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

func (c *Container) Start() {
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
		panic(err)
	}

	// Load container.NetworkSettings
	c.container, err = c.client.InspectContainer(c.container.ID)
	if err != nil {
		panic(err)
	}

	go func() {
		<-c.Closing.Barrier()
		// If the container is signaled to close, send a kill signal
		c.client.KillContainer(docker.KillContainerOptions{
			ID: c.container.ID,
		})
	}()
}

func (c *Container) Wait() {

	w, err := c.client.WaitContainer(c.container.ID)
	if err != nil {
		panic(err)
	}

	log.Println("Exit status:", w)
}

func (c *Container) Run() {

	c.Create()
	defer c.Delete()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.CopyOutput(os.Stdout)
	}()

	c.Start()

	go func() {
		c.AwaitListening()
		log.Println("Listening on", c.container.NetworkSettings.PortMappingAPI())
	}()

	c.Wait()
}

func (c *Container) Delete() {
	c.client.RemoveContainer(docker.RemoveContainerOptions{
		ID:            c.container.ID,
		RemoveVolumes: true,
		Force:         true,
	})
}

const onelineweb = "sleep 0.05; while true; do { printf 'HTTP/1.1 200 OK\r\n\r\n'; printf 'hello world\r\n'; } | nc -l 8000; done"
