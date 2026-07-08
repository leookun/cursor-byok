FROM golang:1.25 AS build

WORKDIR /src

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/cursor-tab-server .

FROM scratch

WORKDIR /app

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/cursor-tab-server /app/cursor-tab-server
COPY config.yaml /app/config.yaml

EXPOSE 8041

ENTRYPOINT ["/app/cursor-tab-server"]
