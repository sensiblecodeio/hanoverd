FROM ubuntu:15.10

RUN apt-get install -y netcat-openbsd

RUN mkdir /runtime

RUN cp --parents \
    /bin/sh \
    /bin/dash \
    /bin/nc \
    /bin/hostname \
    /etc/alternatives/nc \
    /lib64/ld-linux-x86-64.so.* \
    /lib/x86_64-linux-gnu/libnsl*.so* \
    /lib/x86_64-linux-gnu/libc.so.* \
    /lib/x86_64-linux-gnu/libbsd.so.* \
    /lib/x86_64-linux-gnu/libresolv*.so* \
    /runtime

COPY ./runtime/Dockerfile /runtime/Dockerfile

ENTRYPOINT ["tar", "--directory", "/runtime", "--create", "."]
