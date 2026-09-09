param(
    [Parameter(Position = 0, ValueFromRemainingArguments = $true)]
    [string[]]$ScriptArguments
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$AppName = 'cpa-manager-plus'
$RootDir = if ($PSScriptRoot) { $PSScriptRoot } else { Split-Path -Parent $MyInvocation.MyCommand.Path }
$LocalDir = Join-Path $RootDir '.local'
$BuildDir = Join-Path $LocalDir 'build'
$DataDir = Join-Path $LocalDir 'data'
$TempDir = Join-Path $LocalDir 'tmp'
$ControlDir = Join-Path $LocalDir 'control'
$ControlSource = Join-Path $RootDir 'bin/native/cpa-manager-plusctl.ps1'
$ControlScript = Join-Path $ControlDir 'cpa-manager-plusctl.ps1'
$ServerSource = Join-Path $RootDir 'apps/manager-server'
$WebHTML = Join-Path $RootDir 'apps/web/dist/index.html'
$BinaryPath = Join-Path $BuildDir "$AppName.exe"
$NextBinaryPath = Join-Path $BuildDir "$AppName.next.exe"
$PreviousBinaryPath = Join-Path $BuildDir "$AppName.previous.exe"
$AdminKeyFile = Join-Path $DataDir 'admin.key'
$AdminKeyStateFile = Join-Path $DataDir '.admin-key-initialized'
$PortFile = Join-Path $ControlDir 'run/source.port'
$ServiceLogDir = if ($env:CPA_MANAGER_PLUS_LOG_DIR) { $env:CPA_MANAGER_PLUS_LOG_DIR } else { Join-Path $ControlDir 'logs' }
$ServiceLogFile = if ($env:CPA_MANAGER_PLUS_LOG_FILE) { $env:CPA_MANAGER_PLUS_LOG_FILE } else { Join-Path $ServiceLogDir "$AppName.log" }
$ServiceErrorLogFile = if ($env:CPA_MANAGER_PLUS_ERR_LOG_FILE) { $env:CPA_MANAGER_PLUS_ERR_LOG_FILE } else { Join-Path $ServiceLogDir "$AppName.err.log" }
$StartupLogFile = $env:CPA_MANAGER_PLUS_STARTUP_LOG
$StartupTranscriptStarted = $false
$StartupAttemptStartedAt = $null
$SourceHost = if ($env:CPA_MANAGER_PLUS_SOURCE_HOST) { $env:CPA_MANAGER_PLUS_SOURCE_HOST.Trim() } else { '127.0.0.1' }
$ExternalAdminKeyConfigured = -not [string]::IsNullOrWhiteSpace($env:CPA_MANAGER_ADMIN_KEY) -or
    -not [string]::IsNullOrWhiteSpace($env:CPA_MANAGER_ADMIN_KEY_FILE)
$DefaultPort = 18317
$RequestedPort = $null
$Action = 'start'
$ActionArguments = @()
$PowerShellPath = (Get-Process -Id $PID -ErrorAction Stop).Path

function Show-Usage {
    Write-Host @"
Usage: .\cpa-manager-plus.bat [command] [--port <port>]

Commands:
  build      Install dependencies and compile the web panel and Go service.
  start      Start the source-built service; build first when needed.
  stop       Stop the managed source-built service.
  restart    Restart without rebuilding.
  rebuild    Build a candidate while serving, then replace and restart.
  status     Show process, liveness, and business-data readiness.
  logs       Show logs; accepts a line count or -f/--follow.
  admin-key  Show the source runtime's locally managed admin key.
  help       Show this help.

Examples:
  .\cpa-manager-plus.bat start
  .\cpa-manager-plus.bat start --port 18318
  .\cpa-manager-plus.bat rebuild --port=18317
  .\cpa-manager-plus.bat logs 120
  .\cpa-manager-plus.bat admin-key
  .\cpa-manager-plus.bat stop

Environment overrides:
  CPA_MANAGER_PLUS_SOURCE_HOST   Bind host, default: 127.0.0.1
  CPA_MANAGER_PLUS_SOURCE_PORT   Default port, default: 18317
  CPA_MANAGER_PLUS_SKIP_NPM_CI   Set to 1 to reuse installed dependencies
  CPA_MANAGER_PLUS_STARTUP_LOG   Override the Windows startup transcript path
  CPA_MANAGER_PLUS_NO_PAUSE      Set to 1 for non-interactive batch failures
  USAGE_DATA_DIR and related Manager Server variables remain supported.

Source runtime files are stored under .local/.
"@
}

function Resolve-PortValue {
    param(
        [string]$Value,
        [string]$Source
    )

    if ($Value -notmatch '^\d{1,5}$') {
        throw "$Source must be an integer between 1 and 65535."
    }
    $port = [int]$Value
    if ($port -lt 1 -or $port -gt 65535) {
        throw "$Source must be between 1 and 65535."
    }
    return $port
}

function Parse-Arguments {
    $positionals = [System.Collections.Generic.List[string]]::new()
    $tokens = @($ScriptArguments | Where-Object { $null -ne $_ })
    for ($index = 0; $index -lt $tokens.Count; $index++) {
        $token = $tokens[$index]
        if ($token -eq '--port') {
            if ($null -ne $script:RequestedPort) {
                throw '--port may only be specified once.'
            }
            if ($index + 1 -ge $tokens.Count) {
                throw '--port requires a value.'
            }
            $index++
            $script:RequestedPort = Resolve-PortValue $tokens[$index] '--port'
            continue
        }
        if ($token.StartsWith('--port=', [System.StringComparison]::Ordinal)) {
            if ($null -ne $script:RequestedPort) {
                throw '--port may only be specified once.'
            }
            $script:RequestedPort = Resolve-PortValue $token.Substring('--port='.Length) '--port'
            continue
        }
        $positionals.Add($token)
    }

    if ($positionals.Count -gt 0) {
        $script:Action = $positionals[0].ToLowerInvariant()
    }
    if ($positionals.Count -gt 1) {
        $script:ActionArguments = @($positionals.GetRange(1, $positionals.Count - 1))
    }

    $validActions = @('build', 'start', 'stop', 'restart', 'rebuild', 'status', 'logs', 'admin-key', 'help', '-h', '--help')
    if ($validActions -notcontains $script:Action) {
        throw "Unknown command: $($script:Action)"
    }
    if ($script:Action -ne 'logs' -and $script:ActionArguments.Count -gt 0) {
        throw "Unexpected argument for $($script:Action): $($script:ActionArguments[0])"
    }
    if ($script:Action -eq 'logs' -and $script:ActionArguments.Count -gt 1) {
        throw 'logs accepts at most one line count or -f/--follow.'
    }
}

function Ensure-Layout {
    foreach ($path in @($LocalDir, $BuildDir, $DataDir, $TempDir, $ControlDir)) {
        New-Item -ItemType Directory -Force -Path $path | Out-Null
    }
    if (-not (Test-Path -LiteralPath $ControlSource)) {
        throw "Native process controller does not exist: $ControlSource"
    }
    Copy-Item -LiteralPath $ControlSource -Destination $ControlScript -Force
    $env:CPA_MANAGER_PLUS_BIN = $BinaryPath
}

function Get-ConfiguredPort {
    if ($null -ne $RequestedPort) {
        return $RequestedPort
    }
    if ($env:CPA_MANAGER_PLUS_SOURCE_PORT) {
        return Resolve-PortValue $env:CPA_MANAGER_PLUS_SOURCE_PORT 'CPA_MANAGER_PLUS_SOURCE_PORT'
    }
    return $DefaultPort
}

function Get-StoredPort {
    if (Test-Path -LiteralPath $PortFile) {
        $stored = (Get-Content -LiteralPath $PortFile -Raw).Trim()
        try {
            return Resolve-PortValue $stored 'stored source port'
        } catch {
            Remove-Item -LiteralPath $PortFile -Force -ErrorAction SilentlyContinue
        }
    }
    return Get-ConfiguredPort
}

function Set-RuntimeEnvironment {
    param([int]$Port)

    $env:CPA_MANAGER_PLUS_BIN = $BinaryPath
    $env:HTTP_ADDR = "${SourceHost}:$Port"
    if (-not $env:USAGE_DATA_DIR) {
        $env:USAGE_DATA_DIR = $DataDir
    }
    $env:USAGE_DATA_DIR = [System.IO.Path]::GetFullPath($env:USAGE_DATA_DIR)
    if (-not $env:USAGE_DB_PATH) {
        $env:USAGE_DB_PATH = Join-Path $env:USAGE_DATA_DIR 'usage.sqlite'
    }
    $env:USAGE_DB_PATH = [System.IO.Path]::GetFullPath($env:USAGE_DB_PATH)
    if (-not $env:CPA_MANAGER_DATA_KEY_PATH) {
        $env:CPA_MANAGER_DATA_KEY_PATH = Join-Path $env:USAGE_DATA_DIR 'data.key'
    }
    $env:CPA_MANAGER_DATA_KEY_PATH = [System.IO.Path]::GetFullPath($env:CPA_MANAGER_DATA_KEY_PATH)
    if (-not $env:USAGE_CORS_ORIGINS) {
        $env:USAGE_CORS_ORIGINS = '*'
    }
    if (-not $ExternalAdminKeyConfigured) {
        $env:CPA_MANAGER_ADMIN_KEY_FILE = $AdminKeyFile
    } elseif (-not [string]::IsNullOrWhiteSpace($env:CPA_MANAGER_ADMIN_KEY_FILE)) {
        $env:CPA_MANAGER_ADMIN_KEY_FILE = [System.IO.Path]::GetFullPath($env:CPA_MANAGER_ADMIN_KEY_FILE)
    }
}

function Set-PrivateFileAcl {
    param([string]$Path)

    $currentUserSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
    $acl = New-Object System.Security.AccessControl.FileSecurity
    $acl.SetOwner($currentUserSid)
    $acl.SetAccessRuleProtection($true, $false)
    $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
        $currentUserSid,
        [System.Security.AccessControl.FileSystemRights]::FullControl,
        [System.Security.AccessControl.AccessControlType]::Allow
    )
    [void]$acl.AddAccessRule($rule)
    if ($null -ne ('System.IO.FileSystemAclExtensions' -as [type])) {
        [System.IO.FileSystemAclExtensions]::SetAccessControl(
            [System.IO.FileInfo]::new($Path),
            $acl
        )
    } else {
        [System.IO.File]::SetAccessControl($Path, $acl)
    }
}

