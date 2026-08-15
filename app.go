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
	data, err := os.ReadFile("domains.txt")
	if err != nil {
		return "example.com\napi.example.com"
	}
	return string(data)
}

// extractDomain trích xuất hostname từ chuỗi URL hoặc domain
func extractDomain(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Thêm scheme nếu thiếu để url.Parse hoạt động
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err == nil && u.Hostname() != "" {
		return strings.ToLower(u.Hostname())
	}
	// Fallback: lấy phần trước dấu '/' hoặc ':' đầu tiên
	if idx := strings.IndexAny(raw, "/:?#"); idx != -1 {
		host := strings.TrimPrefix(strings.SplitN(raw, "://", 2)[0], "")
		if host != "" {
			return strings.ToLower(host)
		}
		return strings.ToLower(raw[:idx])
	}
	// Nếu là domain thuần
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

	// Ghi file sạch
	err := os.WriteFile("domains.txt", []byte(cleanedText), 0644)
	if err != nil {
		return "Lỗi khi lưu file: " + err.Error() + " / Error saving file: " + err.Error()
	}

	// Gọi updateHosts với danh sách đã làm sạch
	updateHosts(cleanDomains, true)

	return fmt.Sprintf("Đã lưu và áp dụng %d domain thành công! / Saved & applied %d domains successfully!", len(cleanDomains), len(cleanDomains))
}

func (a *App) ToggleLogging(enable bool) string {
	isLogging = enable
	if enable {
		writeLog("Bắt đầu ghi file Log...")
		return "Đã bật ghi file Log! / Logging enabled!"
	}
	closeLogFile()
	return "Đã tắt ghi file Log! / Logging disabled!"
}

func (a *App) OpenLogFolder() {
	logDir := "logs"
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
		// Dùng cmd /c để chuỗi lệnh sc được Windows parse một cách chuẩn xác (quan trọng là binPath= "...")
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
