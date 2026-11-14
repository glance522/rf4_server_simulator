$ErrorActionPreference = 'Stop'
$output = & dumpbin /exports (Join-Path $env:SystemRoot 'System32/winmm.dll')
if ($LASTEXITCODE -ne 0) { throw 'Cannot read WinMM exports.' }
$entries = @()
foreach ($line in $output) {
    if ($line -match '^\s+(\d+)\s+[0-9A-F]+\s+([0-9A-F]{8})\s+(\S+)\s*$') {
        $entries += [pscustomobject]@{Ordinal=[int]$Matches[1]; Rva=$Matches[2]; Name=$Matches[3]}
    } elseif ($line -match '^\s+(\d+)\s+([0-9A-F]{8})\s+\[NONAME\]\s*$') {
        $entries += [pscustomobject]@{Ordinal=[int]$Matches[1]; Rva=$Matches[2]; Name=$null}
    }
}
if ($entries.Count -lt 100) { throw 'Unexpected WinMM exports.' }
$lines = @('LIBRARY winmm', 'EXPORTS')
$imports = @('LIBRARY rf4_gear_winmm_system.dll', 'EXPORTS')
foreach ($entry in ($entries | Sort-Object Ordinal)) {
    if ($entry.Name) {
        $lines += "    $($entry.Name)=rf4_gear_winmm_system.$($entry.Name) @$($entry.Ordinal)"
        $imports += "    $($entry.Name) @$($entry.Ordinal)"
    } else {
        $alias = $entries | Where-Object { $_.Name -and $_.Rva -eq $entry.Rva } | Select-Object -First 1
        if (-not $alias) { throw 'Unsupported ordinal-only export.' }
        $lines += "    ordinal_$($entry.Ordinal)=rf4_gear_winmm_system.$($alias.Name) @$($entry.Ordinal) NONAME"
        $imports += "    ordinal_$($entry.Ordinal) @$($entry.Ordinal) NONAME"
    }
}
New-Item -ItemType Directory -Force (Join-Path $PSScriptRoot 'out') | Out-Null
[IO.File]::WriteAllLines((Join-Path $PSScriptRoot 'out/winmm.def'), $lines, [Text.Encoding]::ASCII)
[IO.File]::WriteAllLines((Join-Path $PSScriptRoot 'out/system.def'), $imports, [Text.Encoding]::ASCII)