function Write-PrivateTextFile {
    param(
        [string]$Path,
        [string]$Value
    )

    [System.IO.File]::WriteAllText(
        $Path,
        $Value + [Environment]::NewLine,
        (New-Object System.Text.UTF8Encoding($false))
    )
    Set-PrivateFileAcl -Path $Path
}

function Get-ManagedAdminKey {
    if (-not (Test-Path -LiteralPath $AdminKeyFile)) {
        throw "Managed admin key does not exist yet: $AdminKeyFile"
    }
    $adminKey = (Get-Content -LiteralPath $AdminKeyFile -Raw).Trim()
    if ($adminKey -notmatch '^cpamp_[0-9A-Za-z]{32}$') {
        throw "Managed admin key file is empty or invalid: $AdminKeyFile"
    }
    return $adminKey
}

function Get-EffectiveAdminKey {
    if (-not [string]::IsNullOrWhiteSpace($env:CPA_MANAGER_ADMIN_KEY)) {
        return $env:CPA_MANAGER_ADMIN_KEY.Trim()
    }
    if (-not [string]::IsNullOrWhiteSpace($env:CPA_MANAGER_ADMIN_KEY_FILE)) {
        if (-not (Test-Path -LiteralPath $env:CPA_MANAGER_ADMIN_KEY_FILE)) {
            throw "Admin key file does not exist: $($env:CPA_MANAGER_ADMIN_KEY_FILE)"
        }
        $adminKey = (Get-Content -LiteralPath $env:CPA_MANAGER_ADMIN_KEY_FILE -Raw).Trim()
        if ([string]::IsNullOrWhiteSpace($adminKey)) {
            throw "Admin key file is empty: $($env:CPA_MANAGER_ADMIN_KEY_FILE)"
        }
        return $adminKey
    }
    return (Get-ManagedAdminKey)
}

