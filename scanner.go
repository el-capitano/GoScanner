package main

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config holds scanner configuration.
type Config struct {
	Targets       []string
	Ports         []int
	Timeout       time.Duration
	Workers       int
	PortWorkers   int
	RateLimit     int
	ScanType      string
	LiveInterface string
	Verbose       bool
}

// HostResult holds scan results for a single host.
type HostResult struct {
	IP      string
	Ports   map[int]PortStatus
	Open    int
	Closed  int
	Latency time.Duration
}

// PortStatus represents a port scan result.
type PortStatus struct {
	State   string
	Service string
	Banner  string
}

// Stats tracks scanning statistics.
type Stats struct {
	TotalHosts   int64
	ScannedHosts int64
	TotalPorts   int64
	ScannedPorts int64
	FoundHosts   int64
	OpenPorts    int64
	ClosedPorts  int64
	StartTime    time.Time
	EndTime      time.Time
}

type ScanHooks struct {
	OnProgress func(Stats)
	OnResult   func(HostResult, Stats)
	OnComplete func([]HostResult, Stats)
}

func defaultConfig() Config {
	return Config{
		Timeout:     2 * time.Second,
		Workers:     runtime.NumCPU() * 16,
		PortWorkers: 64,
		RateLimit:   10000,
		ScanType:    "connect",
		Ports:       defaultPorts(),
	}
}

func defaultPorts() []int {
	return []int{
		21, 22, 23, 25, 53, 80, 110, 111, 135, 139, 143, 443, 993, 995,
		1723, 3306, 3389, 5432, 5900, 8080,
	}
}

func normalizeConfig(cfg Config) Config {
	def := defaultConfig()

	if cfg.Timeout <= 0 {
		cfg.Timeout = def.Timeout
	}
	if cfg.Workers < 1 {
		cfg.Workers = def.Workers
	}
	if cfg.PortWorkers < 1 {
		cfg.PortWorkers = def.PortWorkers
	}
	if strings.TrimSpace(cfg.ScanType) == "" {
		cfg.ScanType = def.ScanType
	}
	if len(cfg.Ports) == 0 {
		cfg.Ports = defaultPorts()
	}

	return cfg
}

func runScan(ctx context.Context, cfg Config, hooks ScanHooks) error {
	cfg = normalizeConfig(cfg)
	targets := parseTargets(cfg.Targets)
	if len(targets) == 0 {
		return fmt.Errorf("enter at least one valid IP, domain, CIDR, or range")
	}

	stats := &Stats{
		TotalHosts: int64(len(targets)),
		TotalPorts: int64(len(targets) * len(cfg.Ports)),
		StartTime:  time.Now(),
	}

	results := make(chan HostResult, cfg.Workers*4)
	targetsCh := make(chan string, cfg.Workers*4)
	var workerWG sync.WaitGroup
	var resultWG sync.WaitGroup

	if hooks.OnProgress != nil {
		hooks.OnProgress(snapshotStats(stats))
	}

	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if hooks.OnProgress != nil {
					hooks.OnProgress(snapshotStats(stats))
				}
			case <-progressDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	var openHosts []HostResult
	resultWG.Add(1)
	go func() {
		defer resultWG.Done()
		for result := range results {
			if result.Open > 0 {
				atomic.AddInt64(&stats.FoundHosts, 1)
				openHosts = append(openHosts, result)
				if hooks.OnResult != nil {
					hooks.OnResult(result, snapshotStats(stats))
				}
			}
		}
	}()

	for i := 0; i < cfg.Workers; i++ {
		workerWG.Add(1)
		go worker(ctx, targetsCh, cfg.Ports, cfg.Timeout, cfg.PortWorkers, results, &workerWG, stats)
	}

	go func() {
		defer close(targetsCh)
		for _, target := range targets {
			select {
			case <-ctx.Done():
				return
			case targetsCh <- target:
			}
		}
	}()

	workerWG.Wait()
	close(results)
	resultWG.Wait()
	close(progressDone)

	sort.Slice(openHosts, func(i, j int) bool {
		return openHosts[i].Open > openHosts[j].Open
	})

	stats.EndTime = time.Now()
	finalStats := snapshotStats(stats)
	finalStats.EndTime = stats.EndTime
	if hooks.OnProgress != nil {
		hooks.OnProgress(finalStats)
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(openHosts, finalStats)
	}

	return nil
}

func snapshotStats(stats *Stats) Stats {
	return Stats{
		TotalHosts:   atomic.LoadInt64(&stats.TotalHosts),
		ScannedHosts: atomic.LoadInt64(&stats.ScannedHosts),
		TotalPorts:   atomic.LoadInt64(&stats.TotalPorts),
		ScannedPorts: atomic.LoadInt64(&stats.ScannedPorts),
		FoundHosts:   atomic.LoadInt64(&stats.FoundHosts),
		OpenPorts:    atomic.LoadInt64(&stats.OpenPorts),
		ClosedPorts:  atomic.LoadInt64(&stats.ClosedPorts),
		StartTime:    stats.StartTime,
		EndTime:      stats.EndTime,
	}
}

func worker(ctx context.Context, targets <-chan string, ports []int, timeout time.Duration, portWorkers int,
	results chan<- HostResult, wg *sync.WaitGroup, stats *Stats) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case ip, ok := <-targets:
			if !ok {
				return
			}
			start := time.Now()
			res := scanHost(ctx, ip, ports, timeout, portWorkers, stats)
			res.Latency = time.Since(start)
			select {
			case <-ctx.Done():
				return
			case results <- res:
			}
		}
	}
}

