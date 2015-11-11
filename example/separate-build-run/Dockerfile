FROM ubuntu:15.10

RUN mkdir /runtime

RUN cp --parents \
    /bin/dash \
    /lib/x86_64-linux-gnu/libc.so.6 \
    /runtime
RUN ln -s /bin/dash /runtime/bin/sh

COPY ./runtime/Dockerfile /runtime/Dockerfile

ENTRYPOINT ["tar", "--directory", "/runtime", "--create", "."]
