package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"golang.org/x/sys/windows/svc"
)

//go:embed all:frontend/dist
var assets embed.FS

var (
	httpListener   net.Listener
	httpsListener  net.Listener
	isLogging      bool
	isSteamEnabled bool
	logFile        *os.File
	logMutex       sync.Mutex

	steamDomains = []string{
		"steamcommunity.com", "store.steampowered.com", "checkout.steampowered.com",
		"login.steampowered.com", "help.steampowered.com", "community.cloudflare.steamstatic.com",
		"steamcommunity-a.akamaihd.net",
	}

	// IP ưu tiên cho domain bị chặn nặng (dùng cho công cụ chẩn đoán và dự phòng)
	staticCleanIPs = map[string][]string{
		"steamcommunity.com": {
			"23.53.209.18",
		},
		"store.steampowered.com": {
			"23.53.210.94",
			"23.53.209.18",
		},
	}


	customTransport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			ips := collectCandidateIPs(host)
			if len(ips) == 0 {
				return nil, fmt.Errorf("không có IP cho %s", host)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
		},
		ResponseHeaderTimeout: 15 * time.Second,
	}
	customHTTPClient = &http.Client{
		Transport: customTransport,
		Timeout:   30 * time.Second,
	}
)

// ================= HỆ THỐNG QUẢN LÝ ĐƯỜNG DẪN TƯƠNG ĐỐI & CONFIG =================
type AppConfig struct {
	Logging bool `json:"logging"`
	Steam   bool `json:"steam"`
}

func getAppPath(filename string) string {
	exe, err := os.Executable()
	if err != nil {
		return filename
	}
	return filepath.Join(filepath.Dir(exe), filename)
}

func loadConfig() {
	data, err := os.ReadFile(getAppPath("config.json"))
	if err == nil {
		var c AppConfig
		json.Unmarshal(data, &c)
		isLogging = c.Logging
		isSteamEnabled = c.Steam
	}
}

func saveConfig() {
	c := AppConfig{Logging: isLogging, Steam: isSteamEnabled}
	data, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(getAppPath("config.json"), data, 0644)
}

// ================= GIAO TIẾP WINDOWS SERVICE =================
type proxyService struct{}

func (m *proxyService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	loadConfig()
	go func() {
		domains := loadDomainsFromFile()
		updateHosts(domains, true)
		runProxy()
	}()

	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			stopProxy()
			domains := loadDomainsFromFile()
			updateHosts(domains, false)
			return false, 0
		}
	}
	return false, 0
}

// ================= ADMIN =================
func checkAdmin() bool {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("IsUserAnAdmin")
	ret, _, _ := proc.Call()
	return ret != 0
}

func elevateAdmin() {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	verb, _ := syscall.UTF16PtrFromString("runas")
	exePtr, _ := syscall.UTF16PtrFromString(exe)
	cwdPtr, _ := syscall.UTF16PtrFromString(cwd)
	argPtr, _ := syscall.UTF16PtrFromString("")

	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("ShellExecuteW")
	proc.Call(0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(exePtr)),
		uintptr(unsafe.Pointer(argPtr)),
		uintptr(unsafe.Pointer(cwdPtr)),
		1,
	)
	os.Exit(0)
}

func main() {
	isWinService, err := svc.IsWindowsService()
	if err == nil && isWinService {
		_ = svc.Run("CustomProxyService", &proxyService{})
		return
	}

	if !checkAdmin() {
		elevateAdmin()
		return
	}

	loadConfig()
	app := NewApp()

	go func() {
		domains := loadDomainsFromFile()
		updateHosts(domains, true)
		runProxy()
	}()

	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	userDataPath := filepath.Join(programData, "CustomProxy_Data")

	err = wails.Run(&options.App{
		Title:            "Custom Proxy v1.4.0 - by ToanBB",
		Width:            520,
		Height:           520,
		DisableResize:    true,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 27, G: 40, B: 56, A: 1},
		OnStartup:        app.startup,
		OnShutdown: func(ctx context.Context) {
			closeLogFile()
			if !IsServiceRunning() {
				stopProxy()
				domains := loadDomainsFromFile()
				updateHosts(domains, false)
			}
		},
		Bind:    []interface{}{app},
		Windows: &windows.Options{WebviewUserDataPath: userDataPath},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

// ================= GHI LOG =================
func writeLog(message string) {
	if !isLogging {
		return
	}
	logMutex.Lock()
	defer logMutex.Unlock()

	logDir := getAppPath("logs")
	_ = os.MkdirAll(logDir, 0755)

	if logFile == nil {
		filename := fmt.Sprintf("proxy-log-%s.log", time.Now().Format("20060102_150405"))
		path := filepath.Join(logDir, filename)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return
		}
		logFile = f
	} else {
		info, err := logFile.Stat()
		if err == nil && info.Size() > 8*1024*1024 {
			logFile.Close()
			filename := fmt.Sprintf("proxy-log-%s.log", time.Now().Format("20060102_150405"))
			path := filepath.Join(logDir, filename)
			f, _ := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			logFile = f
		}
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logEntry := fmt.Sprintf("[%s] %s\n", timestamp, message)
	_, _ = logFile.WriteString(logEntry)
}

func closeLogFile() {
	logMutex.Lock()
	defer logMutex.Unlock()
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}
}

// ================= DNS OVER HTTPS (SONG SONG NHIỀU SERVER + ĐA VÙNG ECS) =================
var dohClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 3 * time.Second}
			switch addr {
			case "cloudflare-dns.com:443":
				return dialer.DialContext(ctx, network, "104.16.249.249:443")
			case "dns.google:443":
				return dialer.DialContext(ctx, network, "8.8.8.8:443")
			case "dns.alidns.com:443":
				return dialer.DialContext(ctx, network, "223.5.5.5:443")
			}
			return dialer.DialContext(ctx, network, addr)
		},
	},
}

