# =============================================================================
# rigger — thin host-side CLI for Rigger on Windows (Phase 6.5d)
#
# Mirrors rigger.sh. Uses the bundled curl.exe (Windows 10+) with a cookie jar so
# the rotating refresh session is handled transparently. Session lives in
# %USERPROFILE%\.rigger.
#
#   rigger login [url]                          authenticate, store a session
#   rigger list                                 list workspaces
#   rigger <cmd> <workspace> <env> [args...]    run a workspace command, streaming
#       cmds: start stop down restart update ps logs refresh backup restore init version
# =============================================================================
[CmdletBinding()]
param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Rest)
$ErrorActionPreference = 'Stop'

$cfgDir  = if ($env:RIGGER_CONFIG_DIR) { $env:RIGGER_CONFIG_DIR } else { Join-Path $HOME '.rigger' }
$cookies = Join-Path $cfgDir 'cookies'
$urlFile = Join-Path $cfgDir 'url'
$curl = Join-Path $env:SystemRoot 'System32\curl.exe'
if (-not (Test-Path $curl)) { $curl = 'curl.exe' }

function Die($m) { Write-Host "rigger: $m" -ForegroundColor Red; exit 1 }

function Invoke-Login($url) {
  if (-not $url) { $url = Read-Host 'Server URL [http://localhost:8080]' }
  if (-not $url) { $url = 'http://localhost:8080' }
  $user = Read-Host 'Username'
  $sec  = Read-Host 'Password' -AsSecureString
  $pass = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
  New-Item -ItemType Directory -Force -Path $cfgDir | Out-Null
  $body = "{""username"":""$user"",""password"":""$pass""}"
  $code = & $curl -s -o NUL -w '%{http_code}' -c $cookies -X POST "$url/api/auth/login" -H 'Content-Type: application/json' -d $body
  if ("$code" -ne '200') { Die "login failed (HTTP $code)" }
  Set-Content -Path $urlFile -Value $url -NoNewline
  Write-Host "Logged in to $url"
}

function Get-Url {
  if (-not (Test-Path $urlFile) -or -not (Test-Path $cookies)) { Die 'not logged in - run: rigger login' }
  return (Get-Content -Raw $urlFile).Trim()
}

function Get-Token($url) {
  $resp = & $curl -s -b $cookies -c $cookies -X POST "$url/api/auth/refresh"
  if ("$resp" -match '"token":"([^"]+)"') { return $Matches[1] }
  Die 'session expired - run: rigger login'
}

function Invoke-Action($action, $rest) {
  if ($rest.Count -lt 2) { Die "usage: rigger $action <workspace> <env> [args]" }
  $ws = $rest[0]; $env = $rest[1]
  $extra = @($rest | Select-Object -Skip 2)
  $url = Get-Url
  $token = Get-Token $url
  $extraJson = '['
  for ($i = 0; $i -lt $extra.Count; $i++) {
    if ($i) { $extraJson += ',' }
    $extraJson += '"' + $extra[$i] + '"'
  }
  $extraJson += ']'
  $body = "{""command"":""$action"",""extra"":$extraJson}"
  & $curl -sN -X POST "$url/api/workspaces/$ws/envs/$env/action" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d $body
}

function Invoke-List {
  $url = Get-Url
  $token = Get-Token $url
  $json = & $curl -s "$url/api/workspaces" -H "Authorization: Bearer $token"
  try { ("$json" | ConvertFrom-Json) | ForEach-Object { $_.name } }
  catch { [regex]::Matches("$json", '[\[{]"name":"([^"]+)"') | ForEach-Object { $_.Groups[1].Value } }
}

function Show-Usage {
  Get-Content $PSCommandPath | Select-Object -Skip 2 -First 13 | ForEach-Object { $_ -replace '^#\s?', '' }
}

$cmd = if ($Rest.Count) { $Rest[0] } else { 'help' }
$args2 = @($Rest | Select-Object -Skip 1)
$actions = 'start', 'stop', 'down', 'restart', 'update', 'ps', 'logs', 'refresh', 'backup', 'restore', 'init', 'version'

switch ($cmd) {
  'login' { Invoke-Login $args2[0] }
  'list'  { Invoke-List }
  { 'help', '-h', '--help' -contains $_ } { Show-Usage }
  { $actions -contains $_ } { Invoke-Action $cmd $args2 }
  default { Write-Host "rigger: unknown command '$cmd'"; Show-Usage; exit 1 }
}
