FROM docker.io/golang:1.17-alpine AS base

WORKDIR /build

COPY . /build
RUN apk --update --no-cache add bash build-base
RUN mkdir -p bin
RUN go build -trimpath -o bin/ ./cmd/syncv3

FROM alpine:latest

COPY --from=base /build/bin/* /usr/bin/
COPY client /usr/bin/client

ENV SYNCV3_BINDADDR="0.0.0.0:8008"

EXPOSE 8008

WORKDIR /usr/bin
RUN ls -l client
ENTRYPOINT /usr/bin/syncv3 -server "${SYNCV3_SERVER}" -db "${SYNCV3_DB}" -port "${SYNCV3_BINDADDR}"