function Get-SHA256Hex {
    param([string]$Value)

    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
        $bytes = (New-Object System.Text.UTF8Encoding($false)).GetBytes($Value)
        return ([System.BitConverter]::ToString($sha256.ComputeHash($bytes))).Replace('-', '').ToLowerInvariant()
    } finally {
        $sha256.Dispose()
    }
}

function Get-DesiredAdminKeyState {
    if (-not (Test-Path -LiteralPath $env:USAGE_DB_PATH)) {
        return $null
    }
    $database = Get-Item -LiteralPath $env:USAGE_DB_PATH
    if ($database.Length -eq 0) {
        return $null
    }

    $databasePath = [System.IO.Path]::GetFullPath($database.FullName).ToLowerInvariant()
    $databaseIdentity = "$databasePath|$($database.CreationTimeUtc.Ticks)"
    $adminKeyHash = Get-SHA256Hex -Value (Get-EffectiveAdminKey)
    return (Get-SHA256Hex -Value "windows-v1|$databaseIdentity|$adminKeyHash")
}

function Get-StoredAdminKeyState {
    if (-not (Test-Path -LiteralPath $AdminKeyStateFile)) {
        return ''
    }
    return (Get-Content -LiteralPath $AdminKeyStateFile -Raw).Trim()
}

