package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type App struct {
	ctx context.Context
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

func (a *App) LoadDomains() string {
	data, err := os.ReadFile(getAppPath("domains.txt"))
	if err != nil {
		return "example.com\napi.example.com"
	}
	return string(data)
}

func extractDomain(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err == nil && u.Hostname() != "" {
		return strings.ToLower(u.Hostname())
	}
	if idx := strings.IndexAny(raw, "/:?#"); idx != -1 {
		host := strings.TrimPrefix(strings.SplitN(raw, "://", 2)[0], "")
		if host != "" {
			return strings.ToLower(host)
		}
		return strings.ToLower(raw[:idx])
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(raw, "https://"), "http://"))
}

func (a *App) SaveDomains(domainText string) string {
	lines := strings.Split(domainText, "\n")
	domainSet := make(map[string]bool)
	var cleanDomains []string
	for _, line := range lines {
		d := extractDomain(line)
		if d != "" && !domainSet[d] {
			domainSet[d] = true
			cleanDomains = append(cleanDomains, d)
		}
	}
	cleanedText := strings.Join(cleanDomains, "\n")
	err := os.WriteFile(getAppPath("domains.txt"), []byte(cleanedText), 0644)
	if err != nil {
		return "Lỗi lưu file: " + err.Error()
	}
	updateHosts(cleanDomains, true)
	return fmt.Sprintf("Đã lưu và áp dụng %d domain thành công! / Saved & applied %d domains!", len(cleanDomains), len(cleanDomains))
}

func (a *App) GetConfig() map[string]bool {
	return map[string]bool{"steam": isSteamEnabled, "logging": isLogging}
}

func (a *App) ToggleLogging(enable bool) string {
	isLogging = enable
	saveConfig()
	if enable {
		writeLog("Bắt đầu ghi file Log...")
		return "Đã bật ghi file Log! / Logging enabled!"
	}
	closeLogFile()
	return "Đã tắt ghi file Log! / Logging disabled!"
}

func (a *App) ToggleSteamProxy(enable bool) string {
	isSteamEnabled = enable
	saveConfig()
	domains := loadDomainsFromFile()
	updateHosts(domains, true)
	if enable {
		writeLog("Đã gộp Steam vào Proxy")
		return "Đã kích hoạt Steam Proxy! / Steam Proxy enabled!"
	}
	writeLog("Đã gỡ Steam khỏi Proxy")
	return "Đã tắt Steam Proxy! / Steam Proxy disabled!"
}

func (a *App) OpenLogFolder() {
	logDir := getAppPath("logs")
	_ = os.MkdirAll(logDir, 0755)
	_ = exec.Command("explorer", logDir).Start()
}

func (a *App) OpenHomePage() {
	cmd := exec.Command("cmd", "/c", "start", "https://github.com/toanbbpro/CustomProxy")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

func (a *App) ManageService(action string) string {
	exePath, err := os.Executable()
	if err != nil {
		return "Không lấy được đường dẫn file"
	}
	switch action {
	case "install":
		cmdStr := fmt.Sprintf(`sc create CustomProxyService binPath= "%s" start= auto`, exePath)
		cmd1 := exec.Command("cmd", "/c", cmdStr)
		cmd1.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd1.Run(); err != nil {
			return "Lỗi tạo Service: " + err.Error()
		}
		cmd2 := exec.Command("cmd", "/c", "sc start CustomProxyService")
		cmd2.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd2.Run(); err != nil {
			return "Đã cài Service nhưng chưa khởi động được: " + err.Error()
		}
		return "Đã cài Service thành công! / Service installed!"
	case "uninstall":
		cmd1 := exec.Command("cmd", "/c", "sc stop CustomProxyService")
		cmd1.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = cmd1.Run()
		cmd2 := exec.Command("cmd", "/c", "sc delete CustomProxyService")
		cmd2.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd2.Run(); err != nil {
			return "Lỗi gỡ Service: " + err.Error()
		}
		return "Đã gỡ Service thành công! / Service uninstalled!"
	default:
		return "Thao tác không hợp lệ"
	}
}

// ================= TEST IP BẰNG TLS HANDSHAKE THẬT =================
// Trả về thông tin cert để chẩn đoán chính xác IP có phục vụ domain không.
func testTLSCert(ip, domain string) string {
	conf := &tls.Config{
		ServerName:         domain,
		InsecureSkipVerify: true, // Chúng ta tự kiểm tra VerifyHostname
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(ip, "443"), conf)
	if err != nil {
		return fmt.Sprintf("❌ %s -> TLS FAIL: %v", ip, err)
	}
	defer conn.Close()

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return fmt.Sprintf("⚠️ %s -> Kết nối OK nhưng không có cert", ip)
	}
	leaf := state.PeerCertificates[0]
	cn := leaf.Subject.CommonName
	sans := strings.Join(leaf.DNSNames, ", ")
	if len(sans) > 80 {
		sans = sans[:80] + "..."
	}

	// Kiểm tra domain có khớp không
	verifyErr := leaf.VerifyHostname(domain)
	if verifyErr == nil {
		return fmt.Sprintf("✅ %s -> cert OK (CN=%s)", ip, cn)
	}
	return fmt.Sprintf("❌ %s -> cert SAI. CN=%s | SAN=[%s] | Lỗi: %v",
		ip, cn, sans, verifyErr)
}

func (a *App) TestDomainStaticIPs(domain string) string {
	ips, ok := staticCleanIPs[domain]
	if !ok || len(ips) == 0 {
		return fmt.Sprintf("Domain %s không có IP tĩnh trong danh sách", domain)
	}
	var results []string
	for _, ip := range ips {
		results = append(results, testTLSCert(ip, domain))
	}
	return strings.Join(results, "<br>")
}

// Test tất cả IP từ DoH
func (a *App) TestDoHForDomain(domain string) string {
	ips, err := resolveDoH(domain)
	if err != nil {
		return fmt.Sprintf("DoH lỗi: %v", err)
	}
	if len(ips) == 0 {
		return "DoH không trả IP nào"
	}
	var results []string
	for _, ip := range ips {
		// Test TCP trước (nhanh)
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "443"), 3*time.Second)
		if err != nil {
			results = append(results, fmt.Sprintf("❌ %s -> TCP FAIL", ip))
			continue
		}
		conn.Close()
		// Test TLS cert (chỉ cho 3 IP đầu để tránh chậm)
		if len(results) < 5 {
			results = append(results, testTLSCert(ip, domain))
		} else {
			results = append(results, fmt.Sprintf("✅ %s -> TCP OK (không test cert)", ip))
		}
	}
	return strings.Join(results, "<br>")
}
