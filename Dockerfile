# Multi-stage build: compile the provider binary, then ship it with the
# vocabulary files on a distroless base. Nothing inside the container is
# executed by mgtt at install time (it uses `docker cp` to pull
# /provider.yaml and /types/), so a scratch-class base is safe and small.

FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/provider .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/provider /bin/provider
COPY provider.yaml /provider.yaml
COPY types /types
ENTRYPOINT ["/bin/provider"]
