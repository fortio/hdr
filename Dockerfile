FROM scratch
COPY demo /usr/bin/demo
ENV HOME=/home/user
ENTRYPOINT ["/usr/bin/demo"]