function Ensure-AdminKeyConfiguration {
    if (-not $ExternalAdminKeyConfigured -and -not (Test-Path -LiteralPath $AdminKeyFile)) {
        $randomBytes = New-Object byte[] 16
        $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
        try {
            $rng.GetBytes($randomBytes)
        } finally {
            $rng.Dispose()
        }
        $adminKey = 'cpamp_' + [System.BitConverter]::ToString($randomBytes).Replace('-', '')
        Write-PrivateTextFile -Path $AdminKeyFile -Value $adminKey
        Remove-Item -LiteralPath $AdminKeyStateFile -Force -ErrorAction SilentlyContinue
    }

    [void](Get-EffectiveAdminKey)
    $desiredState = Get-DesiredAdminKeyState
    if ([string]::IsNullOrWhiteSpace($desiredState) -or $desiredState -eq (Get-StoredAdminKeyState)) {
        return
    }

    $resetKeyFile = $env:CPA_MANAGER_ADMIN_KEY_FILE
    $temporaryKeyFile = $null
    if (-not [string]::IsNullOrWhiteSpace($env:CPA_MANAGER_ADMIN_KEY)) {
        $temporaryKeyFile = Join-Path $TempDir ("admin-key-reset.$PID." + [guid]::NewGuid().ToString('N'))
        Write-PrivateTextFile -Path $temporaryKeyFile -Value (Get-EffectiveAdminKey)
        $resetKeyFile = $temporaryKeyFile
    }

    Write-Host '==> Synchronizing the SQLite admin credential with the configured key'
    try {
        Invoke-NativeCommand {
            & $BinaryPath reset-admin-key --db-path $env:USAGE_DB_PATH --admin-key-file $resetKeyFile
        } 'admin key synchronization failed'
        Write-PrivateTextFile -Path $AdminKeyStateFile -Value $desiredState
    } finally {
        if ($temporaryKeyFile) {
            Remove-Item -LiteralPath $temporaryKeyFile -Force -ErrorAction SilentlyContinue
        }
    }
}

