# GoScanner

GoScanner is a terminal UI port scanner written in Go. It combines a fast concurrent TCP scanner with a live TUI so you can enter targets on the left, watch scan progress on the right, and export results to a text file when the scan is done.

## Overview

The app scans:

- Single IPs such as `192.168.1.10`
- Domains such as `example.com`
- IP ranges such as `192.168.1.1-254`
- CIDR blocks such as `192.168.1.0/24`

It supports:

- Specific ports like `22,80,443`
- Port ranges like `1-65535`
- Mixed syntax like `22,80,443,8000-8100`

The scan method is currently TCP connect scanning:

- If a TCP connection succeeds, the port is marked `OPEN`
- Otherwise, it is marked `CLOSED`

## TUI Layout

The interface is split into two sides.

Left side:

- Target / domain / range input
- Ports input
- Custom timeout checkbox with timeout textbox
- Host worker count
- Port worker count
- Verbose checkbox
- `Start Scan`
- `Stop`
- `Export TXT`
- `Quit`

Right side:

- Progress panel
- Timing panel
- Status panel
- Results pane with a bordered table
- Vertical scroll indicator beside the results

Top header:

- `GoScanner v0.1 / April 2026 - Developed By Majed Khaznadar (https://www.majed.xyz)`

## Scan Workflow

When you click `Start Scan`:

- The `Start Scan` button is disabled
- The `Stop` button becomes enabled
- The scan starts immediately
- The progress panel shows live progress
- The timing panel shows start time, elapsed time, and end time
- Open-port results are appended live into the results table

When the scan finishes:

- The status changes to completed
- The timing panel shows the final end time
- `Start Scan` becomes enabled again
- `Stop` becomes disabled
- `Export TXT` stays enabled if results were found

When you click `Stop`:

- The running scan is cancelled
- The UI returns to an idle-ready state for another run

## Progress And Results

The progress panel shows:

- Running / completed / idle state
- A progress bar
- Percentage complete
- Ports scanned
- Hosts scanned
- Number of hosts with open ports

The timing panel shows:

- Start time
- Elapsed duration
- End time

The results table shows:

- `Host`
- `Port`
- `State`
- `Service`
- `Latency`

The table headers are colorized, the table is bordered, and the right-side scrollbar indicates how far you have scrolled through the result list.

## Export

Clicking `Export TXT` creates a timestamped text file in the project folder:

`goscanner-results-YYYYMMDD-HHMMSS.txt`

The export includes:

- App header
- Developer link
- Start time
- End time
- Elapsed time
- Hosts found
- Open-port totals
- Result rows

## Default Behavior

If the ports field is left empty, GoScanner uses this default port set:

`21, 22, 23, 25, 53, 80, 110, 111, 135, 139, 143, 443, 993, 995, 1723, 3306, 3389, 5432, 5900, 8080`

Default engine settings:

- Timeout: `2s`
- Host workers: `runtime.NumCPU() * 16`
- Port workers: `64`

## Build And Run

Build:

```bash
go build -o ipscanner.exe .
```

Run:

```bash
ipscanner.exe
```

Or directly from source:

```bash
go run .
```

## Project Structure

- [main.go](/c:/Users/Majed/Desktop/development/Go/GoScan/main.go): app entrypoint
- [scanner.go](/c:/Users/Majed/Desktop/development/Go/GoScan/scanner.go): scan engine, parsing, concurrency, and result tracking
- [tui.go](/c:/Users/Majed/Desktop/development/Go/GoScan/tui.go): terminal UI, buttons, progress panels, result table, scrollbar, and TXT export
- [go.mod](/c:/Users/Majed/Desktop/development/Go/GoScan/go.mod): module and dependencies

## Notes

- Full-range scans such as `1-65535` are supported, but they naturally take longer than the default port list.
- Service names are shown for a built-in set of common ports.
- Results are exported only for discovered open-port hosts currently shown by the UI.
