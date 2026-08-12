FROM --platform=$BUILDPLATFORM node:16 AS builder

WORKDIR /web
COPY ./VERSION .
COPY ./web .

# Resolve the version string v<semver>-<sha> (or dev-<sha> if VERSION is empty)
# from the VERSION file plus the GIT_COMMIT build arg. CI passes the exact
# commit via `--build-arg GIT_COMMIT=$(git rev-parse HEAD)`; the fallback
# keeps local ad-hoc builds working.
ARG GIT_COMMIT=unknown
ARG VERSION_OVERRIDE=""
RUN if [ -n "$VERSION_OVERRIDE" ]; then \
      echo "$VERSION_OVERRIDE" > VERSION; \
    fi && \
    semver="$(tr -d '[:space:]' < VERSION)" && \
    if [ -n "$semver" ]; then \
      semver="${semver#v}"; \
      printf 'v%s-%s' "$semver" "$GIT_COMMIT" > /tmp/version; \
    else \
      printf 'dev-%s' "$GIT_COMMIT" > /tmp/version; \
    fi
ENV REACT_APP_VERSION="$(cat /tmp/version)"

RUN npm install --prefix /web/default & \
    npm install --prefix /web/berry & \
    npm install --prefix /web/air & \
    wait

RUN DISABLE_ESLINT_PLUGIN='true' npm run build --prefix /web/default & \
    DISABLE_ESLINT_PLUGIN='true' npm run build --prefix /web/berry & \
    DISABLE_ESLINT_PLUGIN='true' npm run build --prefix /web/air & \
    wait

FROM golang:alpine AS builder2

RUN apk add --no-cache \
    gcc \
    musl-dev \
    sqlite-dev \
    build-base

ENV GO111MODULE=on \
    CGO_ENABLED=1 \
    GOOS=linux

WORKDIR /build

ADD go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=builder /web/build ./web/build

# Resolve the same version string the frontend stage did. Both halves share
# the same VERSION file and GIT_COMMIT so the binary's `common.Version`
# matches the web bundle's REACT_APP_VERSION byte-for-byte.
ARG GIT_COMMIT=unknown
ARG VERSION_OVERRIDE=""
RUN if [ -n "$VERSION_OVERRIDE" ]; then \
      echo "$VERSION_OVERRIDE" > VERSION; \
    fi && \
    semver="$(tr -d '[:space:]' < VERSION)" && \
    if [ -n "$semver" ]; then \
      semver="${semver#v}"; \
      printf 'v%s-%s' "$semver" "$GIT_COMMIT" > /tmp/version; \
    else \
      printf 'dev-%s' "$GIT_COMMIT" > /tmp/version; \
    fi && \
    go build -trimpath -ldflags "-s -w -X 'github.com/songquanpeng/one-api/common.Version=$(cat /tmp/version)' -linkmode external -extldflags '-static'" -o one-api

FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder2 /build/one-api /

EXPOSE 3000
WORKDIR /data
ENTRYPOINT ["/one-api"]
