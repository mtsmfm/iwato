# Try the OpenShell WSLc driver: build the gateway image, create the
# certificates, start the driver on Windows and the gateway in WSLc, then
# run a sandbox. From this folder in PowerShell:
#   powershell -ExecutionPolicy Bypass -File .\try.ps1
# Afterwards: wslc container remove --force openshell-gateway, and close the
# minimized driver window. Check the output and driver.log.
# -Release main tries OpenShell's rolling build of main instead of 0.1.6
# (its gateway, CLI, sandbox and supervisor; the state volume is separate).
param([int]$DriverPort = 17671, [ValidateSet("0.1.6", "main")][string]$Release = "0.1.6")
$ErrorActionPreference = "Continue"
# Pass the JSON quotes below the same way on Windows PowerShell and PowerShell 7.
$PSNativeCommandArgumentPassing = "Legacy"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $here
$image = "iwato-openshell-gateway:$Release"
$state = "openshell-gateway-state"
$runtimeImages = @()
if ($Release -eq "main") {
  $tag = "dev"; $state = "openshell-gateway-state-main"
  $runtimeImages = @("-sandbox-image", "iwato-openshell-sandbox:main", "-supervisor-image", "iwato-openshell-supervisor:main")
} else { $tag = "v$Release" }

"== wslc version"; wslc --version
"== network and state volume"
wslc network create openshell 2>&1 | Out-Null
wslc volume create $state 2>&1 | Out-Null
"== gateway image (the first build downloads OpenShell 0.1.6)"
wslc image build --tag $image --target gateway --build-arg "OPENSHELL_TAG=$tag" --file Dockerfile .
if ($LASTEXITCODE -ne 0) { "image build failed"; exit 1 }
if ($Release -eq "main") {
  foreach ($t in @("sandbox", "supervisor")) {
    wslc image build --tag "iwato-openshell-${t}:main" --target $t --build-arg "OPENSHELL_TAG=$tag" --file Dockerfile .
    if ($LASTEXITCODE -ne 0) { "$t image build failed"; exit 1 }
  }
}

"== certificates"
# Gateway PKI in the state volume; the supervisors' copy comes out here.
wslc container run --rm --mount type=volume,source=$state,target=/var/lib/openshell $image init
New-Item -ItemType Directory -Force gateway-tls | Out-Null
$tmp = wslc container create --mount type=volume,source=$state,target=/var/lib/openshell $image
$tmp = ($tmp | Select-Object -Last 1).Trim()
foreach ($f in @("ca.crt", "client/tls.crt", "client/tls.key")) {
  wslc container cp "${tmp}:/var/lib/openshell/tls/$f" (Join-Path "gateway-tls" ($f -replace "/", "-"))
}
wslc container remove --force $tmp | Out-Null
# The driver's listener and the relay's client certificate.
if (-not (Test-Path driver-tls\relay.pem)) { & .\openshell-driver-wslc.exe certs driver-tls }

# A driver left from an earlier run would hold the port.
Get-Process openshell-driver-wslc -ErrorAction SilentlyContinue | Stop-Process -Force
"== driver on Windows (tls://127.0.0.1:$DriverPort; log: driver.log)"
$driverArgs = @(
  "-listen", "tls://127.0.0.1:$DriverPort",
  "-tls-cert", "driver-tls\server.pem", "-tls-key", "driver-tls\server.key", "-tls-client-ca", "driver-tls\ca.pem",
  "-gateway-endpoint", "https://openshell-gateway:17670", "-network", "openshell",
  "-guest-tls-ca", "gateway-tls\ca.crt") + $runtimeImages
$driver = Start-Process -FilePath (Join-Path $here "openshell-driver-wslc.exe") -WorkingDirectory $here -PassThru -WindowStyle Minimized `
  -RedirectStandardError (Join-Path $here "driver.log") -ArgumentList $driverArgs
Start-Sleep -Seconds 2

"== gateway in WSLc"
wslc container remove --force openshell-gateway 2>&1 | Out-Null
wslc container create --name openshell-gateway --network openshell --publish 127.0.0.1:17670:17670 `
  --env "DRIVER_PORT=$DriverPort" --mount type=volume,source=$state,target=/var/lib/openshell $image | Out-Null
# The relay's client certificate goes in before the gateway starts.
wslc container cp driver-tls openshell-gateway:/etc/openshell/
wslc container start openshell-gateway
Start-Sleep -Seconds 8
wslc container logs --tail 15 openshell-gateway

"== sandbox: a command, then a persistent one with a volume, stop/start, then an interactive shell"
wslc container exec openshell-gateway openshell sandbox create --name probe --no-keep -- `
  sh -c "echo HELLO-FROM-WSLC-SANDBOX; id; cat /etc/resolv.conf; timeout 5 bash -c 'echo > /dev/tcp/github.com/443' && echo open || echo blocked"
wslc volume create openshell-try-data 2>&1 | Out-Null
# A new volume is root's; the sandbox runs as the image's user (1000) and the
# default policy lets it write only under /sandbox and /tmp.
wslc container run --rm --mount type=volume,source=openshell-try-data,target=/data ubuntu:24.04 chown 1000:1000 /data
wslc container exec openshell-gateway openshell sandbox create --name keep --detach `
  --driver-config-json '{\"wslc\":{\"mounts\":[{\"type\":\"volume\",\"source\":\"openshell-try-data\",\"target\":\"/sandbox/data\",\"read_only\":false}]}}' -- sleep infinity
Start-Sleep -Seconds 15
wslc container exec openshell-gateway openshell sandbox exec --name keep -- sh -c "date > /sandbox/data/stamp && echo WROTE; ls -l /sandbox/data"
wslc container exec openshell-gateway openshell sandbox stop keep
Start-Sleep -Seconds 5
wslc container exec openshell-gateway openshell sandbox start keep
Start-Sleep -Seconds 15
wslc container exec openshell-gateway openshell sandbox exec --name keep -- sh -c "echo AFTER-RESTART; cat /sandbox/data/stamp"
wslc container exec openshell-gateway openshell sandbox delete keep
wslc container exec openshell-gateway openshell sandbox delete demo 2>&1 | Out-Null
wslc container exec --interactive --tty openshell-gateway openshell sandbox create --name demo

"== driver log (tail)"; Get-Content -Encoding UTF8 (Join-Path $here "driver.log") -Tail 20
"(driver process $($driver.Id) is still running; stop it when done)"
