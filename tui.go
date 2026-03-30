package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type scannerUI struct {
	app *tview.Application

	headerView *tview.TextView

	form *tview.Form
	startButton  *tview.Button
	stopButton   *tview.Button
	exportButton *tview.Button

	targetField      *tview.InputField
	portsField       *tview.InputField
	customTimeoutBox *tview.Checkbox
	timeoutField     *tview.InputField
	workersField     *tview.InputField
	portWorkersField *tview.InputField
	verboseBox       *tview.Checkbox

	progressView *tview.TextView
	timingView   *tview.TextView
	statusView   *tview.TextView
	resultsPane  *tview.Flex
	resultsTable *tview.Table
	resultsScroll *tview.TextView

	cancel     context.CancelFunc
	running    bool
	resultRow  int
	resultsMu  sync.Mutex
	results    []HostResult
	lastStats  Stats
}

func runTUI() error {
	ui := newScannerUI()
	return ui.app.SetRoot(ui.layout(), true).EnableMouse(true).Run()
}

func newScannerUI() *scannerUI {
	ui := &scannerUI{
		app:          tview.NewApplication(),
		headerView:   tview.NewTextView(),
		progressView: tview.NewTextView(),
		timingView:   tview.NewTextView(),
		statusView:   tview.NewTextView(),
		resultsPane:  tview.NewFlex(),
		resultsTable: tview.NewTable(),
		resultsScroll: tview.NewTextView(),
	}

	ui.targetField = tview.NewInputField().
		SetLabel("Target(s) ").
		SetFieldWidth(0).
		SetText("127.0.0.1")
	ui.portsField = tview.NewInputField().
		SetLabel("Ports ").
		SetFieldWidth(0).
		SetText("")
	ui.customTimeoutBox = tview.NewCheckbox().
		SetLabel("Custom timeout (-t) ").
		SetChecked(false)
	ui.timeoutField = tview.NewInputField().
		SetLabel("Timeout ").
		SetFieldWidth(0).
		SetText("2s")
	ui.workersField = tview.NewInputField().
		SetLabel("Host workers ").
		SetFieldWidth(0).
		SetText(strconv.Itoa(runtime.NumCPU() * 16))
	ui.portWorkersField = tview.NewInputField().
		SetLabel("Port workers ").
		SetFieldWidth(0).
		SetText("64")
	ui.verboseBox = tview.NewCheckbox().
		SetLabel("Verbose ").
		SetChecked(false)

	ui.headerView.
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetBorder(true).
		SetTitle(" GoScanner ")
	ui.progressView.
		SetDynamicColors(true).
		SetBorder(true).
		SetTitle(" Progress ")
	ui.timingView.
		SetDynamicColors(true).
		SetBorder(true).
		SetTitle(" Timing ")
	ui.statusView.
		SetDynamicColors(true).
		SetBorder(true).
		SetTitle(" Status ")

	ui.resultsTable.
		SetBorders(true).
		SetSelectable(true, false).
		SetFixed(1, 0).
		SetEvaluateAllRows(false)
	ui.resultsTable.SetSelectionChangedFunc(func(row, column int) {
		ui.updateResultsScroll()
	})
	ui.resultsScroll.
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	ui.resultsPane.SetDirection(tview.FlexColumn)
	ui.resultsPane.SetBorder(true)
	ui.resultsPane.SetTitle(" Results ")
	ui.resultsPane.AddItem(ui.resultsTable, 0, 1, true)
	ui.resultsPane.AddItem(ui.resultsScroll, 1, 0, false)
	ui.resetResultsTable()
	ui.setIdleState()
	ui.headerView.SetText("[blue::b]GoScanner v0.1 / April 2026[-:-:-] [white]- Developed By Majed Khaznadar ([blue::u]https://www.majed.xyz[-:-:-][white])")

	return ui
}

