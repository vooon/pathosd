FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /pathosd ./cmd/pathosd

FROM alpine:3.21

RUN apk add --no-cache ca-certificates
COPY --from=builder /pathosd /usr/local/bin/pathosd

ENTRYPOINT ["pathosd"]
CMD ["run", "--config", "/etc/pathosd/pathosd.yaml"]
