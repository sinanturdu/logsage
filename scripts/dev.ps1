<#
.SYNOPSIS
  One-command local development for LogSage (Windows PowerShell 5.1 and pwsh).

.DESCRIPTION
  powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 <command>

    up      Create .env if missing (random secrets, COOKIE_SECURE=false), build
            and start the compose app profile (the api serves the dashboard on
            http://localhost:8083), run migrations, and create a dev user, the
            project "demo" and an ingest key if they do not exist yet.
    seed    Send demo data to "demo": an hour of history plus an incident in
            the last 15 minutes (about 70k events, loadgen seed).
    web     Run the Vite dev server with hot reload on http://localhost:5173
            (for working on the frontend; it proxies /api to the api).
    alerts  Build and start the alert evaluator (cmd/alerts) and the local
            webhook sink (cmd/alertsink) as background processes (process
            ids and logs in .dev\), with ALERTS_ALLOW_INSECURE_LOCAL=true;
            the api is recreated with that flag so it accepts the sink URL.
            Prints the sink URL for a channel. -AskSecret prompts (hidden)
            for the channel's signing secret so the sink verifies signatures.
    alerts-demo  Send fresh log messages with new templates (for a new_group
            rule) and a three-minute burst of errors (for an error_rate rule).
            Create the rules in the dashboard first.
    status  Show the services, the local alert processes and the dev login.
    down    Stop the local alert processes and everything else, and delete
            the data (docker compose down -v); asks before removing .dev/
            (use -Yes to skip the question).

  Secrets: .env and .dev/ are git-ignored and local-only. The dev login is
  printed once when it is created; nothing else secret is printed. "up"
  adds ALERTS_ENABLED=true and a random ALERTS_SECRET_KEY to .env when the
  key is missing (never printed).
  Passwords reach the admin CLI only through stdin; keys only come back on
  stdout.
#>
param(
  [Parameter(Position = 0)]
  [ValidateSet('up', 'seed', 'web', 'alerts', 'alerts-demo', 'status', 'down', 'help')]
  [string]$Command = 'help',
  [switch]$Yes,
  [switch]$AskSecret
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

$Root = Split-Path -Parent $PSScriptRoot
$EnvFile = Join-Path $Root '.env'
$DevDir = Join-Path $Root '.dev'
$LoginFile = Join-Path $DevDir 'login.txt'
$KeyFile = Join-Path $DevDir 'ingest.key'
$SinkSecretFile = Join-Path $DevDir 'sink.secret'
$SinkUrl = 'http://localhost:9099/hook'
# Local alert processes: name, binary, pid file and log files in .dev\.
$AlertProcs = @('alerts', 'alertsink')
$DevEmail = 'dev@example.com'
$Project = 'demo'
$ComposeArgs = @('--env-file', $EnvFile,
  '-f', (Join-Path $Root 'infra/docker-compose.yml'),
  '-f', (Join-Path $Root 'infra/docker-compose.app.yml'),
  '--profile', 'app')
$Utf8NoBom = New-Object System.Text.UTF8Encoding($false)

function Say([string]$msg) { Write-Host "dev: $msg" }
function Fail([string]$msg) { Write-Host "dev: $msg" -ForegroundColor Red; exit 1 }

function Require-Tool([string]$name, [string]$hint) {
  if (-not (Get-Command $name -ErrorAction SilentlyContinue)) { Fail "$name was not found. $hint" }
}

function Require-Docker {
  Require-Tool 'docker' 'Install Docker Desktop and start it.'
  $old = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  & docker info *> $null
  $code = $LASTEXITCODE; $ErrorActionPreference = $old
  if ($code -ne 0) { Fail 'Docker is installed but not running. Start Docker Desktop and try again.' }
}

# Runs docker compose with the app profile; stops the script on failure.
function Compose {
  & docker compose @ComposeArgs @args
  if ($LASTEXITCODE -ne 0) { Fail "docker compose $($args -join ' ') failed (exit $LASTEXITCODE)." }
}

# Runs a compose step that applies the migrations. When it fails because the
# PostgreSQL volume was created with another password than the one in .env,
# says so (the volume keeps its first password); deletes nothing.
function Compose-Migrating {
  # stderr is shown and kept: "run" removes its container, so its logs are gone.
  $old = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  & docker compose @ComposeArgs @args 2>&1 | Tee-Object -Variable out | ForEach-Object { "$_" } | Out-Host
  $code = $LASTEXITCODE
  if ($code -ne 0) { $out = @($out) + @(& docker compose @ComposeArgs logs --no-color --tail 50 migrate 2>&1) }
  $ErrorActionPreference = $old
  if ($code -eq 0) { return }
  if (($out | Out-String) -match 'password authentication failed|SQLSTATE 28P01') {
    Fail 'The dev database volume was created with a different password. Run dev.ps1 down to reset the dev volumes, then dev.ps1 up.'
  }
  Fail "docker compose $($args -join ' ') failed (exit $code)."
}

function Quote-Arg([string]$a) {
  if ($a -match '[\s"]') { return '"' + ($a -replace '"', '\"') + '"' }
  return $a
}

# Runs the admin CLI inside the api container. The optional stdin is the
# only way a password or key reaches it. Output is returned, never printed.
# Stdin is written as raw UTF-8 bytes through System.Diagnostics.Process:
# Windows PowerShell 5.1 prepends a byte order mark when it pipes text to a
# native program under a UTF-8 console (code page 65001), whatever
# $OutputEncoding says, and the admin CLI would read it as part of the value.
function Invoke-Admin([string[]]$AdminArgs, [string]$Stdin, [switch]$StdoutOnly) {
  $all = @('compose') + $ComposeArgs + @('exec', '-T', 'api', '/usr/local/bin/admin', $AdminArgs[0], '--allow-remote') + @($AdminArgs | Select-Object -Skip 1)
  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = (Get-Command docker).Source
  $psi.Arguments = (@($all | ForEach-Object { Quote-Arg $_ })) -join ' '
  $psi.UseShellExecute = $false
  $psi.RedirectStandardInput = $true
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  $psi.StandardOutputEncoding = $Utf8NoBom
  $psi.StandardErrorEncoding = $Utf8NoBom
  # .NET Framework builds the stdin writer from [Console]::InputEncoding and
  # writes its preamble (the BOM under code page 65001): use one without.
  $consoleIn = [Console]::InputEncoding
  try {
    [Console]::InputEncoding = $Utf8NoBom
    $p = [System.Diagnostics.Process]::Start($psi)
  } finally { [Console]::InputEncoding = $consoleIn }
  $stdout = $p.StandardOutput.ReadToEndAsync()
  $stderr = $p.StandardError.ReadToEndAsync()
  if ($Stdin) {
    $bytes = $Utf8NoBom.GetBytes($Stdin + "`n")
    $p.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length)
  }
  $p.StandardInput.Close()
  $p.WaitForExit()
  $out = $stdout.Result
  if (-not $StdoutOnly) { $out = $out + "`n" + $stderr.Result }
  return [pscustomobject]@{ Code = $p.ExitCode; Out = $out.Trim() }
}

function New-Secret([int]$Bytes = 24) {
  $b = New-Object byte[] $Bytes
  $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
  try { $rng.GetBytes($b) } finally { $rng.Dispose() }
  return ([Convert]::ToBase64String($b)).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

function Write-LocalFile([string]$path, [string[]]$lines) {
  [System.IO.File]::WriteAllLines($path, $lines, $Utf8NoBom)
}

function Ensure-EnvFile {
  if (Test-Path $EnvFile) { Say '.env exists; keeping it.'; return }
  $example = Join-Path $Root '.env.example'
  if (-not (Test-Path $example)) { Fail '.env.example is missing; run this from a LogSage checkout.' }
  $pg = New-Secret
  $out = foreach ($line in [System.IO.File]::ReadAllLines($example)) {
    if ($line -match '^\s*#' -or $line -notmatch '=') { $line; continue }
    $key, $value = $line -split '=', 2
    switch ($key) {
      'POSTGRES_PASSWORD' { "$key=$pg"; break }
      'DATABASE_URL' { "$key=$($value.Replace('change-me', $pg))"; break }
      'COOKIE_SECURE' { "$key=false"; break }
      'ALERTS_ENABLED' { "$key=true"; break }
      'ALERTS_SECRET_KEY' { "$key=$(New-AlertsKey)"; break }
      default {
        if ($value -like '*change-me*') { "$key=$(New-Secret)" } else { $line }
      }
    }
  }
  Write-LocalFile $EnvFile $out
  Say 'created .env with random secrets, COOKIE_SECURE=false and alerts on (local only, git-ignored).'
}

# 32 random bytes, standard base64: the ALERTS_SECRET_KEY format (ADR 0024).
function New-AlertsKey {
  $b = New-Object byte[] 32
  $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
  try { $rng.GetBytes($b) } finally { $rng.Dispose() }
  return [Convert]::ToBase64String($b)
}

function Get-EnvValue([string]$name) {
  if (-not (Test-Path $EnvFile)) { return '' }
  foreach ($line in [System.IO.File]::ReadAllLines($EnvFile)) {
    if ($line -match '^\s*#' -or $line -notmatch '=') { continue }
    $key, $value = $line -split '=', 2
    if ($key.Trim() -eq $name) { return $value.Trim() }
  }
  return ''
}

# An older .env without an alerts key gets one (and ALERTS_ENABLED=true) so
# the Alerts page works; an existing key is kept. The key is never printed.
function Ensure-AlertsKey {
  if (Get-EnvValue 'ALERTS_SECRET_KEY') { return }
  $lines = @([System.IO.File]::ReadAllLines($EnvFile))
  $seen = @{}
  $out = foreach ($line in $lines) {
    $key = ($line -split '=', 2)[0].Trim()
    if ($line -notmatch '^\s*#' -and $line -match '=' -and ($key -eq 'ALERTS_SECRET_KEY' -or $key -eq 'ALERTS_ENABLED')) {
      $seen[$key] = $true
      if ($key -eq 'ALERTS_ENABLED') { 'ALERTS_ENABLED=true' } else { "ALERTS_SECRET_KEY=$(New-AlertsKey)" }
    } else { $line }
  }
  if (-not $seen['ALERTS_ENABLED']) { $out = @($out) + 'ALERTS_ENABLED=true' }
  if (-not $seen['ALERTS_SECRET_KEY']) { $out = @($out) + "ALERTS_SECRET_KEY=$(New-AlertsKey)" }
  Write-LocalFile $EnvFile $out
  Say 'added ALERTS_ENABLED=true and a random ALERTS_SECRET_KEY to .env (not printed). Losing the key makes stored channel URLs and secrets unreadable.'
}

function Ensure-DevDir {
  if (-not (Test-Path $DevDir)) { New-Item -ItemType Directory -Path $DevDir | Out-Null }
  Write-LocalFile (Join-Path $DevDir '.gitignore') @('# Local-only dev credentials (scripts/dev.ps1). Never commit.', '*')
  Write-LocalFile (Join-Path $DevDir 'README.txt') @('LOCAL ONLY: dev login and ingest key created by scripts/dev.ps1 for the local stack. Never commit or share.')
}

function Wait-Healthy([int]$TimeoutSeconds = 240) {
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    $states = & docker compose @ComposeArgs ps --format '{{.Service}}={{.Health}}' 2> $null
    $pending = @($states | Where-Object { $_ -match '=(starting|unhealthy)$' })
    $api = @($states | Where-Object { $_ -eq 'api=healthy' })
    if ($api.Count -eq 1 -and $pending.Count -eq 0) { Say 'all services are healthy.'; return }
    Start-Sleep -Seconds 3
  }
  Fail "services did not become healthy within $TimeoutSeconds s. See: docker compose ps, docker compose logs api"
}

function Ensure-DevLogin {
  $password = $null
  if (Test-Path $LoginFile) {
    $line = Get-Content $LoginFile | Where-Object { $_ -like 'password: *' } | Select-Object -First 1
    if ($line) { $password = $line.Substring(10) }
  }
  $isNew = -not $password
  if ($isNew) { $password = New-Secret 18 }

  $r = Invoke-Admin @('create-user', '--email', $DevEmail) $password
  if ($r.Code -eq 0) {
    Say "created user $DevEmail."
  } elseif ($r.Out -match 'exist|conflict') {
    # The user exists (an earlier run); make sure the saved password works.
    $r = Invoke-Admin @('set-password', '--email', $DevEmail) $password
    if ($r.Code -ne 0) { Fail "could not set the dev password: $($r.Out)" }
  } else { Fail "could not create the dev user: $($r.Out)" }

  Write-LocalFile $LoginFile @("email: $DevEmail", "password: $password")
  if ($isNew) {
    Say "dev login (shown once, saved in .dev\login.txt): $DevEmail / $password"
  } else {
    Say 'dev login is in .dev\login.txt.'
  }
}

function Ensure-Project {
  $r = Invoke-Admin @('create-project', '--name', 'Demo', '--slug', $Project)
  if ($r.Code -eq 0) { Say "created project $Project." }
  elseif ($r.Out -notmatch 'exist|conflict') { Fail "could not create project ${Project}: $($r.Out)" }
  $r = Invoke-Admin @('add-member', '--project', $Project, '--email', $DevEmail, '--role', 'owner')
  if ($r.Code -ne 0) { Fail "could not make $DevEmail owner of ${Project}: $($r.Out)" }
}

function Ensure-IngestKey {
  if (Test-Path $KeyFile) {
    $key = (Get-Content $KeyFile -Raw).Trim()
    if ($key) {
      $r = Invoke-Admin @('verify-key') $key
      if ($r.Code -eq 0) { Say 'ingest key in .dev\ingest.key is valid.'; return }
      Say "the saved ingest key is not valid any more ($($r.Out -replace 'lsk_\S+', '<key>')); creating a new one."
    }
  }
  $r = Invoke-Admin @('create-key', '--project', $Project, '--kind', 'ingest', '--name', 'dev-seed') -StdoutOnly
  if ($r.Code -ne 0 -or $r.Out -notmatch '^lsk_') { Fail 'could not create an ingest key for the demo project.' }
  Write-LocalFile $KeyFile @($r.Out)
  Say 'created an ingest key (saved in .dev\ingest.key, not printed).'
}

function Cmd-Up {
  Require-Docker
  Ensure-EnvFile
  Ensure-AlertsKey
  Ensure-DevDir
  Say 'starting the stack (first run builds the images; this takes a few minutes)...'
  Compose-Migrating up -d --build
  Wait-Healthy
  Say 'running migrations...'
  Compose-Migrating run --rm --no-deps migrate
  Ensure-DevLogin
  Ensure-Project
  Ensure-IngestKey
  Say 'ready. Dashboard: http://localhost:8083'
  Say 'next: dev.ps1 seed for demo data; dev.ps1 alerts to try alerts; dev.ps1 web only for hot-reload frontend work (http://localhost:5173).'
}

function Cmd-Seed {
  Require-Docker
  Require-Tool 'go' 'Install Go 1.23 or newer (https://go.dev/dl/).'
  if (-not (Test-Path $KeyFile)) { Fail 'no ingest key yet. Run: dev.ps1 up' }
  $key = (Get-Content $KeyFile -Raw).Trim()
  Push-Location (Join-Path $Root 'backend')
  $env:LOADGEN_INGEST_KEY = $key
  try {
    & go run ./cmd/loadgen seed
    if ($LASTEXITCODE -ne 0) { Fail 'loadgen seed failed.' }
  } finally {
    Remove-Item Env:LOADGEN_INGEST_KEY -ErrorAction SilentlyContinue
    Pop-Location
  }
  Say 'seeded. Open http://localhost:8083/projects/demo/logs'
}

function Cmd-Web {
  Require-Tool 'node' 'Install Node 24 LTS (see frontend/.nvmrc).'
  Require-Tool 'npm' 'Install Node 24 LTS (npm comes with it).'
  $major = [int]((& node -v).TrimStart('v').Split('.')[0])
  if ($major -lt 22) { Fail "Node $(& node -v) is too old; use Node 24 LTS (frontend/.nvmrc)." }
  Push-Location (Join-Path $Root 'frontend')
  try {
    if (-not (Test-Path 'node_modules')) {
      & npm ci
      if ($LASTEXITCODE -ne 0) { Fail 'npm ci failed.' }
    }
    Say 'hot-reload dev server on http://localhost:5173 (Ctrl+C to stop); the built dashboard stays on http://localhost:8083.'
    & npm run dev
  } finally { Pop-Location }
}

function Proc-File([string]$name, [string]$ext) { return (Join-Path $DevDir "$name.$ext") }

# Returns the running process recorded in .dev\<name>.pid, or $null. The
# recorded name must match, so a reused process id is never touched.
function Get-AlertProc([string]$name) {
  $pidFile = Proc-File $name 'pid'
  if (-not (Test-Path $pidFile)) { return $null }
  $id = 0
  if (-not [int]::TryParse(((Get-Content $pidFile -Raw).Trim()), [ref]$id)) { return $null }
  $p = Get-Process -Id $id -ErrorAction SilentlyContinue
  if ($p -and $p.ProcessName -eq $name) { return $p }
  return $null
}

function Stop-AlertProcs {
  foreach ($name in $AlertProcs) {
    $p = Get-AlertProc $name
    if ($p) { Stop-Process -Id $p.Id -Force; $p.WaitForExit(); Say "stopped $name (pid $($p.Id))." }
    $pidFile = Proc-File $name 'pid'
    if (Test-Path $pidFile) { Remove-Item $pidFile }
  }
}

# Starts a built binary in the background with extra environment variables
# that only this child process sees (restored afterwards); logs go to .dev\.
function Start-AlertProc([string]$name, [string]$exe, [hashtable]$vars, [string[]]$argv) {
  $saved = @{}
  foreach ($k in $vars.Keys) { $saved[$k] = [Environment]::GetEnvironmentVariable($k, 'Process'); [Environment]::SetEnvironmentVariable($k, $vars[$k], 'Process') }
  try {
    $startArgs = @{ FilePath = $exe; PassThru = $true; WindowStyle = 'Hidden'
      RedirectStandardOutput = (Proc-File $name 'log'); RedirectStandardError = (Proc-File $name 'err.log') }
    if ($argv) { $startArgs.ArgumentList = $argv }
    $p = Start-Process @startArgs
  } finally {
    foreach ($k in $vars.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k], 'Process') }
  }
  Write-LocalFile (Proc-File $name 'pid') @("$($p.Id)")
  Say "started $name (pid $($p.Id)); log: .dev\$name.log and .dev\$name.err.log"
}