function Mark-AdminKeyStateInitialized {
    $desiredState = Get-DesiredAdminKeyState
    if (-not [string]::IsNullOrWhiteSpace($desiredState) -and $desiredState -ne (Get-StoredAdminKeyState)) {
        Write-PrivateTextFile -Path $AdminKeyStateFile -Value $desiredState
    }
}

function Show-AdminKey {
    if ($ExternalAdminKeyConfigured) {
        throw 'The admin key is externally managed through CPA_MANAGER_ADMIN_KEY or CPA_MANAGER_ADMIN_KEY_FILE.'
    }
    Write-Host (Get-ManagedAdminKey)
}

function Get-ServiceURL {
    param(
        [int]$Port,
        [string]$Path
    )

    $healthHost = $SourceHost
    if ($healthHost -in @('0.0.0.0', '::', '[::]')) {
        $healthHost = '127.0.0.1'
    }
    if ($healthHost.Contains(':') -and -not $healthHost.StartsWith('[')) {
        $healthHost = "[$healthHost]"
    }
    return "http://${healthHost}:$Port$Path"
}

function Start-StartupLogging {
    if ([string]::IsNullOrWhiteSpace($StartupLogFile)) {
        return
    }

    $script:StartupAttemptStartedAt = Get-Date
    try {
        $logDirectory = Split-Path -Parent $StartupLogFile
        if ($logDirectory) {
            New-Item -ItemType Directory -Force -Path $logDirectory | Out-Null
        }
        Start-Transcript -Path $StartupLogFile -Force | Out-Null
        $script:StartupTranscriptStarted = $true
        Write-Host "==> Startup log: $StartupLogFile"
    } catch {
        Write-Warning "Could not enable startup logging at ${StartupLogFile}: $($_.Exception.Message)"
    }
}

function Stop-StartupLogging {
    if (-not $StartupTranscriptStarted) {
        return
    }

    try {
        Stop-Transcript | Out-Null
    } catch {
        Write-Warning "Could not finalize startup log: $($_.Exception.Message)"
    }
}

function Show-RecentServiceLogs {
    param([int]$LineCount = 40)

    foreach ($entry in @(
        @{ Label = 'service error log'; Path = $ServiceErrorLogFile },
        @{ Label = 'service output log'; Path = $ServiceLogFile }
    )) {
        if (-not (Test-Path -LiteralPath $entry.Path)) {
            continue
        }

        $log = Get-Item -LiteralPath $entry.Path
        if ($log.Length -eq 0) {
            continue
        }
        if ($null -ne $StartupAttemptStartedAt -and $log.LastWriteTime -lt $StartupAttemptStartedAt.AddSeconds(-2)) {
            continue
        }

        Write-Host "==> Recent $($entry.Label): $($entry.Path)" -ForegroundColor Yellow
        Get-Content -LiteralPath $entry.Path -Tail $LineCount -ErrorAction SilentlyContinue |
            ForEach-Object { Write-Host $_ }
    }
}

function Get-HealthURL {
    param([int]$Port)

    return Get-ServiceURL -Port $Port -Path '/health'
}

function Invoke-Controller {
    param(
        [string[]]$Arguments,
        [switch]$AllowFailure,
        [switch]$Quiet
    )

    if ($Quiet) {
        & $PowerShellPath -NoProfile -ExecutionPolicy Bypass -File $ControlScript @Arguments *> $null
    } else {
        & $PowerShellPath -NoProfile -ExecutionPolicy Bypass -File $ControlScript @Arguments 2>&1 |
            ForEach-Object { Write-Host $_ }
    }
    $exitCode = $LASTEXITCODE
    if (-not $AllowFailure -and $exitCode -ne 0) {
        throw "Process controller command failed: $($Arguments -join ' ')"
    }
    return $exitCode
}

