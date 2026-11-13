# StreamHata Backend

[![codecov](https://codecov.io/gh/Stream-Hata/nameless-streamhata-backend/branch/master/graph/badge.svg)](https://codecov.io/gh/Stream-Hata/nameless-streamhata-backend)

Go-бэкенд StreamHata: Twitch OAuth/EventSub/IRC, каталог, баллы, донаты, музыка, виджеты, сообщения, блог и security API.

## Команды

```bash
go test -race -shuffle=on -count=1 ./...
go test -race -shuffle=on -count=1 -covermode=atomic -coverprofile=coverage.out ./...
go vet ./...
go build ./...
```

## Деплой

См. [deploy/README.md](deploy/README.md).
