all: hanoverd

hanoverd: FORCE
	go generate
	docker build -t hanoverd-buildtime .
	docker run --rm hanoverd-buildtime cat /go/bin/hanoverd > ./hanoverd
	chmod +x ./hanoverd

iptables:
	cp $(shell which iptables) .
	sudo setcap 'cap_net_admin,cap_net_raw=+ep' iptables

test: hanoverd iptables
	PATH=$$PWD:$$PATH go test -v ./tests

# GNU Make instructions
.PHONY: test FORCE