function Test-SourceRunning {
    $exitCode = Invoke-Controller -Arguments @('status') -AllowFailure -Quiet
    return $exitCode -eq 0
}

function Invoke-NativeCommand {
    param(
        [scriptblock]$Command,
        [string]$FailureMessage
    )

    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "$FailureMessage (exit code $LASTEXITCODE)"
    }
}

function Remove-StagedDirectory {
    param([string]$Path)

    if (-not $Path -or -not (Test-Path -LiteralPath $Path)) {
        return
    }

    $resolvedTempDir = [System.IO.Path]::GetFullPath($TempDir).TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    ) + [System.IO.Path]::DirectorySeparatorChar
    $resolvedPath = [System.IO.Path]::GetFullPath($Path)
    if (-not $resolvedPath.StartsWith($resolvedTempDir, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove staged directory outside ${TempDir}: $resolvedPath"
    }

    Remove-Item -LiteralPath $resolvedPath -Recurse -Force
}

function Build-App {
    param([string]$OutputPath)

    if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
        throw 'npm was not found. Install Node.js 22+ first.'
    }
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw 'go was not found. Install Go 1.24+ first.'
    }

    Write-Host '==> Building web panel'
    Push-Location $RootDir
    try {
        if ($env:CPA_MANAGER_PLUS_SKIP_NPM_CI -ne '1') {
            Invoke-NativeCommand { npm ci } 'npm ci failed'
        }
        Invoke-NativeCommand { npm run build } 'web build failed'
    } finally {
        Pop-Location
    }

    if (-not (Test-Path -LiteralPath $WebHTML)) {
        throw "Web build did not create $WebHTML"
    }

    $workDir = Join-Path $TempDir ("source-build." + [guid]::NewGuid().ToString('N'))
    $stagedServer = Join-Path $workDir 'manager-server'
    New-Item -ItemType Directory -Force -Path $stagedServer | Out-Null
    try {
        Write-Host '==> Staging Manager Server source'
        Get-ChildItem -LiteralPath $ServerSource -Force |
            Where-Object { $_.Name -ne 'node_modules' } |
            ForEach-Object {
                Copy-Item -LiteralPath $_.FullName -Destination $stagedServer -Recurse -Force
            }
        Copy-Item -LiteralPath $WebHTML -Destination (Join-Path $stagedServer 'internal/httpapi/web/management.html') -Force

        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $OutputPath) | Out-Null
        Remove-Item -LiteralPath $OutputPath -Force -ErrorAction SilentlyContinue
        Write-Host '==> Building Go service'
        Push-Location $stagedServer
        try {
            Write-Host '==> Testing database startup contracts'
            Invoke-NativeCommand {
                go test ./internal/outboxcontext ./internal/repository/sqlite
            } 'database startup contract tests failed'
            Invoke-NativeCommand {
                go build -trimpath -ldflags '-s -w' -o $OutputPath ./cmd/cpa-manager-plus
            } 'Go build failed'
        } finally {
            Pop-Location
        }
    } finally {
        Remove-StagedDirectory -Path $workDir
    }

    if (-not (Test-Path -LiteralPath $OutputPath)) {
        throw "Build completed without producing $OutputPath"
    }
    Write-Host "Build complete: $OutputPath"
}

function Assert-NormalDatabaseMode {
    param(
        [string]$StatusJSON,
        [string]$StatusURL
    )

    try {
        $status = $StatusJSON | ConvertFrom-Json
    } catch {
        throw "Service returned invalid status JSON: $statusURL"
    }
    if ($null -eq $status) {
        throw "Service returned empty status JSON: $statusURL"
    }
    $recoveryMode = $status.PSObject.Properties['recoveryMode']
    if ($null -ne $recoveryMode -and $recoveryMode.Value -eq $true) {
        throw "Service entered database recovery mode after startup: $statusURL"
    }
}