func resolveDoH(domain string) ([]string, error) {
	// Kết hợp DoH nội địa và DoH kèm subnet quốc tế (ECS) để chống triệt để ISP đổi dải IP chặn
	servers := []string{
		"https://dns.google/resolve?name=%s&type=A",
		"https://dns.google/resolve?name=%s&type=A&edns_client_subnet=1.0.0.0/24",
		"https://cloudflare-dns.com/dns-query?name=%s&type=A",
		"https://dns.alidns.com/resolve?name=%s&type=1",
	}

	type res struct {
		ips []string
		err error
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	resCh := make(chan res, len(servers))
	for _, s := range servers {
		go func(endpoint string) {
			url := fmt.Sprintf(endpoint, domain)
			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				resCh <- res{nil, err}
				return
			}
			req.Header.Set("Accept", "application/dns-json")
			resp, err := dohClient.Do(req)
			if err != nil {
				resCh <- res{nil, err}
				return
			}
			defer resp.Body.Close()
			var parsed struct {
				Answer []struct {
					Data string `json:"data"`
					Type int    `json:"type"`
				} `json:"Answer"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
				resCh <- res{nil, err}
				return
			}
			var ips []string
			for _, a := range parsed.Answer {
				if a.Type == 1 && !strings.HasPrefix(a.Data, "127.") && !strings.Contains(a.Data, ":") {
					ips = append(ips, a.Data)
				}
			}
			resCh <- res{ips, nil}
		}(s)
	}

	ipSet := make(map[string]bool)
	var all []string
	for i := 0; i < len(servers); i++ {
		select {
		case r := <-resCh:
			for _, ip := range r.ips {
				if !ipSet[ip] {
					ipSet[ip] = true
					all = append(all, ip)
				}
			}
		case <-ctx.Done():
			i = len(servers)
		}
	}

	if len(all) > 0 {
		writeLog(fmt.Sprintf("[DNS] %s -> %d IP (gộp từ DoH đa vùng)", domain, len(all)))
		return all, nil
	}

	// Fallback UDP
	writeLog(fmt.Sprintf("[DNS-WARN] Toàn bộ DoH thất bại cho %s, dùng UDP 53", domain))
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, "udp", "8.8.4.4:53")
		},
	}
	ips, err := r.LookupHost(context.Background(), domain)
	if err == nil && len(ips) > 0 {
		var v4 []string
		for _, ip := range ips {
			if strings.Contains(ip, ".") && ip != "127.0.0.1" {
				v4 = append(v4, ip)
			}
		}
		if len(v4) > 0 {
			return v4, nil
		}
	}
	return nil, fmt.Errorf("DNS thất bại hoàn toàn")
}

// Lấy danh sách IP ứng viên từ DoH đa vùng (không block verify đồng bộ)
func collectCandidateIPs(domain string) []string {
	var result []string
	seen := make(map[string]bool)

	// 1. Lấy danh sách IP từ DoH (Google, Google ECS US, Cloudflare, AliDNS)
	dohIPs, err := resolveDoH(domain)
	if err == nil {
		for _, ip := range dohIPs {
			if !seen[ip] {
				seen[ip] = true
				result = append(result, ip)
			}
		}
	}

	// 2. Dự phòng: nếu DoH không trả IP nào, dùng danh sách IP tĩnh có sẵn
	if len(result) == 0 {
		if statics, ok := staticCleanIPs[domain]; ok {
			for _, ip := range statics {
				if !seen[ip] {
					seen[ip] = true
					result = append(result, ip)
				}
			}
		}
	}
	return result
}

// ================= HOSTS =================
func loadDomainsFromFile() []string {
	data, err := os.ReadFile(getAppPath("domains.txt"))
	if err != nil {
		_ = os.WriteFile(getAppPath("domains.txt"), []byte("example.com\napi.example.com"), 0644)
		return []string{"example.com", "api.example.com"}
	}
	var domains []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			domains = append(domains, trimmed)
		}
	}
	return domains
}

func updateHosts(domains []string, enable bool) {
	hostsPath := `C:\Windows\System32\drivers\etc\hosts`
	content, err := os.ReadFile(hostsPath)
	if err != nil {
		writeLog("Lỗi đọc file hosts: " + err.Error())
		return
	}

	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	var newLines []string
	for _, line := range lines {
		if !strings.Contains(line, "# CustomProxy") {
			newLines = append(newLines, line)
		}
	}

	if enable {
		active := append([]string{}, domains...)
		if isSteamEnabled {
			active = append(active, steamDomains...)
		}
		set := make(map[string]bool)
		for _, d := range active {
			d = strings.TrimSpace(d)
			if d != "" && !set[d] {
				set[d] = true
				newLines = append(newLines, fmt.Sprintf("127.0.0.1 %s # CustomProxy", d))
			}
		}
	}

	output := strings.Join(newLines, "\r\n") + "\r\n"
	_ = os.WriteFile(hostsPath, []byte(output), 0644)

	cmd := exec.Command("ipconfig", "/flushdns")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()

	writeLog(fmt.Sprintf("Đã cập nhật file hosts (Tổng số dòng: %d)", len(domains)))
}

// ================= PROXY SERVER =================
func runProxy() {
	var err error

	httpListener, err = net.Listen("tcp", "127.0.0.1:80")
	if err == nil {
		writeLog("HTTP Proxy lắng nghe cổng 80...")
		go http.Serve(httpListener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			r.RequestURI = ""
			r.URL.Scheme = "https"
			r.URL.Host = r.Host
			resp, err := customHTTPClient.Do(r)
			if err != nil {
				writeLog(fmt.Sprintf("[HTTP-ERR] %s %s -> %v", r.Method, r.Host, err))
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)
			written, _ := io.Copy(w, resp.Body)
			writeLog(fmt.Sprintf("[HTTP] %s http://%s%s -> %d (%d bytes, %v)",
				r.Method, r.Host, r.URL.Path, resp.StatusCode, written, time.Since(start)))
		}))
	}

	httpsListener, err = net.Listen("tcp", "127.0.0.1:443")
	if err == nil {
		writeLog("HTTPS SNI Proxy lắng nghe cổng 443...")
		go func() {
			for {
				clientConn, err := httpsListener.Accept()
				if err != nil {
					return
				}
				go handleHTTPSConnection(clientConn)
			}
		}()
	}
}

func handleHTTPSConnection(clientConn net.Conn) {
	defer clientConn.Close()

	headerData, serverName := readClientHello(clientConn)
	if len(headerData) == 0 {
		return
	}
	if serverName == "" {
		serverName = "steamcommunity.com"
	}

	ips := collectCandidateIPs(serverName)
	if len(ips) == 0 {
		writeLog(fmt.Sprintf("[HTTPS-ERR] Không có IP khả dụng cho %s", serverName))
		return
	}

	writeLog(fmt.Sprintf("[DEBUG] %s -> đua %d IP...", serverName, len(ips)))

	// Fast-Dial: Đua tất cả IP song song, IP nào hoàn thành 3-way handshake trước thì dùng luôn
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type dialRes struct {
		conn net.Conn
		ip   string
		err  error
	}
	resCh := make(chan dialRes, len(ips))

	for _, ip := range ips {
		go func(targetIP string) {
			d := &net.Dialer{Timeout: 4 * time.Second}
			conn, dialErr := d.DialContext(ctx, "tcp", net.JoinHostPort(targetIP, "443"))
			if dialErr == nil {
				select {
				case <-ctx.Done():
					conn.Close()
					return
				default:
				}
			}
			select {
			case resCh <- dialRes{conn, targetIP, dialErr}:
			case <-ctx.Done():
				if conn != nil {
					conn.Close()
				}
			}
		}(ip)
	}

	var targetConn net.Conn
	var selectedIP string
	for i := 0; i < len(ips); i++ {
		r := <-resCh
		if r.err == nil {
			targetConn = r.conn
			selectedIP = r.ip
			cancel()
			remaining := len(ips) - i - 1
			if remaining > 0 {
				go func(n int) {
					for j := 0; j < n; j++ {
						rr := <-resCh
						if rr.err == nil && rr.conn != nil {
							rr.conn.Close()
						}
					}
				}(remaining)
			}
			break
		}
	}

	if targetConn == nil {
		writeLog(fmt.Sprintf("[HTTPS-ERR] Toàn bộ %d IP của %s đều fail TCP", len(ips), serverName))
		return
	}
	defer targetConn.Close()

	// ===== GỬI NGUYÊN SNI GỐC - CHỈ FRAGMENT 1 BYTE ĐẦU (CHUẨN v0.3) =====
	if err := sendClientHelloChunked(targetConn, headerData); err != nil {
		writeLog(fmt.Sprintf("[HTTPS-ERR] Gửi ClientHello thất bại: %v", err))
		return
	}

	writeLog(fmt.Sprintf("[HTTPS-SNI] Tunnel OK -> %s (%s, %d IP khả dụng)",
		serverName, selectedIP, len(ips)))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(targetConn, clientConn)
		if tcpConn, ok := targetConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, targetConn)
		if tcpConn, ok := clientConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
	}()
	wg.Wait()
}

// ================= XỬ LÝ CLIENTHELLO =================
func readClientHello(conn net.Conn) ([]byte, string) {
	var buf []byte
	tmp := make([]byte, 4096)

	_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))

	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if sni := extractSNI(buf); sni != "" {
				_ = conn.SetReadDeadline(time.Time{})
				return buf, sni
			}
			if isClientHelloComplete(buf) {
				_ = conn.SetReadDeadline(time.Time{})
				return buf, ""
			}
			if len(buf) > 16384 {
				break
			}
		}
		if err != nil {
			break
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	return buf, extractSNI(buf)
}

func concatHandshake(data []byte) ([]byte, bool) {
	var hs []byte
	offset := 0
	for offset+5 <= len(data) {
		if data[offset] != 0x16 {
			return hs, false
		}
		recLen := int(data[offset+3])<<8 | int(data[offset+4])
		end := offset + 5 + recLen
		if end > len(data) {
			hs = append(hs, data[offset+5:]...)
			return hs, false
		}
		hs = append(hs, data[offset+5:end]...)
		offset = end
	}
	return hs, true
}

func isClientHelloComplete(data []byte) bool {
	hs, complete := concatHandshake(data)
	if !complete || len(hs) < 4 || hs[0] != 0x01 {
		return false
	}
	hsLen := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
	return len(hs) >= 4+hsLen
}

func extractSNI(data []byte) string {
	hs, _ := concatHandshake(data)
	if len(hs) < 38 || hs[0] != 0x01 {
		return ""
	}
	offset := 38
	if len(hs) < offset+1 {
		return ""
	}
	sessionIDLen := int(hs[offset])
	offset += 1 + sessionIDLen
	if len(hs) < offset+2 {
		return ""
	}
	cipherLen := int(hs[offset])<<8 | int(hs[offset+1])
	offset += 2 + cipherLen
	if len(hs) < offset+1 {
		return ""
	}
	compLen := int(hs[offset])
	offset += 1 + compLen
	if len(hs) < offset+2 {
		return ""
	}
	extsLen := int(hs[offset])<<8 | int(hs[offset+1])
	offset += 2
	if len(hs) < offset+extsLen {
		extsLen = len(hs) - offset
	}
	extData := hs[offset : offset+extsLen]

	for len(extData) >= 4 {
		extType := int(extData[0])<<8 | int(extData[1])
		extLen := int(extData[2])<<8 | int(extData[3])
		extData = extData[4:]
		if len(extData) < extLen {
			break
		}
		if extType == 0 {
			snData := extData[:extLen]
			if len(snData) >= 5 {
				listLen := int(snData[0])<<8 | int(snData[1])
				if len(snData) >= 2+listLen && snData[2] == 0 {
					nameLen := int(snData[3])<<8 | int(snData[4])
					if len(snData) >= 5+nameLen {
						return string(snData[5 : 5+nameLen])
					}
				}
			}
		}
		extData = extData[extLen:]
	}
	return ""
}

// Fragment ClientHello: gửi 1 byte đầu, nghỉ 3ms rồi gửi nốt (kỹ thuật chuẩn v0.3)
// Vừa đủ để DPI không kịp ghép SNI, vừa không bị server Akamai hiểu lầm là tấn công Slowloris
func sendClientHelloChunked(conn net.Conn, data []byte) error {
	if len(data) <= 1 {
		_, err := conn.Write(data)
		return err
	}
	if _, err := conn.Write(data[:1]); err != nil {
		return err
	}
	time.Sleep(3 * time.Millisecond)
	_, err := conn.Write(data[1:])
	return err
}

func stopProxy() {
	if httpListener != nil {
		_ = httpListener.Close()
	}
	if httpsListener != nil {
		_ = httpsListener.Close()
	}
	writeLog("Đã dừng Proxy")
}

func IsServiceRunning() bool {
	cmd := exec.Command("sc", "query", "CustomProxyService")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "RUNNING")
}
