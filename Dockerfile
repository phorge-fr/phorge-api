FROM --platform=$BUILDPLATFORM golang:1.25 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/phorge-api ./cmd/phorge-api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/phorge-api /phorge-api
USER 65532:65532
ENTRYPOINT ["/phorge-api"]
