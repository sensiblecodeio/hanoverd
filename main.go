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
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/codegangsta/cli"
	"github.com/docker/docker/nat"
	"github.com/fsouza/go-dockerclient"
	"github.com/pwaller/barrier"
	"github.com/scraperwiki/hookbot/pkg/listen"
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
	env, publish  []string
	source        ContainerSource
	containerArgs []string
	ports         nat.PortSet
	portBindings  nat.PortMap
	statusURI     string
}

type UpdateEvent struct {
	// Source        ContainerSource
	Payload       []byte // input
	OutputStream  io.Writer
	Obtained      barrier.Barrier
	BuildComplete chan<- struct{}
}

// Determine if stdin is connected without blocking
func IsStdinReadable() bool {
	unix.SetNonblock(int(os.Stdin.Fd()), true)
	_, err := os.Stdin.Read([]byte{0})
	unix.SetNonblock(int(os.Stdin.Fd()), false)
	return err != io.EOF
}

func main() {
	app := cli.NewApp()

	app.Name = "hanoverd"
	app.Usage = "handover docker containers"

	app.Flags = []cli.Flag{
		cli.StringSliceFlag{
			Name:  "env, e",
			Usage: "environment variables to pass (reads from env if = omitted)",
			Value: &cli.StringSlice{},
		},
		cli.StringSliceFlag{
			Name:  "publish, p",
			Usage: "ports to publish (same syntax as docker)",
			Value: &cli.StringSlice{},
		},
		cli.StringFlag{
			Name:  "status-uri",
			Usage: "specify URI which returns 200 OK when functioning correctly",
			Value: "/",
		},
		cli.StringFlag{
			Name:  "hookbot",
			Usage: "url of hookbot websocket endpoint to monitor for updates",
		},
	}

	app.Action = ActionRun

	app.RunAndExitOnError()
}

func ActionRun(c *cli.Context) {
	var err error

	options := Options{}
	options.env = makeEnv(c.StringSlice("env"))
	options.statusURI = c.String("status-uri")

	containerName := "hanoverd"
	var imageSource ImageSource

	if c.GlobalIsSet("hookbot") {

		hookbotRe := regexp.MustCompile("/sub/github.com/repo/([^/]+)/([^/]+)/push/branch/([^/]+)")

		hookbotURLStr := c.GlobalString("hookbot")
		hookbotURL, err := url.Parse(hookbotURLStr)
		if err != nil {
			log.Fatalf("Hookbot URL %q does not parse: %v", hookbotURLStr, err)
		}

		if !hookbotRe.MatchString(hookbotURL.Path) {
			log.Fatalf("Hookbot URL path %q does not match %q", hookbotURL.Path, hookbotRe.String())
		}

		groups := hookbotRe.FindStringSubmatch(hookbotURL.Path)
		user, repository, branch := groups[1], groups[2], groups[3]

		containerName = repository
		options.containerArgs = c.Args()

		imageSource = &GithubSource{
			User:          user,
			Repository:    repository,
			InitialBranch: branch,
		}

		log.Printf("Hookbot monitoring %v@%v via %v", imageSource)

	} else if len(c.Args()) == 0 {
		options.source.Type = BuildCwd

	} else {
		first := c.Args().First()
		args := c.Args()[1:]

		if first == "@" {
			// If the first arg is "@", then use the Cwd
			options.source.Type = BuildCwd
		} else if first == "daemon" {
			RunDaemon()
		} else {
			options.source.Type = DockerPull
			options.source.dockerImageName = first
		}
		options.containerArgs = args
	}

	if err := CheckIPTables(); err != nil {
		log.Fatal("Unable to run `iptables -L`, see README (", err, ")")
	}

	options.ports, options.portBindings, err = nat.ParsePortSpecs(c.StringSlice("publish"))
	if err != nil {
		log.Fatalln("--publish:", err)
	}

	log.Println("Hanoverd")

	var wg sync.WaitGroup
	defer wg.Wait()

	// Fired when we're signalled to exit
	var dying barrier.Barrier
	defer dying.Fall()

	if IsStdinReadable() {
		log.Println("Press CTRL-D to exit")
		go func() {
			defer dying.Fall()
			defer log.Println("Stdin closed, exiting...")

			// Await Stdin closure, don't care about errors
			_, _ = io.Copy(ioutil.Discard, os.Stdin)
		}()
	}

	events := make(chan *UpdateEvent, 1)
	originalEvent := &UpdateEvent{}
	events <- originalEvent

	// SIGHUP handler
	go func() {
		sig := make(chan os.Signal)
		signal.Notify(sig, unix.SIGHUP)
		for value := range sig {
			log.Printf("Received signal %s", value)
			// Resend the original event
			events <- originalEvent
		}
	}()

	// SIGTERM, SIGINT handler
	go func() {
		defer dying.Fall()

		var value os.Signal

		defer log.Printf("Received signal %v", value)

		sig := make(chan os.Signal)
		signal.Notify(sig, unix.SIGTERM, unix.SIGINT)
		value = <-sig
	}()

	if c.GlobalIsSet("hookbot") {
		go MonitorHookbot(c.GlobalString("hookbot"), events)
	}

	go loop(containerName, imageSource, &wg, &dying, options, events)

	<-dying.Barrier()
}

