FROM alpine AS builder

COPY . /build/matternostr

RUN apk --no-cache add go \
    && cd /build/matternostr \
    && CGO_ENABLED=0 go build -o /app/matternostr

FROM alpine

RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /app/matternostr /app/matternostr

CMD ["/app/matternostr"]
