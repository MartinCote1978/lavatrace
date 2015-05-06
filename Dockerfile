FROM google/golang

RUN go get github.com/tools/godep

RUN mkdir -p /gopath/src/github.com/lavab/lavatrace
ADD . /gopath/src/github.com/lavab/lavatrace
RUN cd /gopath/src/github.com/lavab/lavatrace && godep go install github.com/lavab/lavatrace/api

CMD []
ENTRYPOINT ["/gopath/bin/api"]