# Gollery

Web server em Go e frontend com HTMX para hospedar arquivos, com foco em fotos.


## Iniciar

Para testar o projeto, rode
```bash
go run main.go
```


## Compilando

Para gerar o binário para o sistema operacional e a arquitetura atuais:
```bash
go build -trimpath -ldflags="-s -w" -o gollery .
```

Para gerar binários Linux para ARM 32 bits (ARMv7) e ARM64:
```bash
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gollery-arm32 .
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gollery-arm64 .
```

Execute o binário correspondente no dispositivo:
```bash
./gollery
./gollery-arm32
./gollery-arm64
```

Para Android ARM64, use:
```bash
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gollery-android-arm64 .
```

