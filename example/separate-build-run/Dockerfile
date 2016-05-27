FROM alpine

RUN apk --no-cache add netcat-openbsd

RUN mkdir /runtime

RUN cp --parents \
    /bin/busybox \
    /bin/hostname \
    /usr/bin/nc \
    /lib/ld-musl-x86_64.so.* \
    /lib/libc.musl-x86_64.so.* \
    /runtime

COPY ./runtime/Dockerfile /runtime/Dockerfile

ENTRYPOINT ["tar", "--directory", "/runtime", "--create", "."]