func (ui *scannerUI) layout() tview.Primitive {
	form := tview.NewForm().
		AddFormItem(ui.targetField).
		AddFormItem(ui.portsField).
		AddFormItem(ui.customTimeoutBox).
		AddFormItem(ui.timeoutField).
		AddFormItem(ui.workersField).
		AddFormItem(ui.portWorkersField).
		AddFormItem(ui.verboseBox).
		AddButton("Start Scan", ui.startScan).
		AddButton("Stop", ui.stopScan).
		AddButton("Export TXT", ui.exportResults).
		AddButton("Quit", func() { ui.app.Stop() })
	ui.form = form
	ui.startButton = form.GetButton(0)
	ui.stopButton = form.GetButton(1)
	ui.exportButton = form.GetButton(2)

	form.SetBorder(true).
		SetTitle(" Scan Setup ").
		SetTitleAlign(tview.AlignLeft)
	form.SetButtonsAlign(tview.AlignLeft)
	ui.updateButtonStates()

	rightTop := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.progressView, 3, 0, false).
		AddItem(ui.timingView, 5, 0, false).
		AddItem(ui.statusView, 3, 0, false)

	right := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(rightTop, 0, 1, false).
		AddItem(ui.resultsPane, 0, 3, true)

	body := tview.NewFlex().
		AddItem(form, 48, 0, true).
		AddItem(right, 0, 1, false)

	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.headerView, 3, 0, false).
		AddItem(body, 0, 1, true)
}

func (ui *scannerUI) startScan() {
	if ui.running {
		ui.setStatus("[yellow]A scan is already running.")
		return
	}

	cfg, err := ui.readConfigFromForm()
	if err != nil {
		ui.setStatus(fmt.Sprintf("[red]%v", err))
		return
	}

	ui.running = true
	ui.resultRow = 1
	ui.results = nil
	ui.lastStats = Stats{}
	ui.resetResultsTable()
	ui.setStatus("[green]Scan started.")
	ui.updateButtonStates()

	ctx, cancel := context.WithCancel(context.Background())
	ui.cancel = cancel

	go func() {
		err := runScan(ctx, cfg, ScanHooks{
			OnProgress: func(stats Stats) {
				ui.app.QueueUpdateDraw(func() {
					ui.lastStats = stats
					ui.renderProgress(stats)
					ui.renderTiming(stats)
				})
			},
			OnResult: func(result HostResult, stats Stats) {
				ui.app.QueueUpdateDraw(func() {
					ui.results = append(ui.results, result)
					ui.lastStats = stats
					ui.appendResult(result)
					ui.renderProgress(stats)
					ui.renderTiming(stats)
					ui.setStatus(fmt.Sprintf("[green]Found %d host(s) with open ports.", stats.FoundHosts))
					ui.updateButtonStates()
				})
			},
			OnComplete: func(results []HostResult, stats Stats) {
				ui.app.QueueUpdateDraw(func() {
					ui.running = false
					ui.cancel = nil
					ui.results = append([]HostResult(nil), results...)
					ui.lastStats = stats
					ui.renderProgress(stats)
					ui.renderTiming(stats)
					ui.setStatus(fmt.Sprintf("[green]Scan complete. Hosts found: %d | Open ports: %d", stats.FoundHosts, stats.OpenPorts))
					ui.updateButtonStates()
				})
			},
		})

		if err != nil {
			ui.app.QueueUpdateDraw(func() {
				ui.running = false
				ui.cancel = nil
				ui.setStatus(fmt.Sprintf("[red]%v", err))
				ui.updateButtonStates()
			})
		}
	}()
}

func (ui *scannerUI) stopScan() {
	if ui.cancel == nil {
		ui.setStatus("[yellow]No running scan to stop.")
		return
	}
	ui.cancel()
	ui.cancel = nil
	ui.running = false
	ui.setStatus("[yellow]Stopping scan...")
	ui.updateButtonStates()
}

