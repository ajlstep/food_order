@echo off
rem Porneste serviciile Go, fiecare in fereastra lui. NATS si ngrok raman in Docker.
rem O fereastra ramane deschisa si dupa eroare, ca sa poti citi ce s-a intamplat.

call :run logs-collect logs "go run . collect"
call :run logs-web logs "go run . web"
call :run users users "go run ."
call :run catalog catalog "go run ."
call :run core core "go run ."
call :run parser parser "go run ."
call :run gateway gateway "go run ."
call :run bot bot "go run ."
exit /b 0

:run
start "%~1" /D "%~dp0%~2" cmd /k %~3
timeout /t 2 /nobreak >nul
exit /b 0
