# StreamHata Backend

[![codecov](https://codecov.io/gh/Stream-Hata/nameless-streamhata-backend/branch/master/graph/badge.svg)](https://codecov.io/gh/Stream-Hata/nameless-streamhata-backend)

Go backend for StreamHata: Twitch OAuth/EventSub/IRC, catalog, points, donations, music, widgets, messaging, blog and security API.

## Commands

```bash
go test -race -shuffle=on -count=1 ./...
go test -race -shuffle=on -count=1 -covermode=atomic -coverprofile=coverage.out ./...
go vet ./...
go build ./...
```

## Deployment

See [deploy/README.md](deploy/README.md).