function Cmd-Alerts {
  Require-Docker
  Require-Tool 'go' 'Install Go 1.23 or newer (https://go.dev/dl/).'
  if (-not (Test-Path $EnvFile)) { Fail 'no .env yet. Run: dev.ps1 up' }
  Ensure-AlertsKey
  Ensure-DevDir
  if ($AskSecret) {
    $sec = Read-Host 'dev: paste the channel signing secret (input hidden)' -AsSecureString
    $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
    if (-not $plain.StartsWith('lsw_')) { Fail 'that does not look like a signing secret (it starts with lsw_).' }
    Write-LocalFile $SinkSecretFile @($plain)
    Say 'saved the signing secret in .dev\sink.secret (not printed).'
  }
  Stop-AlertProcs
  $bin = Join-Path $DevDir 'bin'
  if (-not (Test-Path $bin)) { New-Item -ItemType Directory -Path $bin | Out-Null }
  Say 'building cmd/alerts and cmd/alertsink...'
  Push-Location (Join-Path $Root 'backend')
  try {
    & go build -o (Join-Path $bin 'alerts.exe') ./cmd/alerts
    if ($LASTEXITCODE -ne 0) { Fail 'go build ./cmd/alerts failed.' }
    & go build -o (Join-Path $bin 'alertsink.exe') ./cmd/alertsink
    if ($LASTEXITCODE -ne 0) { Fail 'go build ./cmd/alertsink failed.' }
  } finally { Pop-Location }

  # The api checks channel URLs: recreate it with the development flag so it
  # accepts the sink's plain http localhost URL (dev.ps1 up turns it off again).
  Say 'recreating the api with ALERTS_ALLOW_INSECURE_LOCAL=true (development only)...'
  $env:ALERTS_ALLOW_INSECURE_LOCAL = 'true'
  try { Compose up -d --no-deps api } finally { Remove-Item Env:ALERTS_ALLOW_INSECURE_LOCAL -ErrorAction SilentlyContinue }
  Wait-Healthy

  $pgUser = Get-EnvValue 'POSTGRES_USER'; if (-not $pgUser) { $pgUser = 'logsage' }
  $pgDb = Get-EnvValue 'POSTGRES_DB'; if (-not $pgDb) { $pgDb = 'logsage' }
  $pgPass = [Uri]::EscapeDataString((Get-EnvValue 'POSTGRES_PASSWORD'))
  $chDb = Get-EnvValue 'CLICKHOUSE_DB'; if (-not $chDb) { $chDb = 'logsage' }
  $vars = @{
    DATABASE_URL = "postgres://${pgUser}:$pgPass@127.0.0.1:5432/${pgDb}?sslmode=disable"
    CLICKHOUSE_URL = 'http://127.0.0.1:8123'; CLICKHOUSE_DB = $chDb
    CLICKHOUSE_USER = (Get-EnvValue 'CLICKHOUSE_USER'); CLICKHOUSE_PASSWORD = (Get-EnvValue 'CLICKHOUSE_PASSWORD')
    ALERTS_ENABLED = 'true'; ALERTS_SECRET_KEY = (Get-EnvValue 'ALERTS_SECRET_KEY')
    ALERTS_ALLOW_INSECURE_LOCAL = 'true'; ALERTS_HTTP_ADDR = '127.0.0.1:8084'
    PUBLIC_DASHBOARD_URL = 'http://localhost:8083'; LOG_FORMAT = 'text'
  }
  Start-AlertProc 'alerts' (Join-Path $bin 'alerts.exe') $vars $null
  $sinkVars = @{ ALERTSINK_SECRET = '' }
  if (Test-Path $SinkSecretFile) { $sinkVars.ALERTSINK_SECRET = (Get-Content $SinkSecretFile -Raw).Trim() }
  Start-AlertProc 'alertsink' (Join-Path $bin 'alertsink.exe') $sinkVars @('-addr', '127.0.0.1:9099')
  Say "sink URL for a webhook channel: $SinkUrl"
  Say 'sink output: .dev\alertsink.log (verified=true when the signature checks out); deliveries as JSON: http://127.0.0.1:9099/deliveries'
  if (-not $sinkVars.ALERTSINK_SECRET) {
    Say 'signatures are not checked yet: create the channel, copy its secret, then run dev.ps1 alerts -AskSecret.'
  }
  Say 'evaluator health: http://127.0.0.1:8084/stats; stop both with dev.ps1 down.'
}

