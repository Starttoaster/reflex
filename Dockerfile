# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/starttoaster/reflex/cmd.version=${VERSION} -X github.com/starttoaster/reflex/cmd.commit=${COMMIT}" \
    -o /out/reflex .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/reflex /reflex
# Numeric so Kubernetes runAsNonRoot can verify it (distroless "nonroot" is 65532).
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/reflex"]
CMD ["serve"]
