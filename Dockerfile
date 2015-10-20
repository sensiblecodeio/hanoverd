FROM golang:1.5

RUN apt-get update && apt-get install -y upx iptables

COPY ./github-host-key /etc/ssh/ssh_known_hosts

RUN go get github.com/pwaller/goupx

# Turn off cgo so that we end up with totally static binaries
ENV CGO_ENABLED=0 \
    GO15VENDOREXPERIMENT=1

RUN go install -a -installsuffix=static std

COPY ./vendor /go/src/github.com/scraperwiki/hanoverd/vendor/
COPY ./dependencies /go/src/github.com/scraperwiki/hanoverd/dependencies

RUN xargs go install -installsuffix=static -v < /go/src/github.com/scraperwiki/hanoverd/dependencies

COPY . /go/src/github.com/scraperwiki/hanoverd/

RUN go install -x -v -installsuffix=static \
                -ldflags "-X main.appVersion=`git --git-dir /go/src/github.com/scraperwiki/hanoverd/.git describe --abbrev=0 --tags`" \
		github.com/scraperwiki/hanoverd && \
	goupx /go/bin/hanoverd
