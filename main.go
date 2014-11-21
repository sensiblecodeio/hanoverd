// Copyright 2014 The Hanoverd Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/docker/docker/opts"
	"github.com/docker/docker/pkg/mflag"
	"github.com/fsouza/go-dockerclient"
	"github.com/pwaller/barrier"
)

// DockerErrorStatus returns the HTTP status code represented by `err` or Status
// OK if no error or 0 if err != nil and is not a docker error.
func DockerErrorStatus(err error) int {
	if err, ok := err.(*docker.Error); ok {
		return err.Status
	}
	if err == nil {
		return http.StatusOK
	}
	return 0
}

type Options struct {
	env, publish opts.ListOpts
}

type UpdateEvent struct {
	Source        ContainerSource
	OutputStream  io.Writer
	BuildComplete chan<- struct{}
}

func main() {

	options := Options{
		env:     opts.NewListOpts(nil),
		publish: opts.NewListOpts(nil),
	}
	mflag.Var(&options.env, []string{"e", "-env"}, "Set environment variables")
	mflag.Var(&options.publish, []string{"p", "-publish"}, "Publish a container's port to the host")

	mflag.Parse()

	log.Println("Hanoverd")

	var wg sync.WaitGroup
	defer wg.Wait()

	// Fired when we're signalled to exit
	var dying barrier.Barrier
	defer dying.Fall()

	go func() {
		defer dying.Fall()
		// Await Stdin closure, don't care about errors
		_, _ = io.Copy(ioutil.Discard, os.Stdin)
	}()

	events := make(chan UpdateEvent)

	// SIGINT handler
	go func() {
		sig := make(chan os.Signal)
		signal.Notify(sig, os.Interrupt)
		for _ = range sig {
			// For now, SIGINT always means build the working dir.
			events <- UpdateEvent{Source: ContainerSource{Type: BuildCwd}}
		}
	}()

	go loop(&wg, &dying, options, events)
	go httpInterface(events)

	<-dying.Barrier()
}

func makeEnv(opt opts.ListOpts) []string {
	var env []string
	for _, envVar := range opt.GetAll() {
		if strings.Contains(envVar, "=") {
			env = append(env, envVar)
		} else {
			env = append(env, fmt.Sprint(envVar, "=", os.Getenv(envVar)))
		}
	}
	return env
}

func httpInterface(events chan<- UpdateEvent) {
	http.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {

		buildComplete := make(chan struct{})
		defer func() { <-buildComplete }()
		event := UpdateEvent{
			OutputStream:  w,
			BuildComplete: buildComplete,
		}

		switch r.Method {
		default:
			fmt.Fprintln(w, "Signal build $PWD")
		}

		events <- event
	})
	http.ListenAndServe("localhost:9123", nil)
}

func loop(wg *sync.WaitGroup, dying *barrier.Barrier, options Options, events <-chan UpdateEvent) {
	docker_host := os.Getenv("DOCKER_HOST")
	if docker_host == "" {
		docker_host = "unix:///run/docker.sock"
	}

	docker_tls_verify := os.Getenv("DOCKER_TLS_VERIFY")
	docker_tls_verify_bool := false
	if docker_tls_verify != "" {
		docker_tls_verify_bool = true
	}

	var client *docker.Client
	var err error
	if docker_tls_verify_bool {
		docker_cert_path := os.Getenv("DOCKER_CERT_PATH")
		docker_cert := docker_cert_path + "/cert.pem"
		docker_key := docker_cert_path + "/key.pem"
		// TODO there's no environment variable option in docker client for
		// this, it's called -tlscacert in its command line. We'll leave it
		// as the default (no CA, just trust) which boot2docker uses.
		docker_ca := ""
		client, err = docker.NewTLSClient(docker_host, docker_cert, docker_key, docker_ca)
	} else {
		client, err = docker.NewClient(docker_host)
	}
	if err != nil {
		dying.Fall()
		log.Println("Connecting to Docker failed:", err)
		return
	}

	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	baseName := path.Base(wd)

	var i uint64
	getName := func() string {
		n := atomic.AddUint64(&i, 1)
		return fmt.Sprint(baseName, "_", n)
	}

	env := makeEnv(options.env)

	var liveMutex sync.Mutex
	var live *Container

	lastEvent := UpdateEvent{Source: ContainerSource{Type: BuildCwd}}

	for {

		c := NewContainer(client, getName(), wg)
		c.Env = env

		// Global exit should cause container exit
		dying.Forward(&c.Closing)

		wg.Add(1)
		go func(c *Container) {
			defer wg.Done()

			go func() {
				for err := range c.Errors {
					log.Println("BUG: Async container error:", err)
					// TODO(pwaller): If this case is hit we might not want to
					// tear the container down really.
					c.Failed.Fall()
				}
			}()

			status, err := c.Run(lastEvent)
			if err != nil {
				log.Println("Container run failed:", strings.TrimSpace(err.Error()))
				c.Failed.Fall()
				return
			}
			log.Println(c.Name, "exit:", status)
		}(c)

		go func(c *Container) {

			log.Println("Awaiting container fate:", c.Name)
			select {
			case <-c.Failed.Barrier():
				log.Println("Container failed before going live:", c.Name)
				c.Closing.Fall()
				return
			case <-c.Superceded.Barrier():
				log.Println("Container superceded before going live:", c.Name)
				c.Closing.Fall()
				return
			case <-c.Closing.Barrier():
				log.Println("Container closed before going live:", c.Name)
				log.Println("(This should never happen?)")
				return

			case <-c.Ready.Barrier():
			}

			log.Println("Container going live:", c.Name)

			liveMutex.Lock()
			defer liveMutex.Unlock()
			previousLive := live

			// Block main exit until the firewall rule has been removed
			wg.Add(1)

			target := c.container.NetworkSettings.PortMappingAPI()[0].PublicPort
			remove, err := ConfigureRedirect(5555, target)
			if err != nil {
				// Firewall rule didn't get applied.
				log.Println("Firewall rule application failed:", err)
				wg.Done()
				c.err(err)
				return
			}

			live = c
			if previousLive != nil {
				previousLive.Closing.Fall()
			}

			// Networking
			go func() {
				defer wg.Done()

				<-c.Closing.Barrier()
				remove()
			}()
		}(c)

		lastEvent = <-events

		c.Superceded.Fall()

		log.Println("Signalled!")
	}
}
