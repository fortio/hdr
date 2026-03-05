FROM scratch
COPY hdr_demo /usr/bin/hdr_demo
ENV HOME=/home/user
WORKDIR /home/user
ENTRYPOINT ["/usr/bin/hdr_demo"]
