package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"arkade/agent/internal/compose"
	"arkade/protocol/envelope"
	"gopkg.in/yaml.v3"
)

const (
	selfUpdateArchiveDir     = "/arkade-selfupdate-input"
	selfUpdatePrivateDir     = "/tmp/arkade-selfupdate"
	selfUpdateComposeName    = "compose.yaml"
	selfUpdateEnvName        = "snapshot.env"
	selfUpdatePrivateCompose = selfUpdatePrivateDir + "/" + selfUpdateComposeName
	selfUpdatePrivateEnv     = selfUpdatePrivateDir + "/" + selfUpdateEnvName
)

// 这是唯一固定脚本；调用方不能注入 shell 文本。归档先以 0700/0600
// 写入助手私有层，启动后先转移到 tmpfs、删除私有层输入，然后才执行
// Compose。Compose 输出可能含插值明文，日志仅保留固定诊断。
const selfUpdateManagedScript = `
set -eu
umask 077
cleanup() { rm -rf ` + selfUpdateArchiveDir + ` ` + selfUpdatePrivateDir + ` >/dev/null 2>&1; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
if ! (mkdir ` + selfUpdatePrivateDir + ` &&
      cp ` + selfUpdateArchiveDir + `/` + selfUpdateComposeName + ` ` + selfUpdatePrivateCompose + ` &&
      cp ` + selfUpdateArchiveDir + `/` + selfUpdateEnvName + ` ` + selfUpdatePrivateEnv + ` &&
      rm -rf ` + selfUpdateArchiveDir + `) >/dev/null 2>&1; then
    echo ` + envelope.SelfUpdateManagedPrepareFailed + ` >&2
    exit 1
fi
if ! docker "$@" >/dev/null 2>&1; then
    echo ` + envelope.SelfUpdateManagedComposeFailed + ` >&2
    exit 1
fi
`

// Compose 的 dotenv 接受额外分隔符，不能仅按 KEY=VALUE 识别控制项。
// 只拒绝 Compose 自身控制变量，不删除或改写业务变量。
var managedComposeEnvKey = regexp.MustCompile(`(?m)^[\p{Zs}\t\r]*\x{FEFF}?[\p{Zs}\t]*(?:export[\p{Zs}\t]+)?COMPOSE_[A-Za-z0-9_]*[\p{Zs}\t]*(?:[=:]|\r?$)`)

func validateManagedSelfUpdate(req DetachedComposeUp) error {
	if req.Managed == nil {
		if req.ConfigFile == selfUpdatePrivateCompose {
			return selfUpdateBlocked("当前容器上次由纳管配置部署；本次未提供已同步的纳管配置，无法自更新")
		}
		return nil
	}
	if req.Managed.Project != req.Project {
		return selfUpdateBlocked("纳管配置的项目与当前容器所属项目不一致，已拒绝自更新")
	}
	for _, reserved := range []string{"/tmp", selfUpdateArchiveDir, "/var/run/docker.sock"} {
		if req.WorkingDir == reserved || strings.HasPrefix(req.WorkingDir, reserved+"/") || strings.HasPrefix(reserved, req.WorkingDir+"/") {
			return selfUpdateBlocked("部署目录与自更新容器的内部路径冲突，无法使用纳管配置自更新")
		}
	}
	if strings.TrimSpace(req.Managed.Content) == "" || len(req.Managed.Content) > maxComposeSourceBytes || strings.ContainsRune(req.Managed.Content, 0) {
		return selfUpdateBlocked("纳管 Compose 配置为空、过大或不是文本，无法自更新")
	}
	if err := compose.ValidateEnvContent(req.Managed.EnvContent); err != nil {
		return selfUpdateBlocked("纳管环境变量文件格式不受支持，无法自更新")
	}
	if managedComposeEnvKey.MatchString(req.Managed.EnvContent) {
		return selfUpdateBlocked("纳管环境变量文件含 Compose 自身控制变量，无法安全自更新")
	}
	return validateManagedSelfUpdateCompose(req.Service, req.Managed.Content)
}

func validateManagedSelfUpdateCompose(service, content string) error {
	blocked := func() error {
		return selfUpdateBlocked("纳管 Compose 配置无法确认只更新当前服务，已拒绝自更新")
	}
	// 只认一个文档，且只认原服务的字面名称，避免 include/extends 重定向。
	dec := yaml.NewDecoder(strings.NewReader(content))
	var model struct {
		Include  yaml.Node
		Services map[string]struct {
			Extends yaml.Node
			Scale   yaml.Node
			Deploy  struct{ Replicas yaml.Node }
		}
	}
	if err := dec.Decode(&model); err != nil || model.Include.Kind != 0 {
		return blocked()
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return blocked()
	}
	own, ok := model.Services[service]
	if !ok || !singleSelfUpdateReplica(own.Scale) || !singleSelfUpdateReplica(own.Deploy.Replicas) {
		return blocked()
	}
	for _, svc := range model.Services {
		if svc.Extends.Kind != 0 {
			return blocked()
		}
	}
	return nil
}

func singleSelfUpdateReplica(n yaml.Node) bool {
	return n.Kind == 0 || n.Kind == yaml.ScalarNode && n.Value == "1"
}

func (e *engine) putManagedSelfUpdateArchive(ctx context.Context, id string, config *envelope.SelfUpdateManagedConfig) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dir := strings.TrimPrefix(selfUpdateArchiveDir, "/")
	if err := tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		return err
	}
	for _, f := range []struct{ name, value string }{
		{selfUpdateComposeName, config.Content}, {selfUpdateEnvName, config.EnvContent},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: path.Join(dir, f.name), Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(f.value))}); err != nil {
			return err
		}
		if _, err := io.WriteString(tw, f.value); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	uri := pathContainerPrefix + url.PathEscape(id) + "/archive?path=%2F"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, e.client.url(uri), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := e.client.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("引擎返回 HTTP %d", resp.StatusCode)
	}
	return nil
}