func (ui *scannerUI) readConfigFromForm() (Config, error) {
	cfg := defaultConfig()
	cfg.Targets = splitTargets(ui.targetField.GetText())
	if len(cfg.Targets) == 0 {
		return Config{}, fmt.Errorf("enter at least one IP, domain, CIDR, or range")
	}

	if portsText := strings.TrimSpace(ui.portsField.GetText()); portsText != "" {
		cfg.Ports = parsePortsStr(portsText)
		if len(cfg.Ports) == 0 {
			return Config{}, fmt.Errorf("enter a valid port list or range")
		}
	}

	if ui.customTimeoutBox.IsChecked() {
		timeout, err := time.ParseDuration(strings.TrimSpace(ui.timeoutField.GetText()))
		if err != nil {
			return Config{}, fmt.Errorf("timeout must be a valid duration like 500ms or 2s")
		}
		cfg.Timeout = timeout
	}

	workers, err := strconv.Atoi(strings.TrimSpace(ui.workersField.GetText()))
	if err != nil || workers < 1 {
		return Config{}, fmt.Errorf("host workers must be a positive number")
	}
	cfg.Workers = workers

	portWorkers, err := strconv.Atoi(strings.TrimSpace(ui.portWorkersField.GetText()))
	if err != nil || portWorkers < 1 {
		return Config{}, fmt.Errorf("port workers must be a positive number")
	}
	cfg.PortWorkers = portWorkers
	cfg.Verbose = ui.verboseBox.IsChecked()

	return cfg, nil
}

func (ui *scannerUI) resetResultsTable() {
	ui.resultsTable.Clear()
	headers := []string{"Host", "Port", "State", "Service", "Latency"}
	for col, header := range headers {
		ui.resultsTable.SetCell(0, col,
			ui.newHeaderCell(header, col))
	}
	ui.resultRow = 1
	ui.updateResultsScroll()
}

func (ui *scannerUI) appendResult(result HostResult) {
	ui.resultsMu.Lock()
	defer ui.resultsMu.Unlock()

	var openPorts []int
	for port, status := range result.Ports {
		if status.State == "open" {
			openPorts = append(openPorts, port)
		}
	}
	sort.Ints(openPorts)

	for i, port := range openPorts {
		status := result.Ports[port]
		host := result.IP
		latency := ""
		if i > 0 {
			host = ""
		}
		if i == 0 && result.Latency > 0 {
			latency = result.Latency.Round(time.Millisecond).String()
		}

		row := ui.resultRow
		ui.resultsTable.SetCell(row, 0, ui.newDataCell(host, tview.AlignLeft, 2, 28))
		ui.resultsTable.SetCell(row, 1, ui.newDataCell(strconv.Itoa(port), tview.AlignCenter, 1, 8))
		ui.resultsTable.SetCell(row, 2, ui.newDataCell(strings.ToUpper(status.State), tview.AlignCenter, 1, 8))
		ui.resultsTable.SetCell(row, 3, ui.newDataCell(fallbackService(status.Service), tview.AlignLeft, 2, 24))
		ui.resultsTable.SetCell(row, 4, ui.newDataCell(latency, tview.AlignCenter, 1, 10))
		ui.resultRow++
	}
	if ui.resultRow > 1 {
		ui.resultsTable.Select(ui.resultRow-1, 0)
	}
	ui.updateResultsScroll()
}

func (ui *scannerUI) renderProgress(stats Stats) {
	ui.progressView.Clear()

	const width = 34

	progressTotal := stats.TotalPorts
	progressDone := stats.ScannedPorts
	if progressTotal == 0 {
		progressTotal = stats.TotalHosts
		progressDone = stats.ScannedHosts
	}

	filled := 0
	percent := 0.0
	if progressTotal > 0 {
		filled = int((progressDone * width) / progressTotal)
		percent = float64(progressDone) / float64(progressTotal) * 100
	}
	if filled > width {
		filled = width
	}

	bar := strings.Repeat("=", filled) + strings.Repeat("-", width-filled)
	status := "Idle"
	if ui.running {
		status = "Running"
	} else if !stats.EndTime.IsZero() {
		status = "Completed"
	}

	fmt.Fprintf(ui.progressView,
		"[blue::b]%s[-:-:-] [%s::b][%s][-:-:-] [white::b]%3.0f%%[-:-:-]\n[white]Ports: %d/%d  Hosts: %d/%d  Found: %d",
		status,
		statusColor(status), bar, percent,
		stats.ScannedPorts, stats.TotalPorts,
		stats.ScannedHosts, stats.TotalHosts,
		stats.FoundHosts,
	)
}

