# Gollery

Web server em Go e frontend com HTMX para hospedar arquivos, com foco em fotos.


## Iniciar

Para testar o projeto, rode
```bash
go run main.go
```


## Compilando para ARM

```bash
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gollery
```

e para rodar
```
./gollery
```