# Sends events straight to the ingest API with the dev ingest key.
function Send-Events([string]$key, [object[]]$events) {
  $body = @{ events = $events } | ConvertTo-Json -Depth 4 -Compress
  $r = Invoke-WebRequest -UseBasicParsing -Method Post -Uri 'http://127.0.0.1:8080/v1/ingest' -ContentType 'application/json' `
    -Headers @{ 'X-API-Key' = $key } -Body ([Text.Encoding]::UTF8.GetBytes($body))
  if ($r.StatusCode -ge 300) { Fail "ingest answered $($r.StatusCode)." }
}

function Cmd-AlertsDemo {
  Require-Docker
  Require-Tool 'go' 'Install Go 1.23 or newer (https://go.dev/dl/).'
  if (-not (Test-Path $KeyFile)) { Fail 'no ingest key yet. Run: dev.ps1 up' }
  $key = (Get-Content $KeyFile -Raw).Trim()
  Say 'create the rules first (Alerts page); a new_group rule fires once per new group.'
  # A letter-only word unique to this run makes the templates new each time.
  $letters = 'abcdefghijklmnopqrstuvwxyz'
  $rnd = New-Object System.Random
  $word = -join (1..8 | ForEach-Object { $letters[$rnd.Next(26)] })
  $now = [DateTime]::UtcNow
  $events = @()
  foreach ($i in 0..19) {
    $ts = $now.AddSeconds(-$i).ToString('yyyy-MM-ddTHH:mm:ss.fffZ')
    $events += @{ timestamp = $ts; level = 'error'; service = 'checkout'; message = "demo $word feature flag lookup failed for cart" }
    $events += @{ timestamp = $ts; level = 'warn'; service = 'payments'; message = "demo $word card network answered slowly" }
  }
  Send-Events $key $events
  Say "sent 40 events with two new message templates (word: $word)."
  Say 'now a three-minute burst of payments errors (loadgen demo --incident; Ctrl+C stops it)...'
  Push-Location (Join-Path $Root 'backend')
  $env:LOADGEN_INGEST_KEY = $key
  try {
    & go run ./cmd/loadgen demo --rate 20 --incident --incident-after 0s --incident-duration 3m --duration 3m --incident-rate 50
    if ($LASTEXITCODE -ne 0) { Fail 'loadgen demo failed.' }
  } finally {
    Remove-Item Env:LOADGEN_INGEST_KEY -ErrorAction SilentlyContinue
    Pop-Location
  }
  Say 'done. Alerts resolve after their clear_for once the burst is over.'
}

function Cmd-Status {
  Require-Docker
  if (Test-Path $EnvFile) { Compose ps } else { Say 'no .env yet; run: dev.ps1 up' }
  foreach ($name in $AlertProcs) {
    $p = Get-AlertProc $name
    if ($p) { Say "$name is running (pid $($p.Id)); log: .dev\$name.log" } else { Say "$name is not running (dev.ps1 alerts starts it)." }
  }
  if (Test-Path $LoginFile) { Say "dev login: $DevEmail (password in .dev\login.txt)" } else { Say 'no dev login yet.' }
  Say 'dashboard: http://localhost:5173  API: http://127.0.0.1:8083'
}

function Cmd-Down {
  Require-Docker
  Stop-AlertProcs
  if (Test-Path $EnvFile) {
    Compose down -v
    Say 'stack stopped and its data deleted.'
  } else { Say 'no .env; nothing to stop.' }
  if (Test-Path $DevDir) {
    $answer = 'n'
    if ($Yes) { $answer = 'y' } else { $answer = Read-Host 'dev: remove .dev\ (dev login, ingest key, sink secret, alert logs and binaries)? [y/N]' }
    if ($answer -match '^[yY]') { Remove-Item -Recurse -Force $DevDir; Say 'removed .dev\.' } else { Say 'kept .dev\.' }
  }
}

switch ($Command) {
  'up' { Cmd-Up }
  'seed' { Cmd-Seed }
  'web' { Cmd-Web }
  'alerts' { Cmd-Alerts }
  'alerts-demo' { Cmd-AlertsDemo }
  'status' { Cmd-Status }
  'down' { Cmd-Down }
  default { Get-Help $PSCommandPath -Detailed | Out-String | Write-Host }
}
