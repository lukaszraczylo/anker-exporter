FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/anker-exporter ./cmd/anker-exporter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/anker-exporter /anker-exporter
USER 65532
EXPOSE 9876
ENTRYPOINT ["/anker-exporter"]