function Assert-ServiceReady {
    param([int]$Port)

    $headers = @{ Authorization = 'Bearer ' + (Get-EffectiveAdminKey) }
    $statusURL = Get-ServiceURL -Port $Port -Path '/status'
    try {
        $statusResponse = Invoke-WebRequest -UseBasicParsing -Uri $statusURL -Headers $headers -TimeoutSec 3
    } catch {
        throw "Authenticated status check failed: $statusURL"
    }
    if ($statusResponse.StatusCode -ne 200) {
        throw "Authenticated status check returned HTTP $($statusResponse.StatusCode): $statusURL"
    }
    Assert-NormalDatabaseMode -StatusJSON $statusResponse.Content -StatusURL $statusURL

    $configURL = Get-ServiceURL -Port $Port -Path '/usage-service/config'
    try {
        $configResponse = Invoke-WebRequest -UseBasicParsing -Uri $configURL -Headers $headers -TimeoutSec 3
    } catch {
        throw "Business data readiness check failed: $configURL"
    }
    if ($configResponse.StatusCode -ne 200) {
        throw "Business data readiness check returned HTTP $($configResponse.StatusCode): $configURL"
    }
}

function Wait-ForReady {
    param(
        [int]$Port,
        [int]$TimeoutSeconds = 60
    )

    $healthURL = Get-HealthURL $Port
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $lastReadinessError = ''
    do {
        if (-not (Test-SourceRunning)) {
            throw "Service process exited before readiness checks completed. See error log: $ServiceErrorLogFile"
        }
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri $healthURL -TimeoutSec 2
            if ($response.StatusCode -eq 200) {
                try {
                    Assert-ServiceReady -Port $Port
                    Write-Host "Ready: $healthURL"
                    return
                } catch {
                    $lastReadinessError = $_.Exception.Message
                    if ($lastReadinessError.StartsWith('Service entered database recovery mode')) {
                        throw
                    }
                }
            }
        } catch {
            if ($_.Exception.Message.StartsWith('Service entered database recovery mode')) {
                throw
            }
        }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)

    if (-not [string]::IsNullOrWhiteSpace($lastReadinessError)) {
        throw "Service did not become ready within $TimeoutSeconds seconds. $lastReadinessError"
    }
    throw "Service did not become healthy within $TimeoutSeconds seconds: $healthURL"
}

function Start-App {
    param([int]$Port)

    if (-not (Test-Path -LiteralPath $BinaryPath)) {
        Build-App -OutputPath $BinaryPath
    }
    Set-RuntimeEnvironment -Port $Port

    if (Test-SourceRunning) {
        $activePort = Get-StoredPort
        if ($null -ne $RequestedPort -and $RequestedPort -ne $activePort) {
            throw "Service is already running on port $activePort. Use restart --port $RequestedPort to change it."
        }
        Assert-ServiceReady -Port $activePort
        Write-Host "Source service is already running and ready: http://127.0.0.1:$activePort"
        return
    }

    Ensure-AdminKeyConfiguration

    try {
        Write-Host "==> Starting CPA Manager Plus on ${SourceHost}:$Port"
        [void](Invoke-Controller -Arguments @('start'))
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $PortFile) | Out-Null
        Set-Content -LiteralPath $PortFile -Value $Port -NoNewline
        Write-Host '==> Waiting for HTTP health and business-data readiness'
        Wait-ForReady -Port $Port
        Mark-AdminKeyStateInitialized
        Write-Host "Startup complete: http://127.0.0.1:$Port"
    } catch {
        [void](Invoke-Controller -Arguments @('stop') -AllowFailure -Quiet)
        Remove-Item -LiteralPath $PortFile -Force -ErrorAction SilentlyContinue
        throw
    }
}

function Stop-App {
    [void](Invoke-Controller -Arguments @('stop'))
    Remove-Item -LiteralPath $PortFile -Force -ErrorAction SilentlyContinue
}

