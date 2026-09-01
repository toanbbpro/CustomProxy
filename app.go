package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

type App struct {
	ctx context.Context
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

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
		return "Lỗi khi lưu file: " + err.Error() + " / Error saving file: " + err.Error()
	}

	updateHosts(cleanDomains, true)

	return fmt.Sprintf("Đã lưu và áp dụng %d domain thành công! / Saved & applied %d domains successfully!", len(cleanDomains), len(cleanDomains))
}

// Trả về cấu hình hiện tại cho UI Checkbox lúc khởi động
func (a *App) GetConfig() map[string]bool {
	return map[string]bool{
		"steam":   isSteamEnabled,
		"logging": isLogging,
	}
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

// Logic bật/tắt Steam Proxy
func (a *App) ToggleSteamProxy(enable bool) string {
	isSteamEnabled = enable
	saveConfig() // Lưu lại lựa chọn

	// Cập nhật lại file hosts ngay lập tức
	domains := loadDomainsFromFile()
	updateHosts(domains, true)

	if enable {
		writeLog("Đã gộp cấu hình Steam vào Proxy chung")
		return "Đã kích hoạt và áp dụng Steam Proxy! / Steam Proxy enabled!"
	}
	writeLog("Đã gỡ cấu hình Steam khỏi Proxy")
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
		return "Không lấy được đường dẫn file / Cannot get executable path"
	}

	if action == "install" {
		cmdStr := fmt.Sprintf(`sc create CustomProxyService binPath= "%s" start= auto`, exePath)
		cmd1 := exec.Command("cmd", "/c", cmdStr)
		cmd1.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd1.Run(); err != nil {
			return "Lỗi tạo Service: " + err.Error()
		}

		cmd2 := exec.Command("cmd", "/c", "sc start CustomProxyService")
		cmd2.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd2.Run(); err != nil {
			return "Đã cài Service nhưng chưa thể tự khởi động: " + err.Error()
		}

		return "Đã cài đặt Service thành công! / Service installed successfully!"
	} else if action == "uninstall" {
		cmd1 := exec.Command("cmd", "/c", "sc stop CustomProxyService")
		cmd1.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = cmd1.Run()

		cmd2 := exec.Command("cmd", "/c", "sc delete CustomProxyService")
		cmd2.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd2.Run(); err != nil {
			return "Lỗi gỡ Service: " + err.Error()
		}

		return "Đã gỡ Service thành công! / Service uninstalled successfully!"
	}

	return "Thao tác không hợp lệ / Invalid action"
}
