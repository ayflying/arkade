package runtime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 套接字发现策略。
//
// 为什么要"发现"而不是写死一个路径：Docker 与 Podman 的套接字位置按「引擎 + 安装方式 +
// 用户态」组合有 4 种常见落点，且 rootless Podman 的路径依赖 XDG_RUNTIME_DIR
// （systemd 用户会话下的 /run/user/<uid>）。把它们集中在这里，是为了让探测顺序
// 可读、可测，而不是散落在工厂函数里。
//
// 判定顺序（先引擎优先、后用户态）：
//  1. 配置显式指定的端点（cfg.Socket）——用户永远有最高优先权；
//  2. sqlite/环境变量带来的 DOCKER_HOST（用于 Windows/macOS 开发时连 tcp://127.0.0.1:2375）；
//  3. Docker 默认套接字 /var/run/docker.sock；
//  4. Podman rootless：$XDG_RUNTIME_DIR/podman/podman.sock，缺省回退 /run/user/<uid>/podman/podman.sock；
//  5. Podman root：/run/podman/podman.sock。
const (
	// DockerDefaultSocket 是 Docker 守护进程的默认套接字。
	DockerDefaultSocket = "/var/run/docker.sock"
	// PodmanRootSocket 是 root 身份下 Podman 的套接字。
	PodmanRootSocket = "/run/podman/podman.sock"
	// podmanRuntimeSubdir 是 rootless Podman 在运行时目录下的子路径。
	podmanRuntimeSubdir = "podman/podman.sock"
)

// discoverEndpoints 生成候选端点列表（未去重时按优先级排列）。
//
// explicit 非空时只返回它一个：用户显式配置了端点就意味着"别猜了"，
// 探测阶段的版本判定失败直接报错，不做静默回退（静默回退会让排障变得很难）。
func discoverEndpoints(explicit string) []string {
	if s := strings.TrimSpace(explicit); s != "" {
		return []string{normalizeSocket(s)}
	}

	var out []string
	// DOCKER_HOST 只在本机没有 unix 套接字时才真正需要，但顺序放在最前更符合
	// docker CLI 的直觉（用户设了这个变量就是想连那里）。
	if h := strings.TrimSpace(os.Getenv("DOCKER_HOST")); h != "" {
		out = append(out, normalizeSocket(h))
	}
	if runtime.GOOS != "windows" {
		out = append(out, "unix://"+DockerDefaultSocket)
		out = append(out, podmanRootlessSockets()...)
		out = append(out, "unix://"+PodmanRootSocket)
	}
	return dedupe(out)
}

// podmanRootlessSockets 返回 rootless Podman 的候选套接字。
//
// XDG_RUNTIME_DIR 在交互式 SSH 登录时通常存在（/run/user/<uid>），但在
// systemd 系统服务里往往没有——所以显式补一个 /run/user/<uid> 的回退，
// 这样同一份配置在"手动运行"和"装成服务运行"两种情况下都能连上。
func podmanRootlessSockets() []string {
	var out []string
	if dir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); dir != "" {
		out = append(out, "unix://"+filepath.ToSlash(filepath.Join(dir, podmanRuntimeSubdir)))
	}
	if os.Getuid() > 0 {
		out = append(out, "unix://"+filepath.ToSlash(filepath.Join("/run/user", itoa(os.Getuid()), podmanRuntimeSubdir)))
	}
	return out
}

// normalizeSocket 把用户写的端点补全成 client 认识的格式：
// 裸路径加 unix:// 前缀；tcp 端点原样保留。
func normalizeSocket(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if strings.Contains(s, "://") {
		return s
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\`) {
		return "unix://" + filepath.ToSlash(s)
	}
	// 形如 127.0.0.1:2375 的裸地址按 tcp 处理，便于开发时少写前缀。
	return "tcp://" + s
}

// dedupe 去重并保持原顺序。
func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// itoa 是 strconv.Itoa 的极小封装，避免在本文件引入整个 strconv 只为一次调用。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
