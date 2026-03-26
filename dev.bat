@echo off

REM Smart parameter detection
set DEBUG_MODE=
set PLATFORM=pc

if /i "%1"=="debug" set DEBUG_MODE=debug
if /i "%2"=="debug" set DEBUG_MODE=debug

if /i "%1"=="pc" set PLATFORM=pc
if /i "%2"=="pc" set PLATFORM=pc
if /i "%1"=="mac" set PLATFORM=mac
if /i "%2"=="mac" set PLATFORM=mac

REM If first param is debug and no platform specified, default to pc
if /i "%1"=="debug" if "%PLATFORM%"=="" set PLATFORM=pc

if /i "%DEBUG_MODE%"=="debug" (
    echo Starting Hover development environment ^(platform: %PLATFORM%, debug mode^)...
    set LOG_LEVEL=debug
) else (
    echo Starting Hover development environment ^(platform: %PLATFORM%^)...
    set LOG_LEVEL=info
)

REM Check if Docker is running
docker ps >nul 2>&1
if errorlevel 1 (
    echo Error: Docker Desktop is not running. Please start Docker Desktop first.
    echo Download: https://docs.docker.com/desktop/
    pause
    exit /b 1
)

REM Check if Supabase CLI is installed
supabase --version >nul 2>&1
if errorlevel 1 (
    echo Error: Supabase CLI is not installed.
    echo Install with: npm install -g supabase
    echo Or download from: https://supabase.com/docs/guides/cli
    pause
    exit /b 1
)

REM Configure Air for platform
if /i "%PLATFORM%"=="mac" (
    echo Configuring Air for Mac/Linux...
    powershell -Command "(gc .air.toml) -replace '^cmd = \"go build -o \\./tmp/main\\.exe', '# cmd = \"go build -o ./tmp/main.exe' -replace '^bin = \"tmp/main\\.exe\"', '# bin = \"tmp/main.exe\"' -replace '^# cmd = \"go build -o \\./tmp/main', 'cmd = \"go build -o ./tmp/main' -replace '^# bin = \"tmp/main\"', 'bin = \"tmp/main\"' | sc .air.toml"
) else (
    echo Configuring Air for Windows...
    powershell -Command "(gc .air.toml) -replace '^# cmd = \"go build -o \\./tmp/main\"', '# cmd = \"go build -o ./tmp/main\"' -replace '^# bin = \"tmp/main\"', '# bin = \"tmp/main\"' -replace '^cmd = \"go build -o \\./tmp/main\\.exe', 'cmd = \"go build -o ./tmp/main.exe' -replace '^bin = \"tmp/main\\.exe\"', 'bin = \"tmp/main.exe\"' | sc .air.toml"
)

REM Start Supabase (will be no-op if already running)
echo Starting local Supabase...
supabase start

REM Start Air with hot reloading and migration watching
echo Starting development server with hot reloading...
echo Watching for migration changes - will auto-reset database when needed...

REM Start Air in background and watch for migration changes
start /b air

REM Watch for migration changes and auto-reset database
powershell -Command "$lastWrite = (Get-ChildItem supabase/migrations/*.sql | Sort-Object LastWriteTime -Descending | Select-Object -First 1).LastWriteTime; while($true) { Start-Sleep 2; $newWrite = (Get-ChildItem supabase/migrations/*.sql | Sort-Object LastWriteTime -Descending | Select-Object -First 1).LastWriteTime; if($newWrite -gt $lastWrite) { Write-Host 'Migration change detected - resetting database...'; supabase db reset; $lastWrite = $newWrite } }"