FROM golang:1.4

RUN apt-get update && apt-get install -y upx iptables

COPY ./github-host-key /etc/ssh/ssh_known_hosts

RUN go get github.com/pwaller/goupx \
		   github.com/skelterjohn/rerun

# Turn off cgo so that we end up with totally static binaries
ENV CGO_ENABLED 0

RUN go install -a -installsuffix=static std

RUN go get \
	-v -installsuffix=static \
	github.com/docker/docker/opts \
	github.com/Sirupsen/logrus \
	github.com/docker/libtrust \
	github.com/fsouza/go-dockerclient \
	github.com/pwaller/barrier

COPY . /go/src/github.com/scraperwiki/hanoverd

RUN go get -x -v -installsuffix=static \
		github.com/scraperwiki/hanoverd && \
	goupx /go/bin/hanoverd