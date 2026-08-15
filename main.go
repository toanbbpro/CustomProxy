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

var assets embed.FS

var (
	httpListener  net.Listener
	httpsListener net.Listener
	isLogging     bool
	logFile       *os.File
	logMutex      sync.Mutex

	customTransport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := resolveDoH(host)
			if err != nil || len(ips) == 0 {
				return nil, fmt.Errorf("không phân giải được IP bằng DoH cho %s: %v", host, err)
			}
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
		},
		ResponseHeaderTimeout: 15 * time.Second,
	}
	customHTTPClient = &http.Client{
		Transport: customTransport,
		Timeout:   30 * time.Second,
	}
)

type proxyService struct{}

func (m *proxyService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

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
		default:
		}
	}
	return false, 0
}

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
	proc.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(exePtr)),
		uintptr(unsafe.Pointer(argPtr)),
		uintptr(unsafe.Pointer(cwdPtr)),
		1, // SW_NORMAL
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

	app := NewApp()

	go func() {
		domains := loadDomainsFromFile()
		updateHosts(domains, true)
		runProxy()
	}()

	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData` // Fallback an toàn
	}
	userDataPath := filepath.Join(programData, "CustomProxy_Data")

	err = wails.Run(&options.App{
		Title:  "Custom Proxy v1.0 - by ToanBB",
		Width:  480,
		Height: 540,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
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
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			WebviewUserDataPath: userDataPath,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

func writeLog(message string) {
	if !isLogging {
		return
	}
	logMutex.Lock()
	defer logMutex.Unlock()

	logDir := "logs"
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

func resolveDoH(domain string) ([]string, error) {
	url := fmt.Sprintf("https://cloudflare-dns.com/dns-query?name=%s&type=A", domain)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Answer []struct {
			Data string `json:"data"`
			Type int    `json:"type"`
		} `json:"Answer"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var ips []string
	for _, a := range result.Answer {
		if a.Type == 1 {
			ips = append(ips, a.Data)
		}
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("không tìm thấy IP nào")
	}
	return ips, nil
}

func loadDomainsFromFile() []string {
	data, err := os.ReadFile("domains.txt")
	if err != nil {
		defaultDomains := []string{"example.com", "api.example.com"}
		_ = os.WriteFile("domains.txt", []byte("example.com\napi.example.com"), 0644)
		return defaultDomains
	}

	lines := strings.Split(string(data), "\n")
	var domains []string
	for _, line := range lines {
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

	lines := strings.Split(string(content), "\n")
	var newLines []string

	for _, line := range lines {
		if !strings.Contains(line, "# CustomProxy") {
			newLines = append(newLines, line)
		}
	}

	if enable {
		for _, domain := range domains {
			newLines = append(newLines, fmt.Sprintf("127.0.0.1 %s # CustomProxy", domain))
		}
	}

	output := strings.Join(newLines, "\n")
	_ = os.WriteFile(hostsPath, []byte(output), 0644)

	// Lệnh flushdns chạy ngầm (Ẩn cửa sổ)
	cmd := exec.Command("ipconfig", "/flushdns")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()

	writeLog(fmt.Sprintf("Đã cập nhật file hosts (%d domains)", len(domains)))
}

func runProxy() {
	var err error

	httpListener, err = net.Listen("tcp", "127.0.0.1:80")
	if err == nil {
		writeLog("HTTP Proxy đang lắng nghe trên cổng 80...")
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
			writeLog(fmt.Sprintf("[HTTP] %s http://%s%s -> %d (%d bytes, %v)", r.Method, r.Host, r.URL.Path, resp.StatusCode, written, time.Since(start)))
		}))
	}

	httpsListener, err = net.Listen("tcp", "127.0.0.1:443")
	if err == nil {
		writeLog("HTTPS SNI Proxy đang lắng nghe trên cổng 443...")
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

	_ = clientConn.SetReadDeadline(time.Now().Add(4 * time.Second))
	buf := make([]byte, 8192)
	n, err := clientConn.Read(buf)
	if err != nil || n == 0 {
		return
	}
	headerData := buf[:n]

	if len(headerData) >= 5 && headerData[0] == 0x16 {
		recordLen := int(headerData[3])<<8 | int(headerData[4])
		expectedTotal := 5 + recordLen
		for len(headerData) < expectedTotal && len(headerData) < 16384 {
			tmp := make([]byte, 2048)
			_ = clientConn.SetReadDeadline(time.Now().Add(1 * time.Second))
			n2, err2 := clientConn.Read(tmp)
			if err2 != nil || n2 == 0 {
				break
			}
			headerData = append(headerData, tmp[:n2]...)
		}
	}
	_ = clientConn.SetReadDeadline(time.Time{})

	serverName := extractSNI(headerData)
	if serverName == "" {
		writeLog("[HTTPS-ERR] Không thể bóc tách SNI từ trình duyệt")
		return
	}

	ips, err := resolveDoH(serverName)
	if err != nil || len(ips) == 0 {
		writeLog(fmt.Sprintf("[HTTPS-ERR] DNS DoH thất bại cho %s: %v", serverName, err))
		return
	}

	var targetConn net.Conn
	var selectedIP string
	for _, ip := range ips {
		conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort(ip, "443"), 4*time.Second)
		if dialErr == nil {
			targetConn = conn
			selectedIP = ip
			break
		}
	}

	if targetConn == nil {
		writeLog(fmt.Sprintf("[HTTPS-ERR] Timeout - Không thể nối TCP tới %s", serverName))
		return
	}
	defer targetConn.Close()

	chunkSize := 20
	for i := 0; i < len(headerData); i += chunkSize {
		end := i + chunkSize
		if end > len(headerData) {
			end = len(headerData)
		}
		_, writeErr := targetConn.Write(headerData[i:end])
		if writeErr != nil {
			writeLog(fmt.Sprintf("[HTTPS-ERR] Gửi dữ liệu chunk %d thất bại: %v", i, writeErr))
			return
		}
		time.Sleep(2 * time.Millisecond)
	}

	writeLog(fmt.Sprintf("[HTTPS-SNI] Tunnel Bypass Thành Công -> %s (%s)", serverName, selectedIP))

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

func extractSNI(data []byte) string {
	if len(data) < 5 || data[0] != 0x16 {
		return ""
	}
	clientHello := data[5:]
	if len(clientHello) < 38 || clientHello[0] != 0x01 {
		return ""
	}
	offset := 38
	if len(clientHello) < offset+1 {
		return ""
	}
	sessionIDLen := int(clientHello[offset])
	offset += 1 + sessionIDLen
	if len(clientHello) < offset+2 {
		return ""
	}
	cipherLen := int(clientHello[offset])<<8 | int(clientHello[offset+1])
	offset += 2 + cipherLen
	if len(clientHello) < offset+1 {
		return ""
	}
	compLen := int(clientHello[offset])
	offset += 1 + compLen
	if len(clientHello) < offset+2 {
		return ""
	}
	extsLen := int(clientHello[offset])<<8 | int(clientHello[offset+1])
	offset += 2
	if len(clientHello) < offset+extsLen {
		extsLen = len(clientHello) - offset
	}
	extData := clientHello[offset : offset+extsLen]
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

func stopProxy() {
	if httpListener != nil {
		_ = httpListener.Close()
	}
	if httpsListener != nil {
		_ = httpsListener.Close()
	}
	writeLog("Đã dừng toàn bộ dịch vụ Proxy")
}

func IsServiceRunning() bool {
	// Lệnh check service chạy ngầm (Ẩn cửa sổ)
	cmd := exec.Command("sc", "query", "CustomProxyService")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "RUNNING")
}
