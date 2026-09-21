# syntax=docker/dockerfile:1
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/demo-svc ./cmd/demo-svc

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/demo-svc /demo-svc
USER 65532:65532
ENTRYPOINT ["/demo-svc"]
