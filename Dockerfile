FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/phorge-api ./cmd/phorge-api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/phorge-api /phorge-api
USER nonroot
ENTRYPOINT ["/phorge-api"]
