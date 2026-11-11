@echo off
rem Tayfam Ops: secret islerini terminal komutu yazmadan yapmak icin yerel
rem arayuz (docs/runbooks/sops.md, "Tayfam Ops ile"). Bu dosyayi Windows'ta
rem istediginiz yere (orn. Masaustu) kopyalayin ve asagidaki IKI satiri
rem kendi bilgisayariniza gore duzenleyin. Cift tiklayinca WSL'de araci
rem baslatir; arac tarayiciyi tek kullanimlik baglantiyla kendisi acar.
rem Kapatmak icin acilan pencereyi kapatin.

rem WSL dagitiminin adi (Gezgin'in sol panelindeki "Linux" altinda gorunur).
set "WSL_DISTRO=DUZENLEYIN"
rem Reponun WSL icindeki yolu (orn. /home/<kullanici>/tayfam-api).
set "REPO_DIR=DUZENLEYIN"

if "%WSL_DISTRO%"=="DUZENLEYIN" goto config
if "%REPO_DIR%"=="DUZENLEYIN" goto config

chcp 65001 >nul
rem bash -l: go, sops ve age'in PATH'i giris kabugunun ayarlarindan gelir.
wsl.exe -d "%WSL_DISTRO%" --cd "%REPO_DIR%" -- bash -lc "make ops"
echo.
echo Tayfam Ops kapandi.
pause
exit /b

:config
echo Bu dosyanin en ustundeki WSL_DISTRO ve REPO_DIR satirlarini duzenleyin
echo (sag tik, Duzenle). Ayrinti: docs/runbooks/sops.md, "Tayfam Ops ile".
pause
exit /b 1
