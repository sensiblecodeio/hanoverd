package builder

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/codegangsta/cli"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/sensiblecodeio/hookbot/pkg/listen"

	"github.com/sensiblecodeio/hanoverd/pkg/source"
)

// Action is the codegangsta/cli action for running hanoverd in builder mode.
func Action(c *cli.Context) {
	_, imageSource, err := source.GetSourceFromHookbot(c.String("listen"))
	if err != nil {
		log.Fatalf("Failed to parse hookbot listen URL: %v", err)
	}

	repository, tag, err := ParsePubEndpoint(c.String("docker-notify"))
	if err != nil {
		log.Fatalf("Failed to parse hookbot notify URL: %v", err)
	}

	ref := repository
	if tag == "" {
		tag = "latest"
	}
	ref += ":" + tag

	registry, imageName := ParseRegistryImage(repository)
	log.Printf("Registry: %v, image: %v", registry, imageName)

	client, err := docker.NewEnvClient()
	if err != nil {
		log.Fatalf("Unable to connect to docker: %v", err)
	}

	hookbotURL := c.String("listen")
	// Remove the #anchor part of the URL, if specified.
	hookbotURL = strings.SplitN(hookbotURL, "#", 2)[0]

	finish := make(chan struct{})
	header := http.Header{}
	events, errs := listen.RetryingWatch(hookbotURL, header, finish)

	go func() {
		defer close(finish)

		for err := range errs {
			log.Printf("Error in hookbot event stream: %v", err)
		}

		log.Printf("Error stream finished")
	}()

	build := func() error {
		name, err2 := imageSource.Obtain(client, []byte{})
		if err2 != nil {
			return fmt.Errorf("obtain: %v", err2)
		}

		log.Printf("Tag image...")
		err2 = client.ImageTag(context.TODO(), name, ref)
		if err2 != nil {
			return fmt.Errorf("tagimage: %v", err2)
		}

		rc, err2 := client.ImagePush(context.TODO(), ref, types.ImagePushOptions{})
		if err2 != nil {
			return fmt.Errorf("pushimage: %v", err2)
		}
		defer rc.Close()

		err2 = jsonmessage.DisplayJSONMessagesStream(rc, os.Stderr, 0, false, nil)
		if err2 != nil {
			return err2
		}

		log.Printf("Notify docker endpoint...")
		resp, err2 := http.Post(c.String("docker-notify"), "text/plain", strings.NewReader("UPDATE\n"))
		if err2 != nil {
			return fmt.Errorf("notify hookbot of push: %v", err2)
		}
		log.Printf("Response from notify: %v", resp.StatusCode)
		return nil
	}

	err = build()
	if err != nil {
		log.Printf("Build failed: %v", err)
	}

	for _ = range events {
		log.Printf("Event!")

		err := build()
		if err != nil {
			log.Printf("Build failed: %v", err)
		}
	}

	log.Printf("Event stream finished")
}

func ParseRegistryImage(fullName string) (registry, repository string) {
	var (
		DotBeforeSlash = regexp.MustCompile("^[^/]+\\.[^/]+/")
	)

	if DotBeforeSlash.MatchString(fullName) {
		parts := strings.SplitN(fullName, "/", 2)
		return parts[0], parts[1]
	}

	// No registry specified
	return "", fullName
}

var hookbotDockerPullPub = regexp.MustCompile("^/pub/docker-pull/(.*)/tag/([^/]+)$")

func ParsePubEndpoint(hookbotURLStr string) (image, tag string, err error) {
	u, err := url.Parse(hookbotURLStr)
	if err != nil {
		return "", "", err
	}

	parts := hookbotDockerPullPub.FindStringSubmatch(u.Path)
	if parts == nil {
		return "", "", fmt.Errorf("Pub URL %q doesn't match: %q",
			u.Path, hookbotDockerPullPub.String())
	}

	return parts[1], parts[2], nil
}
