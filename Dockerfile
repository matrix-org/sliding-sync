FROM docker.io/golang:1.20-alpine AS base

WORKDIR /build

RUN apk --update --no-cache add build-base git
ARG BINARYNAME=syncv3
RUN --mount=target=. \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
  GIT_COMMIT=$(git rev-list -1 HEAD) && \
  go build -ldflags "-X main.GitCommit=$GIT_COMMIT" -trimpath -o /out/syncv3 "./cmd/$BINARYNAME"

FROM alpine:3.17

RUN apk --update --no-cache add curl
COPY --from=base /out/* /usr/bin/

ENV SYNCV3_BINDADDR="0.0.0.0:8008"
EXPOSE 8008

WORKDIR /usr/bin
# It would be nice if the binary we exec was called $BINARYNAME here, but build args
# aren't expanded in ENTRYPOINT directives. Instead, we always call the output binary
# "syncv3". (See https://github.com/moby/moby/issues/18492)
ENTRYPOINT /usr/bin/syncv3