func scanHost(ctx context.Context, ipStr string, ports []int, timeout time.Duration, portWorkers int, stats *Stats) HostResult {
	result := HostResult{
		IP:    ipStr,
		Ports: make(map[int]PortStatus),
	}

	if len(ports) == 0 {
		atomic.AddInt64(&stats.ScannedHosts, 1)
		return result
	}

	if portWorkers <= 0 {
		portWorkers = 1
	}
	if portWorkers > len(ports) {
		portWorkers = len(ports)
	}

	type portResult struct {
		port   int
		status PortStatus
	}

	jobs := make(chan int, len(ports))
	portResults := make(chan portResult, len(ports))
	var portWG sync.WaitGroup

	for i := 0; i < portWorkers; i++ {
		portWG.Add(1)
		go func() {
			defer portWG.Done()
			for port := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				portResults <- portResult{
					port:   port,
					status: scanPort(ctx, ipStr, port, timeout),
				}
			}
		}()
	}

	for _, port := range ports {
		select {
		case <-ctx.Done():
			close(jobs)
			portWG.Wait()
			close(portResults)
			return result
		case jobs <- port:
		}
	}
	close(jobs)

	go func() {
		portWG.Wait()
		close(portResults)
	}()

	for res := range portResults {
		result.Ports[res.port] = res.status
		atomic.AddInt64(&stats.ScannedPorts, 1)

		if res.status.State == "open" {
			result.Open++
			atomic.AddInt64(&stats.OpenPorts, 1)
		} else {
			result.Closed++
			atomic.AddInt64(&stats.ClosedPorts, 1)
		}
	}

	atomic.AddInt64(&stats.ScannedHosts, 1)
	return result
}

func scanPort(ctx context.Context, ipStr string, port int, timeout time.Duration) PortStatus {
	addr := fmt.Sprintf("%s:%d", ipStr, port)
	dialer := net.Dialer{Timeout: timeout}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err == nil {
		conn.Close()
		return PortStatus{State: "open", Service: getServiceName(port)}
	}

	return PortStatus{State: "closed"}
}

func parseTargets(targets []string) []string {
	var ips []string
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		if strings.Contains(target, "/") {
			_, ipnet, err := net.ParseCIDR(target)
			if err != nil {
				continue
			}
			for ip := ipnet.IP.Mask(ipnet.Mask); ipnet.Contains(ip); incIP(ip) {
				ips = append(ips, ip.String())
			}
		} else if strings.Contains(target, "-") {
			parts := strings.Split(target, "-")
			if len(parts) == 2 {
				start, end := parseIPRange(parts[0], parts[1])
				if start == nil || end == nil {
					continue
				}
				for ip := start; bytesCompare(ip, end) <= 0; incIP(ip) {
					ips = append(ips, copyIP(ip).String())
				}
			}
		} else {
			ips = append(ips, target)
		}
	}
	return ips
}

func parsePortsStr(portsStr string) []int {
	portSet := make(map[int]struct{})

	for _, part := range strings.Split(portsStr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			rangeParts := strings.SplitN(part, "-", 2)
			if len(rangeParts) != 2 {
				continue
			}

			start, errStart := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			end, errEnd := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if errStart != nil || errEnd != nil {
				continue
			}
			if start > end {
				start, end = end, start
			}
			if start < 1 {
				start = 1
			}
			if end > 65535 {
				end = 65535
			}

			for port := start; port <= end; port++ {
				portSet[port] = struct{}{}
			}
			continue
		}

		port, err := strconv.Atoi(part)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		portSet[port] = struct{}{}
	}

	ports := make([]int, 0, len(portSet))
	for port := range portSet {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

func splitTargets(input string) []string {
	parts := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})

	var targets []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			targets = append(targets, part)
		}
	}
	return targets
}

func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func copyIP(ip net.IP) net.IP {
	dup := make(net.IP, len(ip))
	copy(dup, ip)
	return dup
}

func bytesCompare(a, b net.IP) int {
	for i := 0; i < len(a); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func parseIPRange(start, endStr string) (net.IP, net.IP) {
	parts := strings.Split(start, ".")
	if len(parts) == 4 {
		startIP := net.ParseIP(start).To4()
		if startIP == nil {
			return nil, nil
		}

		endOctet, err := strconv.Atoi(endStr)
		if err != nil || endOctet < 0 || endOctet > 255 {
			return nil, nil
		}

		endIP := copyIP(startIP)
		endIP[3] = byte(endOctet)
		return startIP, endIP
	}
	return nil, nil
}

func getServiceName(port int) string {
	services := map[int]string{
		21:   "ftp",
		22:   "ssh",
		23:   "telnet",
		25:   "smtp",
		53:   "dns",
		80:   "http",
		110:  "pop3",
		111:  "rpcbind",
		135:  "msrpc",
		139:  "netbios-ssn",
		443:  "https",
		993:  "imaps",
		995:  "pop3s",
		1723: "pptp",
		3306: "mysql",
		3389: "rdp",
		5432: "postgresql",
		5900: "vnc",
		8080: "http-proxy",
	}
	if name, ok := services[port]; ok {
		return name
	}
	return ""
}
