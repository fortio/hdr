FROM scratch
COPY hdr /usr/bin/hdr
ENV HOME=/home/user
ENTRYPOINT ["/usr/bin/hdr"]