func (ui *scannerUI) renderTiming(stats Stats) {
	ui.timingView.Clear()

	startText := "-"
	elapsedText := "0s"
	endText := "-"

	if !stats.StartTime.IsZero() {
		startText = stats.StartTime.Format("2006-01-02 15:04:05")
		elapsed := time.Since(stats.StartTime)
		if !stats.EndTime.IsZero() {
			elapsed = stats.EndTime.Sub(stats.StartTime)
			endText = stats.EndTime.Format("2006-01-02 15:04:05")
		}
		elapsedText = elapsed.Round(time.Second).String()
	}

	fmt.Fprintf(ui.timingView,
		"[white::b]Start:[-:-:-] %s\n[white::b]Elapsed:[-:-:-] %s\n[white::b]End:[-:-:-] %s",
		startText, elapsedText, endText,
	)
}

func (ui *scannerUI) setIdleState() {
	ui.progressView.SetText("[blue::b]Idle[-:-:-] [white]Fill the form on the left and start a scan.")
	ui.timingView.SetText("[white::b]Start:[-:-:-] -\n[white::b]Elapsed:[-:-:-] 0s\n[white::b]End:[-:-:-] -")
	ui.statusView.SetText("[yellow]Ready. Start a scan to see live progress and completion state.")
}

func (ui *scannerUI) setStatus(message string) {
	ui.statusView.SetText(message)
}

func statusColor(status string) string {
	switch status {
	case "Running":
		return "yellow"
	case "Completed":
		return "green"
	default:
		return "blue"
	}
}

func fallbackService(service string) string {
	if strings.TrimSpace(service) == "" {
		return "-"
	}
	return service
}

func (ui *scannerUI) updateButtonStates() {
	if ui.startButton != nil {
		ui.startButton.SetDisabled(ui.running)
	}
	if ui.stopButton != nil {
		ui.stopButton.SetDisabled(!ui.running)
	}
	if ui.exportButton != nil {
		ui.exportButton.SetDisabled(len(ui.results) == 0)
	}
}

func (ui *scannerUI) exportResults() {
	if len(ui.results) == 0 {
		ui.setStatus("[yellow]No results available to export yet.")
		return
	}

	filename := filepath.Join(".", fmt.Sprintf("goscanner-results-%s.txt", time.Now().Format("20060102-150405")))
	content := ui.buildExportText()
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		ui.setStatus(fmt.Sprintf("[red]Failed to export results: %v", err))
		return
	}

	ui.setStatus(fmt.Sprintf("[green]Results exported to %s", filename))
}

