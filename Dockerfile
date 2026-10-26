FROM golang:1.27-alpine AS build

WORKDIR /src

RUN apk add --no-cache ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nsh-backend .

FROM alpine:3.20

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 app

COPY --from=build /out/nsh-backend /app/nsh-backend
COPY migrations /app/migrations

USER app

ENV LISTEN_ADDR=0.0.0.0:3000

EXPOSE 3000

CMD ["/app/nsh-backend"]
