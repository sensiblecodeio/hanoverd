buildtime: .PHONY
	docker build -t hanoverd-buildtime .
	docker run --rm hanoverd-buildtime cat /go/bin/hanoverd > ./hanoverd
	chmod u+x ./hanoverd

.PHONY:
