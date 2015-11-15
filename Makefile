all: hanoverd

# Compute a file that looks like "hanoverd: file.go" for all go source files
# that hanoverd depends on.
hanoverd.deps:
	# 1) Get recursive list of deps.
	# 2) Filter out stdlib.
	# 3) Print all Go source files used in the package.
	# 4) Use sed to only catch lines which are in this directory and remove $PWD.
	go list -f '{{.ImportPath}}{{"\n"}}{{range .Deps}}{{.}}{{"\n"}}{{end}}' . | \
	xargs go list -f '{{if not .Standard}}{{.ImportPath}}{{end}}' | \
	xargs go list -f '{{with $$p := .}}{{range .GoFiles}}{{$$p.Dir}}/{{.}}{{"\n"}}{{end}}{{end}}' | \
	sed --quiet 's|^'$(shell pwd)/'|hanoverd: |p' > hanoverd.deps

# So that make knows about hanoverd's other dependencies.
-include hanoverd.deps

hanoverd: hanoverd.deps
	# Build hanoverd using
	go generate
	docker build -t hanoverd-buildtime .
	docker run --rm hanoverd-buildtime cat /go/bin/hanoverd > ./hanoverd
	chmod +x ./hanoverd

release: hanoverd
	mv hanoverd hanoverd_linux_amd64
	gphr release -keep=true hanoverd_linux_amd64

iptables:
	cp $(shell which iptables) .
	sudo setcap 'cap_net_admin,cap_net_raw=+ep' iptables

test: hanoverd iptables
	@PATH=.:$(PATH) go test -v ./tests

.PHONY: release test
