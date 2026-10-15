FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /puzzle-table .

FROM gcr.io/distroless/static-debian12
COPY --from=build /puzzle-table /puzzle-table
ENV PUZZLE_DATA=/data PUZZLE_ADDR=:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/puzzle-table"]