function Show-Status {
    $port = Get-StoredPort
    Set-RuntimeEnvironment -Port $port
    [void](Invoke-Controller -Arguments @('status'))
    $url = Get-HealthURL $port
    try {
        $response = Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 3
        if ($response.StatusCode -ne 200) {
            throw "HTTP $($response.StatusCode)"
        }
        Write-Host "HTTP health: healthy ($url)"
    } catch {
        throw "Process is running but HTTP health failed: $url"
    }
    Assert-ServiceReady -Port $port
    Write-Host 'Business data readiness: ready'
}

function Activate-Candidate {
    param([int]$Port)

    $wasRunning = Test-SourceRunning
    if ($wasRunning) {
        Stop-App
    }

    $hadPreviousBuild = Test-Path -LiteralPath $BinaryPath
    Remove-Item -LiteralPath $PreviousBinaryPath -Force -ErrorAction SilentlyContinue
    if ($hadPreviousBuild) {
        Move-Item -LiteralPath $BinaryPath -Destination $PreviousBinaryPath -Force
    }
    Move-Item -LiteralPath $NextBinaryPath -Destination $BinaryPath -Force

    try {
        Start-App -Port $Port
        Remove-Item -LiteralPath $PreviousBinaryPath -Force -ErrorAction SilentlyContinue
    } catch {
        $activationError = $_.Exception.Message
        [void](Invoke-Controller -Arguments @('stop') -AllowFailure -Quiet)
        Remove-Item -LiteralPath $BinaryPath -Force -ErrorAction SilentlyContinue
        if ($hadPreviousBuild -and (Test-Path -LiteralPath $PreviousBinaryPath)) {
            Move-Item -LiteralPath $PreviousBinaryPath -Destination $BinaryPath -Force
            try {
                Start-App -Port $Port
                Write-Warning 'The new build failed to start; the previous build was restored.'
            } catch {
                Write-Warning "The previous build was restored but could not be started: $($_.Exception.Message)"
            }
        }
        throw "The new build failed to start: $activationError"
    }
}

$scriptExitCode = 0
try {
    Start-StartupLogging
    Parse-Arguments
    if ($StartupTranscriptStarted) {
        Write-Host "==> Requested action: $Action"
        Write-Host "==> Repository: $RootDir"
    }
    if ($Action -in @('help', '-h', '--help')) {
        Show-Usage
    } else {
        Ensure-Layout
        $port = if ($null -ne $RequestedPort) {
            $RequestedPort
        } elseif (Test-SourceRunning) {
            Get-StoredPort
        } else {
            Get-ConfiguredPort
        }
        Set-RuntimeEnvironment -Port $port

        switch ($Action) {
            'build' {
                if (Test-SourceRunning) {
                    throw "The source service is running. Use 'rebuild' to build a candidate before stopping it."
                }
                Build-App -OutputPath $BinaryPath
            }
            'start' { Start-App -Port $port }
            'stop' { Stop-App }
            'restart' {
                Stop-App
                Start-App -Port $port
            }
            'rebuild' {
                Remove-Item -LiteralPath $NextBinaryPath -Force -ErrorAction SilentlyContinue
                Build-App -OutputPath $NextBinaryPath
                Activate-Candidate -Port $port
            }
            'status' { Show-Status }
            'logs' {
                $logArgs = @('logs') + $ActionArguments
                [void](Invoke-Controller -Arguments $logArgs)
            }
            'admin-key' { Show-AdminKey }
        }
    }
    if ($StartupTranscriptStarted) {
        Write-Host '==> Startup action completed successfully'
    }
} catch {
    Write-Host "ERROR: $($_.Exception.Message)" -ForegroundColor Red
    if (-not [string]::IsNullOrWhiteSpace($StartupLogFile)) {
        Show-RecentServiceLogs
        Write-Host "Startup log: $StartupLogFile"
    }
    $scriptExitCode = 1
} finally {
    Stop-StartupLogging
}
exit $scriptExitCode