func MonitorHookbot(target string, notify chan<- *UpdateEvent) {
	finish := make(chan struct{})
	header := http.Header{}

	events, errs := listen.RetryingWatch(target, header, finish)

	log.Println("Monitoring hookbot")

	for payload := range events {

		log.Printf("Signalled via hookbot")
		// data := map[string]string{}
		// err := json.Unmarshal(ev, &data)
		// if err != nil {
		// 	log.Printf("Failed to parse message: %v", err)
		// 	continue
		// }

		done := make(chan struct{})

		outBound := &UpdateEvent{}
		outBound.BuildComplete = done
		outBound.Payload = payload
		// outBound.Source.Type = GithubRepository
		// outBound.Source.githubURL = "github.com/scraperwiki/hookbot"
		// outBound.Source.githubRef = data["SHA"]

		notify <- outBound

		<-outBound.Obtained.Barrier()

		// <-done
		log.Printf("--- Build completed %v ---", "TODO(pwaller): Determine name from payload")
	}

	for err := range errs {
		log.Printf("Error in MonitorHookbot: %v", err)
	}
}

// Make an env []string from a list of options specified on the cmdline.
func makeEnv(opts []string) []string {
	var env []string
	for _, envVar := range opts {
		if strings.Contains(envVar, "=") {
			env = append(env, envVar)
		} else {
			env = append(env, fmt.Sprint(envVar, "=", os.Getenv(envVar)))
		}
	}
	return env
}

func dockerConnect() (*docker.Client, error) {
	docker_host := os.Getenv("DOCKER_HOST")
	if docker_host == "" {
		docker_host = "unix:///run/docker.sock"
	}

	docker_tls_verify := os.Getenv("DOCKER_TLS_VERIFY") != ""

	var (
		client *docker.Client
		err    error
	)
	if docker_tls_verify {
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
func loop(
	containerName string,
	imageSource ImageSource,
	wg *sync.WaitGroup,
	dying *barrier.Barrier,
	options Options,
	events <-chan *UpdateEvent,
) {
	client, err := dockerConnect()
	if err != nil {
		dying.Fall()
		log.Println("Connecting to Docker failed:", err)
		return
	}

	flips := make(chan *Container)
	go flipper(wg, options, flips)

	var i int

	for event := range events {

		name := fmt.Sprint(containerName, "_", i)
		i++

		log.Printf("New container starting: %q", name)

		c := NewContainer(client, name, wg)
		c.Args = options.containerArgs
		c.Env = options.env
		c.StatusURI = options.statusURI

		c.Obtained.Forward(&event.Obtained)

		// Global exit should cause container exit
		dying.Forward(&c.Closing)

		wg.Add(1)
		go func(c *Container) {
			defer wg.Done()

			status, err := c.Run(imageSource, event.Payload)
			if err != nil {
				log.Println("Container run failed:", strings.TrimSpace(err.Error()))
				return
			}
			log.Println("container", c.Name, "quit, exit status:", status)
		}(c)

		wg.Add(1)
		go func(c *Container) {
			defer wg.Done()

			log.Printf("Awaiting container fate: %q %q", c.Name, c.StatusURI)
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

			flips <- c
		}(c)
	}
}

// Manage firewall flips
func flipper(
	wg *sync.WaitGroup,
	options Options,
	newContainers <-chan *Container,
) {
	var live *Container

	for container := range newContainers {

		err := flip(wg, options, container)
		if err != nil {
			container.Failed.Fall()
			// Don't flip the firewall rules if there was a problem.
			continue
		}

		if live != nil {
			live.Closing.Fall()
		}

		live = container
	}
}

// Make container receive live traffic
func flip(wg *sync.WaitGroup, options Options, container *Container) error {

	removal := []func(){}

	defer func() {
		// Block main exit until firewall rule has been removed
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-container.Closing.Barrier()

			for _, remove := range removal {
				remove()
			}
		}()
	}()

	for internalPort, bindings := range options.portBindings {
		if mappedPort, ok := container.MappedPort(internalPort.Int()); ok {
			for _, binding := range bindings {
				var public int
				_, err := fmt.Sscan(binding.HostPort, &public)
				if err != nil {
					// If no public port specified, use same port as internal port
					public = internalPort.Int()
				}

				ipAddress := container.container.NetworkSettings.IPAddress
				remove, err := ConfigureRedirect(public, mappedPort, ipAddress)
				if err != nil {
					// Firewall rule didn't get applied.
					err := fmt.Errorf(
						"Firewall rule application failed: %q (public: %v, private: %v)",
						err, public, internalPort)
					container.err(err)
					return err
				}

				removal = append(removal, remove)
			}
		} else {
			err := fmt.Errorf("Docker image not exposing port %v!", internalPort)
			container.err(err)
			return err
		}
	}

	return nil
}
