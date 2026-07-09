FROM golang:1.21-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /out/fal-github-bot .

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/fal-github-bot /fal-github-bot
EXPOSE 8080
ENTRYPOINT ["/fal-github-bot"]
