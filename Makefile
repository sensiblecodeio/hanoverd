buildtime: .FORCE
	go generate
	docker build -t hanoverd-buildtime .
	docker run --rm hanoverd-buildtime cat /go/bin/hanoverd > ./hanoverd
	chmod u+x ./hanoverd

release: buildtime .FORCE
	mv hanoverd hanoverd_linux_amd64
	gphr release -keep=true hanoverd_linux_amd64

.FORCE:
.PHONY: .FORCE buildtime release
