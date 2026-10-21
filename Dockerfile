FROM alpine:3.24

RUN apk add --no-cache ca-certificates

LABEL org.opencontainers.image.description="pathosd — health-aware BGP VIP announcer"

COPY pathosd /usr/local/bin/pathosd

ENTRYPOINT ["pathosd"]
CMD ["run", "--config", "/etc/pathosd/pathosd.yaml"]
