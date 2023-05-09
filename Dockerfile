FROM docker.io/golang:1.19-alpine AS base

WORKDIR /build

RUN apk --update --no-cache add build-base git
ARG BINARYNAME=syncv3
RUN --mount=target=. \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
  GIT_COMMIT=$(git rev-list -1 HEAD) && \
  go build -ldflags "-X main.GitCommit=$GIT_COMMIT" -trimpath -o /out/ "./cmd/$BINARYNAME"

FROM alpine:3.17

RUN apk --update --no-cache add curl
COPY --from=base /out/* /usr/bin/

ENV SYNCV3_BINDADDR="0.0.0.0:8008"
EXPOSE 8008

WORKDIR /usr/bin
ENTRYPOINT /usr/bin/syncv3
