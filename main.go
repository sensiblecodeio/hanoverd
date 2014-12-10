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

	"github.com/docker/docker/nat"
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
	env, publish  opts.ListOpts
	source        ContainerSource
	containerArgs []string
	ports         nat.PortSet
	portBindings  nat.PortMap
}

type UpdateEvent struct {
	Source        ContainerSource
	OutputStream  io.Writer
	BuildComplete chan<- struct{}
}

func main() {
	var err error

	options := Options{
		env:     opts.NewListOpts(nil),
		publish: opts.NewListOpts(nil),
	}
	mflag.Var(&options.env, []string{"e", "-env"}, "Set environment variables")
	mflag.Var(&options.publish, []string{"p", "-publish"}, "Publish a container's port to the host")

	mflag.Parse()

	l := mflag.NArg()
	if l == 0 {
		options.source.Type = BuildCwd
	} else {
		args := mflag.Args()
		// If the first arg is "@", then use the Cwd
		if args[0] == "@" {
			options.source.Type = BuildCwd
		} else {
			options.source.Type = DockerPull
			options.source.dockerImageName = args[0]
		}
		args = args[1:]
		options.containerArgs = args
	}

	if err := CheckIPTables(); err != nil {
		log.Fatal("Unable to run `iptables -L`, see README (", err, ")")
	}

	options.ports, options.portBindings, err = nat.ParsePortSpecs(options.publish.GetAll())
	if err != nil {
		log.Fatalln("--publish:", err)
	}

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

	events := make(chan UpdateEvent, 1)
	originalEvent := UpdateEvent{Source: options.source}
	events <- originalEvent

	// SIGINT handler
	go func() {
		sig := make(chan os.Signal)
		signal.Notify(sig, os.Interrupt)
		for _ = range sig {
			// For now, SIGINT always means build the working dir.
			events <- originalEvent
		}
	}()

	go loop(&wg, &dying, options, events)
	go httpInterface(events)

	<-dying.Barrier()
}

// Make an env []string from a list of options specified on the cmdline.
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
		event := UpdateEvent{
			OutputStream:  NewFlushWriter(w),
			BuildComplete: buildComplete,
		}

		switch r.Method {
		case "POST":
			var (
				reader io.Reader
				err    error
			)

			switch r.Header.Get("Content-Type") {
			default:
				const msg = "Unrecognized Content-Type. Should be application/zip or application/x-tar (or x-bzip2 or gzip)"
				http.Error(w, msg, http.StatusBadRequest)
				return
			case "application/zip":
				reader, err = zip2tar(r.Body)
				if err != nil {
					const msg = "Problem whilst reading zip input"
					http.Error(w, msg, http.StatusBadRequest)
					return
				}

			case "application/x-bzip2", "application/x-tar":
				reader = r.Body
			}

			event.Source = ContainerSource{
				Type:                BuildTarballContent,
				buildTarballContent: reader,
			}
		default:
			fmt.Fprintln(w, "Signal build $PWD")
		}

		events <- event
		// Only wait on buildComplete when we know we got as far as requesting
		// the build.
		<-buildComplete
	})
	http.ListenAndServe("localhost:9123", nil)
}

func dockerConnect() (*docker.Client, error) {
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
	return client, err
}

// Main loop managing the lifecycle of all containers.
func loop(wg *sync.WaitGroup, dying *barrier.Barrier, options Options, events <-chan UpdateEvent) {
	client, err := dockerConnect()
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

	lastEvent := <-events

	for {

		c := NewContainer(client, getName(), wg)
		c.Args = options.containerArgs
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
			log.Println("container", c.Name, "quit, exit status:", status)
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
				return

			case <-c.Ready.Barrier():
			}

			log.Println("Container going live:", c.Name)

			liveMutex.Lock()
			defer liveMutex.Unlock()
			previousLive := live

			// Block main exit until the firewall rule has been placed
			// (and removed)
			wg.Add(1)
			defer wg.Done()

			isInternalPort := func(p int) bool {
				for _, m := range c.container.NetworkSettings.PortMappingAPI() {
					if m.PrivatePort == int64(p) {
						return true
					}
				}
				return false
			}

			removal := []func(){}

			defer func() {
				// Block main exit until firewall rule has been removed
				wg.Add(1)
				go func() {
					defer wg.Done()

					<-c.Closing.Barrier()
					for _, remove := range removal {
						remove()
					}
				}()
			}()

			for internalPort, bindings := range options.portBindings {
				if isInternalPort(internalPort.Int()) {
					for _, binding := range bindings {
						var public int64
						_, err := fmt.Sscan(binding.HostPort, &public)
						if err != nil {
							panic(err)
						}

						remove, err := ConfigureRedirect(public, internalPort.Int())
						if err != nil {
							// Firewall rule didn't get applied.
							log.Println("Firewall rule application failed:", err)
							c.err(err)
							return
						}

						removal = append(removal, remove)
					}
				} else {
					log.Println("Not a valid port!", internalPort)
				}
			}

			live = c
			if previousLive != nil {
				previousLive.Closing.Fall()
			}

		}(c)

		lastEvent = <-events

		c.Superceded.Fall()

		log.Println("Signalled!")
	}
}
