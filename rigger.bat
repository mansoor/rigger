@echo off
rem rigger — Windows CLI shim that runs the PowerShell wrapper alongside it.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0rigger.ps1" %*
