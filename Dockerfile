FROM docker.io/golang:1.17-alpine AS base

WORKDIR /build

COPY . /build
RUN apk --update --no-cache add bash build-base git && mkdir -p bin
RUN GIT_COMMIT=$(git rev-list -1 HEAD) && \
  go build -ldflags "-X main.GitCommit=$GIT_COMMIT" -trimpath -o bin/ ./cmd/syncv3

FROM alpine:latest

COPY --from=base /build/bin/* /usr/bin/

ENV SYNCV3_BINDADDR="0.0.0.0:8008"
EXPOSE 8008

WORKDIR /usr/bin
ENTRYPOINT /usr/bin/syncv3 -server "${SYNCV3_SERVER}" -db "${SYNCV3_DB}" -port "${SYNCV3_BINDADDR}"