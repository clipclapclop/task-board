FROM golang:1.26.5-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./...
ARG REVISION=development
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.revision=${REVISION}" -o /out/task-board ./cmd/task-board \
    && mkdir -m 1777 /out/tmp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/task-board /task-board
COPY --from=build --chmod=1777 /out/tmp /tmp
EXPOSE 8080
ENTRYPOINT ["/task-board"]
CMD ["serve"]
