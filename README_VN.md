<div align="center">

# 🚀 Custom Proxy

Ứng dụng quản lý và điều hướng **Custom Proxy** nhẹ, hiệu năng cao được xây dựng trên nền tảng **Go** (Backend) và **Wails** (Desktop GUI Framework).

![Wails](https://img.shields.io/badge/Wails-v2-red?style=flat-square&logo=wails)
![Go](https://img.shields.io/badge/Go-1.20+-00ADD8?style=flat-square&logo=go)
![NodeJS](https://img.shields.io/badge/Node.js-18+-339933?style=flat-square&logo=node.js)
![License](https://img.shields.io/badge/License-MIT-blue?style=flat-square)

[🇻🇳 Tiếng Việt](README_VN.md) | **🇬🇧 English**

</div>

## 🚀 Tính năng chính

* **Hiệu năng cao & Mượt mà:** Sử dụng Backend Go thuần túy giúp xử lý luồng dữ liệu proxy nhanh chóng và tối ưu tài nguyên RAM/CPU.
* **Giao diện hiện đại:** Giao diện người dùng nhẹ, dễ thao tác được tích hợp qua Wails.
* **Cấu hình linh hoạt:** Dễ dàng thêm, chỉnh sửa và chuyển đổi qua lại giữa các node Proxy.
* **Cross-platform:** Hỗ trợ đóng gói dễ dàng cho hệ điều hành Windows (và macOS/Linux).

### 📸 Screenshot

<div align="center">
  <img width="466" height="533" alt="image" src="https://github.com/user-attachments/assets/a8a0cc01-64bf-4416-b926-fe1db30e32f2" />
</div>

---

### 📥 Download

Latest Release: **v1.0**

👉 **[Download CustomProxy.zip (GitHub Releases)](https://github.com/toanbbpro/CustomProxy/releases/latest)**

---

## 🛠️ Yêu cầu hệ thống (Prerequisites)

Để phát triển hoặc build dự án từ nguồn, máy tính của bạn cần cài đặt sẵn:

* **Go** (v1.20 trở lên)
* **Node.js** (v18 trở lên) & **npm**
* **Wails CLI** (v2.x)

Cài đặt Wails CLI nếu bạn chưa có:
```bash
go install [github.com/wailsapp/wails/v2/cmd/wails@latest](https://github.com/wailsapp/wails/v2/cmd/wails@latest)