func (ui *scannerUI) buildExportText() string {
	var b strings.Builder

	b.WriteString("GoScanner v0.1 / April 2026\n")
	b.WriteString("Developed By Majed Khaznadar\n")
	b.WriteString("https://www.majed.xyz\n")
	b.WriteString(strings.Repeat("=", 72))
	b.WriteString("\n")

	if !ui.lastStats.StartTime.IsZero() {
		b.WriteString(fmt.Sprintf("Start Time: %s\n", ui.lastStats.StartTime.Format("2006-01-02 15:04:05")))
	}
	if !ui.lastStats.EndTime.IsZero() {
		b.WriteString(fmt.Sprintf("End Time: %s\n", ui.lastStats.EndTime.Format("2006-01-02 15:04:05")))
		b.WriteString(fmt.Sprintf("Elapsed: %s\n", ui.lastStats.EndTime.Sub(ui.lastStats.StartTime).Round(time.Second)))
	} else if !ui.lastStats.StartTime.IsZero() {
		b.WriteString(fmt.Sprintf("Elapsed: %s\n", time.Since(ui.lastStats.StartTime).Round(time.Second)))
	}
	b.WriteString(fmt.Sprintf("Hosts Found: %d\n", ui.lastStats.FoundHosts))
	b.WriteString(fmt.Sprintf("Open Ports: %d\n", ui.lastStats.OpenPorts))
	b.WriteString(strings.Repeat("-", 72))
	b.WriteString("\n")
	b.WriteString("HOST            PORT    STATE   SERVICE          LATENCY\n")
	b.WriteString(strings.Repeat("-", 72))
	b.WriteString("\n")

	for _, result := range ui.results {
		var openPorts []int
		for port, status := range result.Ports {
			if status.State == "open" {
				openPorts = append(openPorts, port)
			}
		}
		sort.Ints(openPorts)

		for i, port := range openPorts {
			host := result.IP
			latency := ""
			if i > 0 {
				host = ""
			}
			if i == 0 && result.Latency > 0 {
				latency = result.Latency.Round(time.Millisecond).String()
			}
			b.WriteString(fmt.Sprintf("%-15s %-7d %-7s %-16s %s\n",
				host,
				port,
				"OPEN",
				fallbackService(result.Ports[port].Service),
				latency,
			))
		}
	}

	return b.String()
}

func (ui *scannerUI) newHeaderCell(text string, col int) *tview.TableCell {
	bgColors := []tcell.Color{
		tcell.NewHexColor(0x0F4C81),
		tcell.NewHexColor(0x1B6CA8),
		tcell.NewHexColor(0x2A7F62),
		tcell.NewHexColor(0x7A5C17),
		tcell.NewHexColor(0x5C4B8A),
	}

	expansion := 1
	maxWidth := 12
	align := tview.AlignCenter
	switch col {
	case 0:
		expansion = 2
		maxWidth = 28
		align = tview.AlignLeft
	case 3:
		expansion = 2
		maxWidth = 24
		align = tview.AlignLeft
	}

	return tview.NewTableCell(text).
		SetSelectable(false).
		SetAttributes(tcell.AttrBold).
		SetTextColor(tcell.ColorWhite).
		SetBackgroundColor(bgColors[col%len(bgColors)]).
		SetAlign(align).
		SetExpansion(expansion).
		SetMaxWidth(maxWidth)
}

func (ui *scannerUI) newDataCell(text string, align, expansion, maxWidth int) *tview.TableCell {
	return tview.NewTableCell(text).
		SetAlign(align).
		SetExpansion(expansion).
		SetMaxWidth(maxWidth)
}

func (ui *scannerUI) updateResultsScroll() {
	ui.resultsScroll.Clear()

	totalRows := ui.resultsTable.GetRowCount()
	if totalRows <= 1 {
		ui.resultsScroll.SetText("[gray] ")
		return
	}

	_, _, _, height := ui.resultsTable.GetRect()
	visibleRows := height - 1
	if visibleRows < 1 {
		visibleRows = 1
	}

	offset, _ := ui.resultsTable.GetOffset()
	totalDataRows := totalRows - 1

	if totalDataRows <= visibleRows {
		ui.resultsScroll.SetText(strings.Repeat("[gray]│\n", visibleRows))
		return
	}

	thumbSize := visibleRows * visibleRows / totalDataRows
	if thumbSize < 1 {
		thumbSize = 1
	}
	maxThumbTop := visibleRows - thumbSize
	thumbTop := 0
	if maxThumbTop > 0 {
		thumbTop = offset * maxThumbTop / (totalDataRows - visibleRows)
	}

	var b strings.Builder
	for i := 0; i < visibleRows; i++ {
		if i >= thumbTop && i < thumbTop+thumbSize {
			b.WriteString("[blue]█")
		} else {
			b.WriteString("[gray]│")
		}
		if i < visibleRows-1 {
			b.WriteByte('\n')
		}
	}
	ui.resultsScroll.SetText(b.String())
}
